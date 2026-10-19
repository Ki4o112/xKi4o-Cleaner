//go:build windows

package main

import (
	"os"
	"strings"
	"testing"
)

// The drive test's file on Windows: past the cache, and gone when closed.
// Short and in the test's own temporary folder, so it also runs on a real PC.
func TestBenchWindowsFile(t *testing.T) {
	dir := t.TempDir()
	r := runBench(benchOpts{Disk: true, Drive: "C:", Quick: true}, openBenchFileWin, dir, 0)
	if r.Disk == nil || r.Disk.Error != "" || r.Disk.SeqRead <= 0 || r.Disk.IOPSW <= 0 {
		t.Fatalf("%+v", r.Disk)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
	t.Logf("16 MB in Temp: ↓%.0f ↑%.0f MB/s, 4K ↓%.1f ↑%.1f MB/s (%s)", r.Disk.SeqRead, r.Disk.SeqWrit, r.Disk.RndRead, r.Disk.RndWrit, r.Disk.Kind)
}

// What browsers and extensions this PC has (read only).
func TestExtScanHere(t *testing.T) {
	v := extScan()
	t.Logf("browsers %v: %d extensions, %d high, %d medium", v.Browsers, len(v.Exts), v.High, v.Medium)
	for _, x := range v.Exts {
		t.Logf("  [%s] %-8s %-30s %-12s on=%v store=%v allSites=%v %v", x.Risk, x.Browser, x.Name, x.Version, x.Enabled, x.Store, x.AllSites, x.Perms)
	}
	if r := extOpen("chrome", `x" & calc`); r["error"] == nil {
		t.Fatal("a bad id must be refused")
	}
	if extShowFolder(`C:\Windows\System32`) {
		t.Fatal("only extension folders can be shown")
	}
	if reportOpen(`C:\Windows\notepad.exe`) || reportShow(`C:\x\other.html`) {
		t.Fatal("only the app's own reports can be opened")
	}
}

// The report's content, from this PC (read only: the HTML is only built in
// memory here; making the file on the Desktop is tested under Wine).
func TestReportBuildHere(t *testing.T) {
	d := buildReport(systemInfo(false), diskHealthList(false), getDrives(), getNetState(), listStartupQuick(), extScan(), nil)
	h := renderReport(d)
	if !strings.Contains(h, "<html") || len(d.Sections) < 4 {
		t.Fatalf("%d sections", len(d.Sections))
	}
	t.Logf("score %d, %d findings, %d KB", d.Score, len(d.Findings), len(h)/1024)
	for _, f := range d.Findings {
		t.Logf("  [%s] %s", f.Tone, f.Text)
	}
}

func TestReportMakeFile(t *testing.T) {
	changesThisPC(t)
	r := reportMake()
	p, _ := r["path"].(string)
	if p == "" {
		t.Fatalf("%v", r)
	}
	defer os.Remove(p)
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "xKi4o Cleaner") {
		t.Fatal(err)
	}
}
