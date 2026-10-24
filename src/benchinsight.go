package main

// Benchmark analysis: after a test, say in plain words whether each part
// performed as it should, and why not when it did not. The checks rest on
// what was actually measured on this PC (scaling over the cores, speed that
// falls during a long run, memory slower than its rating, a drive slower
// than its kind, the graphics card's own sensors under load) — not on a
// guess from a table. The one table here (graphics card classes) is only
// used to say how strong the card is and whether the processor can keep up
// with it in games (the "bottleneck").

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// benchGPU is the graphics test, run in the page with WebGL 2 (the card
// renders a heavy shader off screen; nothing is shown or changed), plus the
// card's sensors read while it ran.
type benchGPU struct {
	Name     string    `json:"name"`     // from Windows / the driver
	Renderer string    `json:"renderer"` // what WebGL ran on
	GFLOPS   float64   `json:"gflops"`   // shader work per second (estimated from the shader's cost)
	FPS      float64   `json:"fps"`      // the scene at 2560x1440
	Sustain  []float64 `json:"sustain,omitempty"`
	UtilMax  int       `json:"utilMax"` // %, -1 unknown
	ClockMax int       `json:"clockMax"`
	BoostMax int       `json:"boostMax"`
	TempMax  int       `json:"tempMax"`
	PowerMax float64   `json:"powerMax"`
	PowerLim float64   `json:"powerLimit"`
	Limits   []string  `json:"limits,omitempty"`
	PCIeGen  int       `json:"pcieGen"`
	PCIeW    int       `json:"pcieWidth"`
	PCIeMaxG int       `json:"pcieMaxGen"`
	PCIeMaxW int       `json:"pcieMaxWidth"`
	Sensors  bool      `json:"sensors"` // the card's own sensors were read (NVIDIA)
	Error    string    `json:"error,omitempty"`
}

// benchCtx is what the PC is (from System information), for the analysis.
type benchCtx struct {
	CPU      string     `json:"cpu"`
	Cores    int        `json:"cores"`
	Threads  int        `json:"threads"`
	Laptop   bool       `json:"laptop"`
	RAM      []benchRAM `json:"ram"`
	GPUs     []string   `json:"gpus"`
	DiskBus  string     `json:"diskBus"`  // NVMe, SATA, USB
	DiskKind string     `json:"diskKind"` // SSD, HDD
	DiskFree float64    `json:"diskFree"` // % free on the tested drive
	ResW     int        `json:"resW"`     // the main screen
	ResH     int        `json:"resH"`
}

type benchRAM struct {
	Size  int64  `json:"size"` // MB
	Rated int    `json:"rated"`
	Cfg   int    `json:"cfg"`
	Type  string `json:"type"`
	Part  string `json:"part"`
}

// benchNote is one line of the analysis: a tone, a text key and its values.
type benchNote struct {
	Part string   `json:"part"` // cpu, ram, disk, gpu, pc
	Tone string   `json:"tone"` // good, info, warn, bad
	Key  string   `json:"key"`
	Args []string `json:"args,omitempty"`
}

// benchBalance is the bottleneck verdict, from published test data (see
// hwdata.go): how many frames per second the processor can prepare in games
// and how many the graphics card can draw, at each resolution.
type benchBalance struct {
	Verdict  string        `json:"verdict"`           // balanced, cpu, gpu, unknown
	Missing  string        `json:"missing,omitempty"` // unknown: cpu or gpu (no test data for that model)
	CPUName  string        `json:"cpuName"`
	GPUName  string        `json:"gpuName"`
	Res      string        `json:"res"` // the resolution of the screen: 1080p, 1440p, 4K
	CPUFps   float64       `json:"cpuFps"`
	GPUFps   float64       `json:"gpuFps"`
	Pct      int           `json:"pct"` // how much the slower part holds the other back (%)
	Rows     []benchBalRow `json:"rows,omitempty"`
	GPUClass string        `json:"gpuClass"`
	GPUIndex int           `json:"gpuIndex"` // % of an RTX 3060 at 1080p
	Pos      int           `json:"pos"`      // 0-100 for the balance bar: 50 = even, < 50 the processor limits, > 50 the card limits
	Source   string        `json:"source"`
	// how this processor and card compare with the others in the published
	// tests: faster than CPUPct% of CPUOf processors (0 of 0: not in the tests)
	CPUPct int `json:"cpuPct"`
	CPUOf  int `json:"cpuOf"`
	GPUPct int `json:"gpuPct"`
	GPUOf  int `json:"gpuOf"`
}

