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

// benchBalance is the bottleneck verdict.
type benchBalance struct {
	Verdict  string `json:"verdict"` // balanced, cpu, gpu, unknown
	GPUClass string `json:"gpuClass"`
	GPUIndex int    `json:"gpuIndex"` // ~ % of an RTX 3060 in games
	CPUNeed  int    `json:"cpuNeed"`  // one-core points this card likes in games
	CPUHave  int    `json:"cpuHave"`
	Pos      int    `json:"pos"` // 0-100 for the balance bar: 50 = even, < 50 CPU limits, > 50 GPU limits
}

// ---------------------------------------------------------------- graphics card classes

// gpuIndex: rough game performance (1440p) relative to an RTX 3060 = 100.
// Ordered longest name first where names overlap ("4070 ti super" before
// "4070 ti" before "4070").
var gpuTable = []struct {
	re  *regexp.Regexp
	idx int
}{}

var gpuList = []struct {
	pat string
	idx int
}{
	{`rtx\s*5090`, 420}, {`rtx\s*5080`, 290}, {`rtx\s*5070\s*ti`, 240}, {`rtx\s*5070`, 190}, {`rtx\s*5060\s*ti`, 135}, {`rtx\s*5060`, 115}, {`rtx\s*5050`, 90},
	{`rtx\s*4090`, 330}, {`rtx\s*4080\s*super`, 265}, {`rtx\s*4080`, 255}, {`rtx\s*4070\s*ti\s*super`, 225}, {`rtx\s*4070\s*ti`, 210},
	{`rtx\s*4070\s*super`, 190}, {`rtx\s*4070`, 165}, {`rtx\s*4060\s*ti`, 125}, {`rtx\s*4060`, 105},
	{`rtx\s*3090\s*ti`, 225}, {`rtx\s*3090`, 205}, {`rtx\s*3080\s*ti`, 200}, {`rtx\s*3080`, 185}, {`rtx\s*3070\s*ti`, 155}, {`rtx\s*3070`, 145},
	{`rtx\s*3060\s*ti`, 125}, {`rtx\s*3060`, 100}, {`rtx\s*3050`, 70},
	{`rtx\s*2080\s*ti`, 150}, {`rtx\s*2080\s*super`, 130}, {`rtx\s*2080`, 122}, {`rtx\s*2070\s*super`, 115}, {`rtx\s*2070`, 103}, {`rtx\s*2060\s*super`, 95}, {`rtx\s*2060`, 83},
	{`gtx\s*1660\s*(ti|super)`, 70}, {`gtx\s*1660`, 62}, {`gtx\s*1650\s*super`, 52}, {`gtx\s*1650`, 42},
	{`gtx\s*1080\s*ti`, 108}, {`gtx\s*1080`, 85}, {`gtx\s*1070\s*ti`, 78}, {`gtx\s*1070`, 72}, {`gtx\s*1060`, 52}, {`gtx\s*1050\s*ti`, 32}, {`gtx\s*1050`, 26},
	{`rx\s*9070\s*xt`, 240}, {`rx\s*9070`, 215}, {`rx\s*9060\s*xt`, 140},
	{`rx\s*7900\s*xtx`, 270}, {`rx\s*7900\s*xt`, 235}, {`rx\s*7900\s*gre`, 195}, {`rx\s*7800\s*xt`, 180}, {`rx\s*7700\s*xt`, 150}, {`rx\s*7600\s*xt`, 110}, {`rx\s*7600`, 102},
	{`rx\s*6950\s*xt`, 205}, {`rx\s*6900\s*xt`, 195}, {`rx\s*6800\s*xt`, 182}, {`rx\s*6800`, 158}, {`rx\s*6750\s*xt`, 135}, {`rx\s*6700\s*xt`, 128}, {`rx\s*6700`, 115},
	{`rx\s*6650\s*xt`, 108}, {`rx\s*6600\s*xt`, 103}, {`rx\s*6600`, 90}, {`rx\s*6500\s*xt`, 50}, {`rx\s*5700\s*xt`, 98}, {`rx\s*5700`, 88}, {`rx\s*5600\s*xt`, 78}, {`rx\s*5500\s*xt`, 52},
	{`rx\s*590`, 47}, {`rx\s*580`, 44}, {`rx\s*570`, 38},
	{`arc.*b580`, 118}, {`arc.*b570`, 103}, {`arc.*a770`, 108}, {`arc.*a750`, 98}, {`arc.*a580`, 88}, {`arc.*a380`, 38},
	{`radeon.*890m`, 42}, {`radeon.*880m`, 38}, {`radeon.*780m`, 34}, {`radeon.*760m`, 28}, {`radeon.*680m`, 28},
	{`arc.*140v|arc.*130v`, 36}, {`intel.*arc(\(tm\))?\s*graphics`, 30}, {`iris.*xe`, 18}, {`radeon.*vega`, 14}, {`uhd\s*graphics|hd\s*graphics`, 8}, {`radeon(\(tm\))?\s*graphics`, 16},
}

