package main

import (
	"os"
	"path/filepath"
	"testing"
)

type memIO struct{ b []byte }

func (m *memIO) WriteAt(p []byte, off int64) (int, error) { return copy(m.b[off:], p), nil }
func (m *memIO) ReadAt(p []byte, off int64) (int, error)  { return copy(p, m.b[off:]), nil }
func (m *memIO) Close() error                             { return nil }

func TestBenchQuick(t *testing.T) {
	dir := t.TempDir()
	r := runBench(benchOpts{CPU: true, RAM: true, Disk: true, Drive: "C:", Quick: true},
		func(path string, size int64) (benchIO, error) { return &memIO{b: make([]byte, size)}, nil }, dir, 0)
	if r.Error != "" || r.CPU1 <= 0 || r.CPUN < r.CPU1 || r.RAMBW <= 0 || r.RAMLat <= 0 || r.Disk == nil || r.Disk.SeqRead <= 0 || r.Disk.IOPSR <= 0 {
		t.Fatalf("%+v %+v", r, r.Disk)
	}
	if r.Score <= 0 || r.SCPU1 <= 0 || r.SRAM <= 0 || r.SDisk <= 0 {
		t.Fatalf("scores %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "xKi4o-benchmark.tmp")); !os.IsNotExist(err) {
		t.Fatal("the test file was left behind")
	}
	t.Logf("cpu1 %.0f cpuN %.0f (%d threads) ram %.1f GB/s %.0f ns score %d", r.CPU1, r.CPUN, r.Threads, r.RAMBW, r.RAMLat, r.Score)
}

func TestBenchStopAndBusy(t *testing.T) {
	bench.busy.Store(true)
	if r := runBench(benchOpts{CPU: true, Quick: true}, nil, "", 0); r.Error != errBenchBusy.Error() {
		t.Fatal("a second run at the same time must be refused")
	}
	bench.busy.Store(false)
	benchProgress = func(step string, pct int) { bench.stop.Store(true) }
	defer func() { benchProgress = func(string, int) {} }()
	r := runBench(benchOpts{CPU: true, RAM: true, Quick: true}, nil, "", 0)
	if !r.Stopped || r.RAMBW != 0 {
		t.Fatalf("stop: %+v", r)
	}
}

func TestBenchPointsAndHistory(t *testing.T) {
	if pts(refCPU1, refCPU1) != 1000 || pts(0, 1) != 0 || geo(1000, 0, 4000) != 2000 {
		t.Fatal("points")
	}
	for v, k := range map[[2]float64]string{{7000, 80}: "nvme4", {3500, 60}: "nvme3", {550, 30}: "sata", {180, 1}: "hdd", {40, 0.5}: "slow"} {
		if diskKind(v[0], v[1]) != k {
			t.Errorf("%v: %s", v, diskKind(v[0], v[1]))
		}
	}
	p := filepath.Join(t.TempDir(), "h.json")
	for i := 0; i < benchKeep+5; i++ {
		addBenchHistory(p, benchResult{Score: i})
	}
	h := readBenchHistory(p)
	if len(h) != benchKeep || h[0].Score != benchKeep+4 {
		t.Fatalf("history %d first %d", len(h), h[0].Score)
	}
}
