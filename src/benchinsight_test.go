package main

import (
	"strings"
	"testing"
)

func noteKeys(r benchResult) string {
	var ks []string
	for _, n := range r.Notes {
		ks = append(ks, n.Tone+":"+n.Key)
	}
	return strings.Join(ks, " ")
}

// The bottleneck data: every name Windows gives is found as the right model.
func TestHWDataNames(t *testing.T) {
	for name, want := range map[string]string{
		"NVIDIA GeForce GTX 1660 SUPER": "GeForce GTX 1660 Super", "NVIDIA GeForce GTX 1660 Ti": "GeForce GTX 1660 Ti", "NVIDIA GeForce GTX 1660": "GeForce GTX 1660",
		"NVIDIA GeForce RTX 4070 Ti SUPER": "GeForce RTX 4070 Ti Super", "NVIDIA GeForce RTX 4070 Ti": "GeForce RTX 4070 Ti", "NVIDIA GeForce RTX 4070": "GeForce RTX 4070",
		"NVIDIA GeForce RTX 4090": "GeForce RTX 4090", "AMD Radeon RX 5700 XT": "Radeon RX 5700 XT", "AMD Radeon RX 5700": "Radeon RX 5700",
		"Radeon RX 570 Series": "Radeon RX 570", "AMD Radeon RX 580 2048SP": "Radeon RX 580", "AMD Radeon RX 7900 XTX": "Radeon RX 7900 XTX",
		"Intel(R) Arc(TM) B580 Graphics": "Intel Arc B580", "NVIDIA GeForce GTX 1060 6GB": "GeForce GTX 1060 6GB",
	} {
		g := gpuDataFor(name)
		if g == nil || g.Name != want {
			t.Errorf("%s: %+v, want %s", name, g, want)
		}
	}
	for _, n := range []string{"NVIDIA GeForce RTX 4060 Laptop GPU", "Intel(R) UHD Graphics 770", "AMD Radeon(TM) Graphics", "Mystery 3D"} {
		if gpuDataFor(n) != nil {
			t.Errorf("%s should have no data", n)
		}
	}
	for name, want := range map[string]string{
		"AMD Ryzen 5 2600 Six-Core Processor": "Ryzen 5 2600", "AMD Ryzen 5 2600X Six-Core Processor": "Ryzen 5 2600X",
		"12th Gen Intel(R) Core(TM) i9-12900K": "Core i9-12900K", "Intel(R) Core(TM) i5-10400F CPU @ 2.90GHz": "Core i5-10400",
		"13th Gen Intel(R) Core(TM) i7-13700KF": "Core i7-13700K", "AMD Ryzen 7 7800X3D 8-Core Processor": "Ryzen 7 7800X3D",
		"AMD Ryzen 7 5800X3D 8-Core Processor": "Ryzen 7 5800X3D", "AMD Ryzen 7 5800X 8-Core Processor": "Ryzen 7 5800X",
		"Intel(R) Core(TM) Ultra 7 265K": "Core Ultra 7 265K", "Intel(R) Core(TM) Ultra 7 270K Plus": "Core Ultra 7 270K Plus",
		"Intel(R) Core(TM) i9-9900KS CPU @ 4.00GHz": "Core i9-9900KS", "Intel(R) Core(TM) i9-9900K CPU @ 3.60GHz": "Core i9-9900K",
	} {
		c := cpuDataFor(name)
		if c == nil || c.Name != want {
			t.Errorf("%s: %+v, want %s", name, c, want)
		}
	}
	for _, n := range []string{"12th Gen Intel(R) Core(TM) i7-12700H", "AMD Ryzen 7 7840HS w/ Radeon 780M Graphics", "11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz"} {
		if c := cpuDataFor(n); c != nil {
			t.Errorf("%s (a laptop chip) should have no data: %+v", n, c)
		}
	}
	if !isIntegrated("Intel(R) UHD Graphics 770") || isIntegrated("NVIDIA GeForce RTX 4090") {
		t.Fatal("integrated graphics")
	}
}