func init() {
	for _, g := range gpuList {
		gpuTable = append(gpuTable, struct {
			re  *regexp.Regexp
			idx int
		}{regexp.MustCompile(`(?i)` + g.pat), g.idx})
	}
}

var reLaptopGPU = regexp.MustCompile(`(?i)laptop|mobile|max-q`)

func gpuIndexOf(name string) int {
	for _, g := range gpuTable {
		if g.re.MatchString(name) {
			// a laptop chip of the same name is clearly slower
			if reLaptopGPU.MatchString(name) && g.idx > 60 {
				return g.idx * 7 / 10
			}
			return g.idx
		}
	}
	return 0
}

func gpuClassOf(idx int) string {
	switch {
	case idx >= 250:
		return "enthusiast"
	case idx >= 160:
		return "high"
	case idx >= 95:
		return "upper"
	case idx >= 60:
		return "mid"
	case idx >= 30:
		return "entry"
	case idx > 0:
		return "igpu"
	}
	return ""
}

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
			if !isIntegrated(n) && gpuIndexOf(n) > 40 {
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
			add("pc", "warn", "bn.n.balCPU", b.GPUClass, itoa(b.CPUHave), itoa(b.CPUNeed))
		case "gpu":
			add("pc", "info", "bn.n.balGPU")
		case "balanced":
			add("pc", "good", "bn.n.balOK")
		}
	}
	// the biggest upgrade: a part far below the others
	parts := map[string]int{"cpu": r.SCPU1, "ram": r.SRAM, "disk": r.SDisk, "gpu": r.SGPU}
	var vals []int
	for _, v := range parts {
		if v > 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) >= 3 {
		sort.Ints(vals)
		med := vals[len(vals)/2]
		weak, wv := "", 1<<30
		for k, v := range parts {
			if v > 0 && v < wv {
				weak, wv = k, v
			}
		}
		if wv < med/2 {
			add("pc", "info", "bn.n.upgrade."+weak)
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

// bottleneck compares how fast one core is (games lean on one or two
// threads) with how much processor speed the graphics card asks for to stay
// busy at high frame rates. At 4K the card is the limit almost always.
func bottleneck(r *benchResult, c benchCtx) *benchBalance {
	if r.SCPU1 <= 0 {
		return nil
	}
	name := ""
	if r.GPU != nil {
		name = r.GPU.Name
		if name == "" || gpuIndexOf(name) == 0 {
			if gpuIndexOf(r.GPU.Renderer) > 0 {
				name = r.GPU.Renderer
			}
		}
	}
	if gpuIndexOf(name) == 0 {
		for _, n := range c.GPUs {
			if gpuIndexOf(n) > gpuIndexOf(name) {
				name = n
			}
		}
	}
	idx := gpuIndexOf(name)
	if idx == 0 {
		return &benchBalance{Verdict: "unknown", CPUHave: r.SCPU1}
	}
	need := int(math.Round(700 + 2.6*float64(idx)))
	b := &benchBalance{GPUIndex: idx, GPUClass: gpuClassOf(idx), CPUNeed: need, CPUHave: r.SCPU1}
	ratio := float64(r.SCPU1) / float64(need)
	switch {
	case ratio < 0.82:
		b.Verdict = "cpu"
	case ratio > 1.6 && idx < 120:
		b.Verdict = "gpu"
	default:
		b.Verdict = "balanced"
	}
	// the bar: log scale around even
	pos := 50 + math.Log2(ratio)*35
	b.Pos = int(math.Max(4, math.Min(96, pos)))
	return b
}
