package main

// Benchmark: a short, read-mostly test of the processor (one core and all
// cores), the memory (copy speed and latency) and one drive (sequential and
// 4K random reads and writes). The drive test writes one temporary file that
// is removed at the end (on Windows it is opened "delete on close", so it is
// gone even if the app is closed in the middle). Nothing else is changed.
//
// The points are this app's own scale: they are made to compare a PC with
// itself over time (before/after a cleaning, a new driver, a new drive) and
// with other PCs measured by this app — not with other benchmark programs.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

type benchDisk struct {
	Drive   string  `json:"drive"`
	SizeMB  int     `json:"sizeMB"`
	SeqRead float64 `json:"seqRead"`  // MB/s
	SeqWrit float64 `json:"seqWrite"` // MB/s
	RndRead float64 `json:"rndRead"`  // 4K QD1, MB/s
	RndWrit float64 `json:"rndWrite"` // 4K QD1, MB/s
	IOPSR   float64 `json:"iopsRead"`
	IOPSW   float64 `json:"iopsWrite"`
	Kind    string  `json:"kind"` // what it compares to: nvme4, nvme3, sata, hdd, slow
	Error   string  `json:"error,omitempty"`
}

type benchResult struct {
	At      time.Time  `json:"at"`
	Threads int        `json:"threads"`
	CPU1    float64    `json:"cpu1"`   // work units per second, one core
	CPUN    float64    `json:"cpuN"`   // work units per second, all cores
	RAMBW   float64    `json:"ramBW"`  // GB/s copied
	RAMLat  float64    `json:"ramLat"` // ns per random access
	Disk    *benchDisk `json:"disk,omitempty"`
	Score   int        `json:"score"`
	SCPU1   int        `json:"sCPU1"`
	SCPUN   int        `json:"sCPUN"`
	SRAM    int        `json:"sRAM"`
	SDisk   int        `json:"sDisk"`
	GPU     *benchGPU  `json:"gpu,omitempty"`
	SGPU    int        `json:"sGPU"`
	// a long run: the all-cores speed every 5 seconds (it falls when the
	// processor gets hot or hits a power limit)
	CPUSustain []float64     `json:"cpuSustain,omitempty"`
	Load0      float64       `json:"load0"` // % processor use by other programs just before
	Long       bool          `json:"long,omitempty"`
	Ctx        *benchCtx     `json:"ctx,omitempty"`
	Notes      []benchNote   `json:"notes,omitempty"`
	Balance    *benchBalance `json:"balance,omitempty"`
	Stopped    bool          `json:"stopped,omitempty"`
	Error      string        `json:"error,omitempty"`
	TookSecs   float64       `json:"took"`
}

type benchOpts struct {
	CPU   bool   `json:"cpu"`
	RAM   bool   `json:"ram"`
	Disk  bool   `json:"disk"`
	Drive string `json:"drive"` // "C:" etc.; empty = the system drive
	Quick bool   `json:"quick"` // shorter runs (tests)
	GPU   bool   `json:"gpu"`   // the page runs the graphics test after this
	Long  bool   `json:"long"`  // 30 s on all cores, to see if the speed holds
}

// Reference values: 1000 points each. They are a mid-range desktop of
// 2022 as this app measures it (a 6-core CPU, DDR4, a PCIe 3 NVMe drive).
const (
	refCPU1   = 2400.0 // units/s on one core
	refCPUN   = 14000.0
	refRAMBW  = 18.0 // GB/s
	refRAMLat = 85.0 // ns (lower is better)
	refSeqR   = 3000.0
	refSeqW   = 2000.0
	refRndR   = 55.0 // MB/s 4K QD1
	refRndW   = 120.0
	refGFLOPS = 3000.0 // the graphics shader on an RTX 3060-class card (estimated)
)

var bench struct {
	busy atomic.Bool
	stop atomic.Bool
}

var errBenchBusy = errors.New("busy")

// benchProgress is set by the platform (it sends an event to the page).
var benchProgress = func(step string, pct int) {}

// ---------------------------------------------------------------- CPU

// benchUnit is one piece of mixed work: hashing (integer, as in file and
// network work), a small fractal (floating point, as in games and media)
// and a sort (branches and memory, as in most programs).
type benchWork struct {
	buf  []byte
	ints []int
	src  []int
}

