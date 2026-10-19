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

func TestGPUIndex(t *testing.T) {
	for name, want := range map[string]int{
		"NVIDIA GeForce RTX 4090": 330, "NVIDIA GeForce RTX 4070 Ti SUPER": 225, "NVIDIA GeForce RTX 4070 Ti": 210, "NVIDIA GeForce RTX 4070": 165,
		"NVIDIA GeForce RTX 3060 Ti": 125, "NVIDIA GeForce RTX 3060": 100, "AMD Radeon RX 7900 XTX": 270, "AMD Radeon RX 7900 XT": 235,
		"Intel(R) Arc(TM) B580 Graphics": 118, "Intel(R) UHD Graphics 770": 8, "NVIDIA GeForce RTX 4060 Laptop GPU": 73, "Some Unknown GPU": 0,
		"AMD Radeon(TM) Graphics": 16, "AMD Radeon 780M Graphics": 34,
	} {
		if got := gpuIndexOf(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	if !isIntegrated("Intel(R) UHD Graphics 770") || !isIntegrated("ANGLE (AMD, AMD Radeon(TM) Graphics Direct3D11)") || isIntegrated("NVIDIA GeForce RTX 4090") {
		t.Fatal("integrated graphics")
	}
}

func TestAnalyzeGoodPC(t *testing.T) {
	r := benchResult{Threads: 24, CPU1: 3980, CPUN: 41200, RAMBW: 46, RAMLat: 71,
		Disk: &benchDisk{Drive: "C:", SeqRead: 6800, SeqWrit: 5400, RndRead: 78, Kind: "nvme4"},
		GPU: &benchGPU{Name: "NVIDIA GeForce RTX 4090", Renderer: "ANGLE (NVIDIA, NVIDIA GeForce RTX 4090 Direct3D11)", GFLOPS: 9900,
			UtilMax: 99, ClockMax: 2745, BoostMax: 3105, TempMax: 66, PCIeGen: 4, PCIeW: 16, PCIeMaxG: 4, PCIeMaxW: 16, Sensors: true}}
	c := benchCtx{CPU: "12th Gen Intel Core i9-12900K", Cores: 16, Threads: 24, GPUs: []string{"NVIDIA GeForce RTX 4090"},
		RAM: []benchRAM{{Size: 16384, Rated: 6400, Cfg: 6400, Type: "DDR5"}, {Size: 16384, Rated: 6400, Cfg: 6400, Type: "DDR5"}}, DiskBus: "NVMe", DiskKind: "SSD", DiskFree: 40}
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
	if r.Balance == nil || r.Balance.Verdict != "balanced" || r.Balance.GPUClass != "enthusiast" || r.SGPU <= 0 {
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
	if r.Balance.Verdict != "cpu" || r.Balance.Pos >= 50 {
		t.Fatalf("%+v", r.Balance)
	}
}

func TestBottleneckGPULimited(t *testing.T) {
	r := benchResult{CPU1: 4200, SCPU1: 1750}
	b := bottleneck(&r, benchCtx{GPUs: []string{"NVIDIA GeForce GTX 1650"}})
	if b.Verdict != "gpu" || b.Pos <= 50 {
		t.Fatalf("%+v", b)
	}
	if b := bottleneck(&r, benchCtx{GPUs: []string{"Mystery 3D"}}); b.Verdict != "unknown" {
		t.Fatalf("%+v", b)
	}
	if sustainDrop([]float64{100, 100, 100, 80, 80, 80}) != 20 || sustainDrop([]float64{1, 2}) != 0 {
		t.Fatal("drop")
	}
}

func TestBenchLongRun(t *testing.T) {
	r := runBench(benchOpts{CPU: true, Long: true, Quick: true}, nil, "", 0)
	if !r.Long || len(r.CPUSustain) < 3 || r.CPUN <= 0 {
		t.Fatalf("long: %+v", r)
	}
}