type benchBalRow struct {
	Res     string  `json:"res"`
	CPUFps  float64 `json:"cpuFps"`
	GPUFps  float64 `json:"gpuFps"`
	Verdict string  `json:"verdict"`
	Pct     int     `json:"pct"`
}

// ---------------------------------------------------------------- graphics card classes

// isIntegrated: names of graphics built into the processor.
func isIntegrated(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "uhd") || strings.Contains(n, "iris") || strings.Contains(n, "hd graphics") ||
		regexp.MustCompile(`radeon(\(tm\))?\s*(\d{3}m|graphics|vega)`).MatchString(n) ||
		regexp.MustCompile(`intel.*arc(\(tm\))?\s*(graphics|1[34]0v)`).MatchString(n) || strings.Contains(n, "microsoft basic") || strings.Contains(n, "swiftshader")
}

// ---------------------------------------------------------------- the analysis

func f0(v float64) string { return fmt.Sprintf("%.0f", v) }
func itoa(v int) string   { return fmt.Sprint(v) }

// sustainDrop: how much the speed fell from the start to the end of a long
// run, in % (first third vs last third, so one slow moment does not count).
func sustainDrop(xs []float64) float64 {
	if len(xs) < 3 {
		return 0
	}
	n := len(xs) / 3
	avg := func(s []float64) float64 {
		t := 0.0
		for _, x := range s {
			t += x
		}
		return t / float64(len(s))
	}
	a, b := avg(xs[:n]), avg(xs[len(xs)-n:])
	if a <= 0 {
		return 0
	}
	return math.Max(0, (a-b)/a*100)
}

// expectedScaling: how many times one core the processor should manage on
// all cores (a second thread on a core adds about a quarter; on hybrid
// chips the efficiency cores are slower, which the factor allows for).
func expectedScaling(cores, threads int) float64 {
	if cores <= 0 {
		cores = threads
	}
	smt := threads - cores
	if smt < 0 {
		smt = 0
	}
	return float64(cores)*0.82 + float64(smt)*0.25
}