// Real pairs, checked against published tests: a Ryzen 5 2600 does not hold
// back a GTX 1660 Super (TechSpot: with an RX 580 or slower the 3600 is no
// faster than the 2600; Tom's Hardware data: the card makes ~40 fps at 1080p
// ultra, the processor can feed ~77).
func TestBottleneckRealPairs(t *testing.T) {
	cases := []struct {
		cpu, gpu string
		w, h     int
		want     string
	}{
		{"AMD Ryzen 5 2600 Six-Core Processor", "NVIDIA GeForce GTX 1660 SUPER", 1920, 1080, "balanced"},
		{"AMD Ryzen 5 2600 Six-Core Processor", "NVIDIA GeForce RTX 4090", 1920, 1080, "cpu"},
		{"AMD Ryzen 5 2600 Six-Core Processor", "NVIDIA GeForce RTX 4090", 3840, 2160, "balanced"},
		{"12th Gen Intel(R) Core(TM) i9-12900K", "NVIDIA GeForce RTX 4090", 3440, 1440, "balanced"},
		{"12th Gen Intel(R) Core(TM) i9-12900K", "NVIDIA GeForce RTX 4090", 1920, 1080, "cpu"},
		{"AMD Ryzen 7 9800X3D 8-Core Processor", "NVIDIA GeForce GTX 1650", 1920, 1080, "gpu"},
		{"AMD Ryzen 5 5600X 6-Core Processor", "NVIDIA GeForce RTX 3060", 1920, 1080, "balanced"},
		{"Intel(R) Core(TM) i5-10400F CPU @ 2.90GHz", "NVIDIA GeForce RTX 3060 Ti", 1920, 1080, "balanced"},
		{"12th Gen Intel(R) Core(TM) i7-12700H", "NVIDIA GeForce RTX 4060 Laptop GPU", 1920, 1080, "unknown"},
	}
	for _, c := range cases {
		r := benchResult{GPU: &benchGPU{Name: c.gpu}}
		b := bottleneck(&r, benchCtx{CPU: c.cpu, GPUs: []string{c.gpu}, ResW: c.w, ResH: c.h})
		if b == nil || b.Verdict != c.want {
			t.Errorf("%s + %s at %dx%d: %+v, want %s", c.cpu, c.gpu, c.w, c.h, b, c.want)
			continue
		}
		t.Logf("%-40s %-32s %s: %s (cpu %.0f fps, gpu %.0f fps, %d%%)", c.cpu, c.gpu, b.Res, b.Verdict, b.CPUFps, b.GPUFps, b.Pct)
	}
}

func TestAnalyzeGoodPC(t *testing.T) {
	r := benchResult{Threads: 24, CPU1: 3980, CPUN: 41200, RAMBW: 46, RAMLat: 71,
		Disk: &benchDisk{Drive: "C:", SeqRead: 6800, SeqWrit: 5400, RndRead: 78, Kind: "nvme4"},
		GPU: &benchGPU{Name: "NVIDIA GeForce RTX 4090", Renderer: "ANGLE (NVIDIA, NVIDIA GeForce RTX 4090 Direct3D11)", GFLOPS: 9900,
			UtilMax: 99, ClockMax: 2745, BoostMax: 3105, TempMax: 66, PCIeGen: 4, PCIeW: 16, PCIeMaxG: 4, PCIeMaxW: 16, Sensors: true}}
	c := benchCtx{CPU: "12th Gen Intel Core i9-12900K", Cores: 16, Threads: 24, GPUs: []string{"NVIDIA GeForce RTX 4090"},
		RAM: []benchRAM{{Size: 16384, Rated: 6400, Cfg: 6400, Type: "DDR5"}, {Size: 16384, Rated: 6400, Cfg: 6400, Type: "DDR5"}}, DiskBus: "NVMe", DiskKind: "SSD", DiskFree: 40, ResW: 3440, ResH: 1440}
	r.score()
	analyzeBench(&r, c)
	k := noteKeys(r)
	for _, want := range []string{"good:bn.n.cpuScale", "good:bn.n.ramOK", "good:bn.n.diskOK", "good:bn.n.gpuOK"} {
		if !strings.Contains(k, want) {
			t.Errorf("missing %s in %s", want, k)
		}
	}
	if strings.Contains(k, "warn:") || strings.Contains(k, "bad:") {
		t.Errorf("a healthy PC got warnings: %s", k)
	}
	if r.Balance == nil || r.Balance.Verdict != "balanced" || r.Balance.GPUClass != "enthusiast" || r.Balance.Res != "1440p" || len(r.Balance.Rows) != 3 || r.SGPU <= 0 {
		t.Fatalf("balance %+v sgpu %d", r.Balance, r.SGPU)
	}
}