func newBenchWork(seed uint64) *benchWork {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	w := &benchWork{buf: make([]byte, 16<<10), ints: make([]int, 2048), src: make([]int, 2048)}
	for i := range w.buf {
		w.buf[i] = byte(r.Uint32())
	}
	for i := range w.src {
		w.src[i] = int(r.Uint32())
	}
	return w
}

var benchSink atomic.Uint64

func (w *benchWork) unit() {
	s := sha256.Sum256(w.buf)
	w.buf[0] ^= s[0]
	// a 48x48 Mandelbrot tile, 48 iterations
	n := 0
	for y := 0; y < 48; y++ {
		ci := -1.2 + float64(y)*0.05
		for x := 0; x < 48; x++ {
			cr := -2.0 + float64(x)*0.05
			zr, zi := 0.0, 0.0
			k := 0
			for ; k < 48 && zr*zr+zi*zi < 4; k++ {
				zr, zi = zr*zr-zi*zi+cr, 2*zr*zi+ci
			}
			n += k
		}
	}
	copy(w.ints, w.src)
	sort.Ints(w.ints)
	benchSink.Add(uint64(n) + uint64(w.ints[len(w.ints)/2]&1) + uint64(s[1]))
}

// benchCPU runs the work on n goroutines for d and returns units per second.
func benchCPU(n int, d time.Duration, step string) float64 {
	r, _ := benchCPUSlices(n, d, 0, step)
	return r
}

// benchCPUSlices is benchCPU that also returns the speed of every slice of
// the run (slice 0 = none).
func benchCPUSlices(n int, d, slice time.Duration, step string) (float64, []float64) {
	var total, liveN atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	end := start.Add(d)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			w := newBenchWork(seed)
			var c int64
			for first := true; first || (time.Now().Before(end) && !bench.stop.Load()); first = false {
				for j := 0; j < 4; j++ {
					w.unit()
				}
				c += 4
				liveN.Add(4)
			}
			total.Add(c)
		}(uint64(i + 1))
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	var slices []float64
	var lastN int64
	lastAt := start
	for {
		select {
		case <-done:
			el := time.Since(start).Seconds()
			if el <= 0 {
				return 0, slices
			}
			return float64(total.Load()) / el, slices
		case <-tick.C:
			benchProgress(step, int(min(99, time.Since(start)*100/d)))
			if slice > 0 && time.Since(lastAt) >= slice {
				// the work is counted when each goroutine ends, so sample
				// the live counter instead
				cur := liveN.Load()
				slices = append(slices, float64(cur-lastN)/time.Since(lastAt).Seconds())
				lastN, lastAt = cur, time.Now()
			}
		}
	}
}

// ---------------------------------------------------------------- memory

// benchRAMBW copies between two large buffers (bigger than any CPU cache)
// and returns GB/s copied, using every core (like real programs do).
func benchRAMBW(d time.Duration, mb int) float64 {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8 // more threads than this only fight over the memory bus
	}
	per := (mb << 20) / n
	var bytes atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	end := start.Add(d)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, b := make([]byte, per), make([]byte, per)
			for k := range a {
				a[k] = byte(k)
			}
			var c int64
			for first := true; first || (time.Now().Before(end) && !bench.stop.Load()); first = false { // at least one copy
				copy(b, a)
				a, b = b, a
				c += int64(per)
			}
			bytes.Add(c)
		}()
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	if el <= 0 {
		return 0
	}
	return float64(bytes.Load()) / el / 1e9
}

// benchRAMLat follows a random chain through a large buffer: each step
// waits for the memory, so the time per step is the memory's latency.
func benchRAMLat(mb int, steps int) float64 {
	n := (mb << 20) / int(unsafe.Sizeof(uint32(0)))
	next := make([]uint32, n)
	for i := range next {
		next[i] = uint32(i)
	}
	// Sattolo's shuffle: one single cycle through every element
	r := rand.New(rand.NewPCG(7, 11))
	for i := n - 1; i > 0; i-- {
		j := r.IntN(i)
		next[i], next[j] = next[j], next[i]
	}
	p := uint32(0)
	for i := 0; i < 1<<16; i++ { // warm up the page tables
		p = next[p]
	}
	start := time.Now()
	for i := 0; i < steps && !bench.stop.Load(); i++ {
		p = next[p]
	}
	el := time.Since(start)
	benchSink.Add(uint64(p))
	return float64(el.Nanoseconds()) / float64(steps)
}