func analyzeBench(r *benchResult, c benchCtx) {
	var notes []benchNote
	add := func(part, tone, key string, args ...string) {
		notes = append(notes, benchNote{Part: part, Tone: tone, Key: key, Args: args})
	}
	// processor
	if r.CPU1 > 0 && r.CPUN > 0 {
		got := r.CPUN / r.CPU1
		want := expectedScaling(c.Cores, r.Threads)
		if c.Threads > 0 {
			want = expectedScaling(c.Cores, c.Threads)
		}
		if got >= want*0.6 {
			add("cpu", "good", "bn.n.cpuScale", fmt.Sprintf("%.1f", got), itoa(max(c.Threads, r.Threads)))
		} else {
			add("cpu", "warn", "bn.n.cpuScaleLow", fmt.Sprintf("%.1f", got), fmt.Sprintf("%.0f", want))
		}
		if r.Load0 >= 20 {
			add("cpu", "warn", "bn.n.cpuBusy", f0(r.Load0))
		}
		if len(r.CPUSustain) >= 3 {
			if d := sustainDrop(r.CPUSustain); d >= 10 {
				tone := "warn"
				if d >= 25 {
					tone = "bad"
				}
				add("cpu", tone, "bn.n.cpuThrottle", f0(d))
			} else {
				add("cpu", "good", "bn.n.cpuSteady", itoa(len(r.CPUSustain)*5))
			}
		}
	}
	// memory
	if r.RAMBW > 0 && len(c.RAM) > 0 {
		slow := false
		for _, m := range c.RAM {
			if m.Rated > 0 && m.Cfg > 0 && float64(m.Cfg) < float64(m.Rated)*0.92 {
				add("ram", "warn", "bn.n.ramXMP", itoa(m.Cfg), itoa(m.Rated), xmpName(c.CPU))
				slow = true
				break
			}
		}
		if len(c.RAM) == 1 {
			add("ram", "warn", "bn.n.ramSingle", fmt.Sprintf("%.0f", float64(c.RAM[0].Size)/1024))
			slow = true
		} else {
			mixed := false
			for _, m := range c.RAM[1:] {
				if m.Size != c.RAM[0].Size || (m.Part != "" && c.RAM[0].Part != "" && m.Part != c.RAM[0].Part) {
					mixed = true
				}
			}
			if mixed {
				add("ram", "info", "bn.n.ramMixed")
			}
		}
		if r.RAMLat >= 120 {
			add("ram", "info", "bn.n.ramLat", f0(r.RAMLat))
		}
		if !slow {
			add("ram", "good", "bn.n.ramOK", fmt.Sprintf("%.1f", r.RAMBW), c.RAM[0].Type, itoa(c.RAM[0].Cfg))
		}
	}
	// drive
	if d := r.Disk; d != nil && d.Error == "" && d.SeqRead > 0 {
		bus, media := strings.ToLower(c.DiskBus), strings.ToLower(c.DiskKind)
		switch {
		case bus == "nvme" && (d.Kind == "sata" || d.Kind == "hdd" || d.Kind == "slow"):
			add("disk", "warn", "bn.n.nvmeSlow", d.Drive, f0(d.SeqRead))
		case media == "ssd" && (d.Kind == "hdd" || d.Kind == "slow"):
			add("disk", "bad", "bn.n.ssdSlow", d.Drive, f0(d.SeqRead))
		case media == "hdd" || d.Kind == "hdd":
			add("disk", "info", "bn.n.hdd", d.Drive)
		default:
			add("disk", "good", "bn.n.diskOK", d.Drive, f0(d.SeqRead), f0(d.SeqWrit))
		}
		if c.DiskFree > 0 && c.DiskFree < 10 && media != "hdd" {
			add("disk", "warn", "bn.n.diskFull", d.Drive, f0(c.DiskFree))
		}
	}
	// graphics card
	if g := r.GPU; g != nil && g.Error == "" && g.GFLOPS > 0 {
		name := g.Name
		if name == "" {
			name = g.Renderer
		}
		dedicated := ""
		for _, n := range c.GPUs {
			if !isIntegrated(n) && gpuDataFor(n) != nil {
				dedicated = n
			}
		}
		if isIntegrated(g.Renderer) && dedicated != "" {
			add("gpu", "warn", "bn.n.gpuIGP", dedicated)
		}
		if g.Sensors {
			ok := true
			if g.UtilMax >= 0 && g.UtilMax < 80 {
				add("gpu", "info", "bn.n.gpuLoad", itoa(g.UtilMax))
				ok = false
			}
			hasLimit := func(k string) bool {
				for _, l := range g.Limits {
					if l == k {
						return true
					}
				}
				return false
			}
			if hasLimit("thermal") || hasLimit("hw") {
				add("gpu", "bad", "bn.n.gpuHot", itoa(g.TempMax))
				ok = false
			} else if g.TempMax >= 86 {
				add("gpu", "warn", "bn.n.gpuWarm", itoa(g.TempMax))
				ok = false
			}
			if g.UtilMax >= 80 && g.BoostMax > 0 && g.ClockMax > 0 && float64(g.ClockMax) < float64(g.BoostMax)*0.75 && !hasLimit("power") {
				add("gpu", "warn", "bn.n.gpuClock", itoa(g.ClockMax), itoa(g.BoostMax))
				ok = false
			}
			if hasLimit("power") {
				add("gpu", "info", "bn.n.gpuPower", f0(g.PowerMax), f0(g.PowerLim))
			}
			if g.PCIeMaxG > 0 && g.PCIeMaxW > 0 && (g.PCIeGen < g.PCIeMaxG || g.PCIeW < g.PCIeMaxW) && g.UtilMax >= 50 {
				add("gpu", "warn", "bn.n.gpuPCIe", itoa(g.PCIeGen), itoa(g.PCIeW), itoa(g.PCIeMaxG), itoa(g.PCIeMaxW))
				ok = false
			}
			if len(g.Sustain) >= 3 {
				if d := sustainDrop(g.Sustain); d >= 10 {
					add("gpu", "warn", "bn.n.gpuThrottle", f0(d))
					ok = false
				}
			}
			if ok {
				add("gpu", "good", "bn.n.gpuOK", name, itoa(g.UtilMax), itoa(g.ClockMax), itoa(g.TempMax))
			}
		} else {
			add("gpu", "info", "bn.n.gpuNoSensors", name)
		}
	}
	// bottleneck
	r.Balance = bottleneck(r, c)
	if b := r.Balance; b != nil {
		switch b.Verdict {
		case "cpu":
			add("pc", "warn", "bn.n.balCPU", b.CPUName, b.GPUName, b.Res, itoa(b.Pct), f0(b.CPUFps), f0(b.GPUFps))
		case "gpu":
			add("pc", "info", "bn.n.balGPU", b.CPUName, b.GPUName, b.Res, f0(b.CPUFps), f0(b.GPUFps))
		case "balanced":
			add("pc", "good", "bn.n.balOK", b.CPUName, b.GPUName, b.Res)
		case "unknown":
			name := b.CPUName
			if b.Missing == "gpu" {
				name = b.GPUName
			}
			add("pc", "info", "bn.n.balUnknown", name)
		}
	}
	order := map[string]int{"pc": 0, "cpu": 1, "gpu": 2, "ram": 3, "disk": 4}
	tone := map[string]int{"bad": 0, "warn": 1, "info": 2, "good": 3}
	sort.SliceStable(notes, func(i, j int) bool {
		if order[notes[i].Part] != order[notes[j].Part] {
			return order[notes[i].Part] < order[notes[j].Part]
		}
		return tone[notes[i].Tone] < tone[notes[j].Tone]
	})
	r.Notes = notes
}