func TestAnalyzeProblems(t *testing.T) {
	r := benchResult{Threads: 12, CPU1: 1400, CPUN: 3000, Load0: 35, RAMBW: 12, RAMLat: 130,
		CPUSustain: []float64{9000, 9100, 8000, 6500, 6300, 6200},
		Disk:       &benchDisk{Drive: "D:", SeqRead: 480, SeqWrit: 420, RndRead: 30, Kind: "sata"},
		GPU: &benchGPU{Name: "NVIDIA GeForce RTX 4080", Renderer: "ANGLE (Intel, Intel(R) UHD Graphics 770 Direct3D11)", GFLOPS: 600,
			UtilMax: 98, ClockMax: 1500, BoostMax: 2800, TempMax: 88, Limits: []string{"thermal"}, PCIeGen: 3, PCIeW: 8, PCIeMaxG: 4, PCIeMaxW: 16, Sensors: true}}
	c := benchCtx{CPU: "AMD Ryzen 5 3600", Cores: 6, Threads: 12, GPUs: []string{"NVIDIA GeForce RTX 4080", "Intel(R) UHD Graphics 770"},
		RAM: []benchRAM{{Size: 16384, Rated: 3600, Cfg: 2133, Type: "DDR4"}}, DiskBus: "NVMe", DiskKind: "SSD", DiskFree: 6}
	r.score()
	analyzeBench(&r, c)
	k := noteKeys(r)
	for _, want := range []string{"warn:bn.n.cpuScaleLow", "warn:bn.n.cpuBusy", "bad:bn.n.cpuThrottle", "warn:bn.n.ramXMP", "warn:bn.n.ramSingle",
		"warn:bn.n.nvmeSlow", "warn:bn.n.diskFull", "warn:bn.n.gpuIGP", "bad:bn.n.gpuHot", "warn:bn.n.gpuPCIe", "warn:bn.n.balCPU"} {
		if !strings.Contains(k, want) {
			t.Errorf("missing %s in %s", want, k)
		}
	}
	for _, n := range r.Notes {
		if n.Key == "bn.n.ramXMP" && (n.Args[0] != "2133" || n.Args[1] != "3600" || n.Args[2] != "EXPO/DOCP") {
			t.Errorf("xmp args %v", n.Args)
		}
	}
	if r.Notes[0].Part != "pc" {
		t.Errorf("the bottleneck comes first: %v", r.Notes[0])
	}
	if r.Balance.Verdict != "cpu" || r.Balance.Pos >= 50 || r.Balance.GPUName != "GeForce RTX 4080" || r.Balance.CPUName != "AMD Ryzen 5 3600" {
		t.Fatalf("%+v", r.Balance)
	}
}

func TestBottleneckUnknownAndDrop(t *testing.T) {
	r := benchResult{SCPU1: 1750}
	if b := bottleneck(&r, benchCtx{CPU: "AMD Ryzen 5 2600", GPUs: []string{"Mystery 3D"}}); b.Verdict != "unknown" || b.Missing != "gpu" {
		t.Fatalf("%+v", b)
	}
	if b := bottleneck(&r, benchCtx{CPU: "Some CPU 9000", GPUs: []string{"NVIDIA GeForce RTX 3060"}}); b.Verdict != "unknown" || b.Missing != "cpu" {
		t.Fatalf("%+v", b)
	}
	if sustainDrop([]float64{100, 100, 100, 80, 80, 80}) != 20 || sustainDrop([]float64{1, 2}) != 0 {
		t.Fatal("drop")
	}
	if resClass(1920, 1080) != "1080p" || resClass(2560, 1440) != "1440p" || resClass(3440, 1440) != "1440p" || resClass(3840, 2160) != "4K" || resClass(0, 0) != "1080p" {
		t.Fatal("res")
	}
}

func TestBenchLongRun(t *testing.T) {
	r := runBench(benchOpts{CPU: true, Long: true, Quick: true}, nil, "", 0)
	if !r.Long || len(r.CPUSustain) < 3 || r.CPUN <= 0 {
		t.Fatalf("long: %+v", r)
	}
}