// ---------------------------------------------------------------- drive

// benchIO is the test file; on Windows it bypasses the cache so the drive
// itself is measured, not the memory.
type benchIO interface {
	WriteAt(p []byte, off int64) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	Close() error
}

// alignedBuf returns n bytes starting on a 4096-byte boundary (needed to
// read and write past the cache).
func alignedBuf(n int) []byte {
	b := make([]byte, n+4096)
	off := 0
	if r := int(uintptr(unsafe.Pointer(&b[0])) & 4095); r != 0 {
		off = 4096 - r
	}
	return b[off : off+n : off+n]
}

// diskKind says what a drive's speed is like.
func diskKind(seqRead, rnd float64) string {
	switch {
	case seqRead >= 5500:
		return "nvme4"
	case seqRead >= 1500:
		return "nvme3"
	case seqRead >= 350 || rnd >= 15:
		return "sata"
	case seqRead >= 80:
		return "hdd"
	}
	return "slow"
}

func benchDrive(io benchIO, size int64, phase time.Duration) (benchDisk, error) {
	var d benchDisk
	d.SizeMB = int(size >> 20)
	const chunk = 1 << 20
	buf := alignedBuf(chunk)
	r := rand.New(rand.NewPCG(3, 5)) // not zeros: some drives compress
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	// sequential write
	start := time.Now()
	var off int64
	for off = 0; off < size; off += chunk {
		if bench.stop.Load() {
			return d, errBenchStopped
		}
		if _, err := io.WriteAt(buf, off); err != nil {
			return d, err
		}
		benchProgress("disk-w", int(off*100/size))
	}
	d.SeqWrit = float64(size) / 1e6 / time.Since(start).Seconds()
	// sequential read
	start = time.Now()
	for off = 0; off < size; off += chunk {
		if bench.stop.Load() {
			return d, errBenchStopped
		}
		if _, err := io.ReadAt(buf, off); err != nil {
			return d, err
		}
		benchProgress("disk-r", int(off*100/size))
	}
	d.SeqRead = float64(size) / 1e6 / time.Since(start).Seconds()
	// 4K random, one at a time (how programs and Windows mostly use a drive)
	blocks := size / 4096
	small := buf[:4096]
	rnd := func(write bool, step string) (float64, error) {
		start := time.Now()
		n := 0
		for time.Since(start) < phase {
			if bench.stop.Load() {
				return 0, errBenchStopped
			}
			o := r.Int64N(blocks) * 4096
			var err error
			if write {
				_, err = io.WriteAt(small, o)
			} else {
				_, err = io.ReadAt(small, o)
			}
			if err != nil {
				return 0, err
			}
			n++
			if n%64 == 0 {
				benchProgress(step, int(min(99, time.Since(start)*100/phase)))
			}
		}
		return float64(n) / time.Since(start).Seconds(), nil
	}
	var err error
	if d.IOPSR, err = rnd(false, "disk-4kr"); err != nil {
		return d, err
	}
	if d.IOPSW, err = rnd(true, "disk-4kw"); err != nil {
		return d, err
	}
	d.RndRead = d.IOPSR * 4096 / 1e6
	d.RndWrit = d.IOPSW * 4096 / 1e6
	d.Kind = diskKind(d.SeqRead, d.RndRead)
	return d, nil
}

var errBenchStopped = errors.New("stopped")

// ---------------------------------------------------------------- points

func pts(v, ref float64) int {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return int(math.Round(v / ref * 1000))
}