func xmpName(cpu string) string {
	if strings.Contains(strings.ToLower(cpu), "amd") || strings.Contains(strings.ToLower(cpu), "ryzen") {
		return "EXPO/DOCP"
	}
	return "XMP"
}

// bottleneck compares, for this processor and graphics card, how many frames
// per second each can deliver in games (published averages over many games,
// see hwdata.go) at the screen's resolution. It does not use this app's own
// points: those are for comparing a PC with itself, not for this.
func bottleneck(r *benchResult, c benchCtx) *benchBalance {
	gname := ""
	if r.GPU != nil {
		for _, n := range []string{r.GPU.Name, r.GPU.Renderer} {
			if gname == "" && gpuDataFor(n) != nil {
				gname = n
			}
		}
	}
	if gname == "" {
		for _, n := range c.GPUs {
			if gpuDataFor(n) != nil {
				gname = n
				break
			}
		}
	}
	if gname == "" && r.GPU == nil && len(c.GPUs) == 0 {
		return nil
	}
	b := &benchBalance{CPUName: cleanCPUName(c.CPU), Res: resClass(c.ResW, c.ResH), Source: hwDataSource}
	cpu := cpuDataFor(c.CPU)
	gpu := gpuDataFor(gname)
	if cpu != nil {
		b.CPUPct, b.CPUOf = cpuRank(cpu.Index)
	}
	if gpu != nil {
		b.GPUPct, b.GPUOf = gpuRank(gpu.FPS[0])
	}
	switch {
	case gpu != nil:
		b.GPUName = gpu.Name
	case r.GPU != nil && r.GPU.Name != "":
		b.GPUName = r.GPU.Name
	case len(c.GPUs) > 0:
		b.GPUName = c.GPUs[0]
	}
	switch {
	case cpu == nil:
		b.Verdict, b.Missing = "unknown", "cpu"
		return b
	case gpu == nil:
		b.Verdict, b.Missing = "unknown", "gpu"
		return b
	}
	cfps := cpu.Index / 100 * cpuFpsAt100
	for i, res := range []string{"1080p", "1440p", "4K"} {
		g := gpu.FPS[i]
		row := benchBalRow{Res: res, CPUFps: cfps, GPUFps: g}
		switch {
		case cfps < g*0.85: // the processor costs more than 15% of the card's frames
			row.Verdict, row.Pct = "cpu", int(math.Round((1-cfps/g)*100))
		case g < cfps*0.5: // the card gives less than half of what the processor could feed
			row.Verdict, row.Pct = "gpu", int(math.Round((1-g/cfps)*100))
		default:
			row.Verdict = "balanced"
		}
		b.Rows = append(b.Rows, row)
		if res == b.Res {
			b.Verdict, b.Pct, b.CPUFps, b.GPUFps = row.Verdict, row.Pct, cfps, g
		}
	}
	b.GPUIndex = int(math.Round(gpu.FPS[0] / gpuRef1080 * 100))
	b.GPUClass = gpuClassOfFps(gpu.FPS[0])
	ratio := b.CPUFps / b.GPUFps // > 1: the processor has room; < 1: it holds the card back
	b.Pos = int(math.Max(4, math.Min(96, 50+math.Log2(ratio)*45)))
	return b
}

// resClass: the screen's resolution as one of the three tested ones (an
// ultrawide 3440x1440 counts as 1440p).
func resClass(w, h int) string {
	switch {
	case h >= 2000 || w >= 3800:
		return "4K"
	case h >= 1300:
		return "1440p"
	}
	return "1080p"
}
