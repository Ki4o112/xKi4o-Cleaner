//go:build windows

package main

import (
	"errors"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// benchFile reads and writes past Windows' cache (FILE_FLAG_NO_BUFFERING,
// write-through) so the drive itself is measured. It is "delete on close":
// the file disappears when it is closed, even if the app is closed first.
type benchFile struct{ h windows.Handle }

func openBenchFileWin(path string, size int64) (benchIO, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return nil, err
	}
	return &benchFile{h: h}, nil
}

func (f *benchFile) at(p []byte, off int64, write bool) (int, error) {
	ov := windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(off >> 32)}
	var n uint32
	var err error
	if write {
		err = windows.WriteFile(f.h, p, &n, &ov)
	} else {
		err = windows.ReadFile(f.h, p, &n, &ov)
	}
	if err == nil && int(n) != len(p) {
		err = errors.New("short transfer")
	}
	return int(n), err
}

func (f *benchFile) WriteAt(p []byte, off int64) (int, error) { return f.at(p, off, true) }
func (f *benchFile) ReadAt(p []byte, off int64) (int, error)  { return f.at(p, off, false) }
func (f *benchFile) Close() error                             { return windows.CloseHandle(f.h) }

func init() {
	benchPriority = func() func() {
		h := windows.CurrentProcess()
		old, err := windows.GetPriorityClass(h)
		if err != nil {
			return func() {}
		}
		_ = windows.SetPriorityClass(h, windows.ABOVE_NORMAL_PRIORITY_CLASS)
		return func() { _ = windows.SetPriorityClass(h, old) }
	}
}

func benchHistPath() string { return benchHistoryPath(os.Getenv("LOCALAPPDATA")) }

// benchContext reads what the PC is, for the analysis (cached reads).
func benchContext(drive string) *benchCtx {
	c := &benchCtx{}
	si := systemInfo(false)
	for _, p := range si.CPU {
		c.CPU, c.Cores, c.Threads = p.Name, c.Cores+p.Cores, c.Threads+p.Threads
	}
	c.Laptop = si.Board.Laptop
	for _, m := range si.RAM {
		c.RAM = append(c.RAM, benchRAM{Size: m.Size, Rated: m.MHz, Cfg: m.Cfg, Type: m.Type, Part: m.Part})
	}
	for _, g := range si.GPU {
		if !g.Virtual {
			c.GPUs = append(c.GPUs, g.Name)
		}
	}
	letter := strings.TrimSuffix(drive, ":")
	for _, d := range diskHealthList(false).Disks {
		for _, l := range d.Letters {
			if strings.EqualFold(strings.TrimSuffix(l, ":"), letter) {
				c.DiskBus, c.DiskKind = d.Bus, d.Media
			}
		}
	}
	if t, f := diskSpace(drive + `\`); t > 0 {
		c.DiskFree = float64(f) / float64(t) * 100
	}
	return c
}

// otherLoad: how busy the processor is with other programs right now (%).
func otherLoad() float64 {
	var m cpuMeter
	m.read()
	time.Sleep(700 * time.Millisecond)
	return m.read()
}

// benchRun runs the processor, memory and drive parts. When the page also
// runs the graphics test (o.GPU), the result is not saved yet: the page
// adds the graphics part and calls benchSave.
func benchRun(o benchOpts) map[string]any {
	drive := strings.ToUpper(strings.TrimSpace(o.Drive))
	if len(drive) < 1 || drive[0] < 'A' || drive[0] > 'Z' {
		drive = systemDrive()
	}
	drive = drive[:1] + ":"
	o.Drive = drive
	root := drive + `\`
	if o.Disk {
		rp, _ := windows.UTF16PtrFromString(root)
		if t := windows.GetDriveType(rp); t != windows.DRIVE_FIXED && t != windows.DRIVE_REMOVABLE {
			return map[string]any{"error": T("bench.err.drive", drive)}
		}
	}
	_, free := diskSpace(root)
	dir := root
	if strings.EqualFold(drive, systemDrive()) {
		dir = os.TempDir() // on the system drive: in Temp, not in the drive's root
		if !strings.EqualFold(dir[:2], drive) {
			dir = root
		}
	}
	load := 0.0
	if o.CPU {
		load = otherLoad()
	}
	benchProgress = func(step string, pct int) { emit("bench-progress", map[string]any{"step": step, "pct": pct}) }
	op := beginOp("bench", drive, T("log.bench"), map[string]any{"cpu": o.CPU, "ram": o.RAM, "disk": o.Disk, "gpu": o.GPU, "long": o.Long})
	r := runBench(o, openBenchFileWin, dir, int64(free))
	if r.Error == errBenchBusy.Error() {
		op.end("", nil)
		return map[string]any{"error": T("bench.err.busy")}
	}
	r.Load0 = load
	r.Ctx = benchContext(drive)
	if r.Disk != nil && r.Disk.Error != "" {
		if r.Disk.Error == "nospace" {
			r.Disk.Error = T("bench.err.space", drive)
		}
		op.fail(drive, r.Disk.Error, nil)
	}
	op.end(T("log.benchDone", r.Score), map[string]any{"score": r.Score, "cpu1": r.SCPU1, "cpuN": r.SCPUN, "ram": r.SRAM, "disk": r.SDisk})
	if o.GPU && !r.Stopped {
		return map[string]any{"result": r, "pending": true}
	}
	return benchSave(r)
}

// benchSave adds the graphics part (when there is one), scores everything,
// writes the analysis and keeps it in the history.
func benchSave(r benchResult) map[string]any {
	if r.Ctx == nil {
		r.Ctx = benchContext(systemDrive())
	}
	if g := r.GPU; g != nil {
		g.Limits = uniqStrings(g.Limits)
		if g.GFLOPS < 0 || g.GFLOPS > 1e7 {
			g.GFLOPS = 0
		}
	}
	r.score()
	analyzeBench(&r, *r.Ctx)
	if r.GPU != nil {
		logEvent("bench", "info", "gpu", T("log.benchGPU", r.SGPU, r.GPU.Renderer), map[string]any{"gflops": r.GPU.GFLOPS, "util": r.GPU.UtilMax})
	}
	var h []benchResult
	if !r.Stopped && r.Score > 0 {
		h = addBenchHistory(benchHistPath(), r)
	} else {
		h = readBenchHistory(benchHistPath())
	}
	return map[string]any{"result": r, "history": h}
}

func uniqStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// benchGPUSample: the graphics card's sensors right now (read during the
// graphics test). Only NVIDIA cards give these through their driver.
func benchGPUSample() gpuSensors { return gpuSensorsRead() }

func benchStop() bool { bench.stop.Store(true); return true }

func benchHistory() []benchResult {
	h := readBenchHistory(benchHistPath())
	if h == nil {
		h = []benchResult{}
	}
	return h
}

func benchClearHistory() bool {
	err := os.Remove(benchHistPath())
	return err == nil || errors.Is(err, os.ErrNotExist)
}

// openGraphicsSettings opens Windows' Graphics settings (where a program can
// be set to use the high-performance graphics card). Nothing is changed here.
func openGraphicsSettings() bool {
	shellOpen("ms-settings:display-advancedgraphics")
	return true
}