// geo is the geometric mean of the parts that ran (so one very fast part
// cannot hide a slow one).
func geo(xs ...int) int {
	s, n := 0.0, 0
	for _, x := range xs {
		if x > 0 {
			s += math.Log(float64(x))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return int(math.Round(math.Exp(s / float64(n))))
}

func (r *benchResult) score() {
	r.SCPU1, r.SCPUN = pts(r.CPU1, refCPU1), pts(r.CPUN, refCPUN)
	if r.RAMBW > 0 && r.RAMLat > 0 {
		r.SRAM = geo(pts(r.RAMBW, refRAMBW), pts(refRAMLat, r.RAMLat))
	}
	if d := r.Disk; d != nil && d.Error == "" && d.SeqRead > 0 {
		r.SDisk = geo(pts(d.SeqRead, refSeqR), pts(d.SeqWrit, refSeqW), pts(d.RndRead, refRndR), pts(d.RndWrit, refRndW))
	}
	if g := r.GPU; g != nil && g.Error == "" {
		r.SGPU = pts(g.GFLOPS, refGFLOPS)
	}
	r.Score = geo(r.SCPU1, r.SCPUN, r.SRAM, r.SDisk, r.SGPU)
}

// runBench runs the chosen parts. open makes the drive's test file.
func runBench(o benchOpts, open func(path string, size int64) (benchIO, error), dir string, free int64) benchResult {
	res := benchResult{At: time.Now(), Threads: runtime.NumCPU()}
	if !bench.busy.CompareAndSwap(false, true) {
		res.Error = errBenchBusy.Error()
		return res
	}
	defer bench.busy.Store(false)
	bench.stop.Store(false)
	t0 := time.Now()
	cpuD, ramMB, latSteps, diskPhase := 3*time.Second, 512, 4_000_000, 3*time.Second
	if o.Quick {
		cpuD, ramMB, latSteps, diskPhase = 300*time.Millisecond, 32, 200_000, 300*time.Millisecond
	}
	if o.CPU {
		res.CPU1 = benchCPU(1, cpuD, "cpu1")
		if !bench.stop.Load() {
			if o.Long {
				d, sl := 30*time.Second, 5*time.Second
				if o.Quick {
					d, sl = 900*time.Millisecond, 200*time.Millisecond
				}
				res.CPUN, res.CPUSustain = benchCPUSlices(runtime.NumCPU(), d, sl, "cpuN")
				res.Long = true
			} else {
				res.CPUN = benchCPU(runtime.NumCPU(), cpuD, "cpuN")
			}
		}
	}
	if o.RAM && !bench.stop.Load() {
		benchProgress("ram", 10)
		res.RAMBW = benchRAMBW(cpuD/2, ramMB)
		benchProgress("ram", 60)
		res.RAMLat = benchRAMLat(ramMB/2, latSteps)
		benchProgress("ram", 100)
	}
	if o.Disk && !bench.stop.Load() {
		d := &benchDisk{Drive: o.Drive}
		size := int64(1 << 30)
		if o.Quick {
			size = 16 << 20
		}
		switch {
		case free > 0 && free < size+(2<<30) && !o.Quick:
			size = 256 << 20
			if free < size+(1<<30) {
				d.Error = "nospace"
			}
		}
		if d.Error == "" {
			path := filepath.Join(dir, "xKi4o-benchmark.tmp")
			f, err := open(path, size)
			if err != nil {
				d.Error = err.Error()
			} else {
				m, err := benchDrive(f, size, diskPhase)
				f.Close()
				_ = os.Remove(path) // already gone on Windows (delete on close)
				m.Drive = o.Drive
				*d = m
				if err != nil && !errors.Is(err, errBenchStopped) {
					d.Error = err.Error()
				}
			}
		}
		res.Disk = d
	}
	res.Stopped = bench.stop.Load()
	res.TookSecs = time.Since(t0).Seconds()
	res.score()
	return res
}

// ---------------------------------------------------------------- history

const benchKeep = 30

func benchHistoryPath(base string) string {
	return filepath.Join(base, "xKi4oCleaner", "benchmarks.json")
}

func readBenchHistory(path string) []benchResult {
	var h []benchResult
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &h)
	}
	return h
}

// addBenchHistory keeps the newest results first, at most benchKeep.
func addBenchHistory(path string, r benchResult) []benchResult {
	h := append([]benchResult{r}, readBenchHistory(path)...)
	if len(h) > benchKeep {
		h = h[:benchKeep]
	}
	if b, err := json.MarshalIndent(h, "", " "); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, b, 0o644)
	}
	return h
}
