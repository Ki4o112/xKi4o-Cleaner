//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winget only lists here ("winget upgrade" without a package changes nothing).
func TestWingetList(t *testing.T) {
	if wingetExe() == "" {
		t.Skip("winget is not installed")
	}
	start := time.Now()
	v := wingetUpdates(true)
	t.Logf("winget: available=%v, %d updates, error=%q, %s", v.Available, len(v.Updates), v.Error, time.Since(start).Round(time.Millisecond))
	for _, u := range v.Updates {
		t.Logf("  %s [%s] %s -> %s explicit=%v cut=%v", u.Name, u.ID, u.Version, u.Available, u.Explicit, u.Cut)
		if !u.Cut && !validWingetID(u.ID) {
			t.Errorf("an id that would be refused: %q", u.ID)
		}
	}
	if !v.Available {
		t.Error("winget is there but was not used")
	}
	var ids []string
	for _, u := range v.Updates {
		ids = append(ids, u.ID)
		if s := selfUpdater(u.ID); s != "" {
			t.Logf("  %s is updated by its own updater (%s)", u.ID, s)
		}
	}
	for _, o := range wingetOpen(ids) { // only reads the process list
		t.Logf("  open now: %s (%s, %d processes)", o.Name, o.ID, o.Count)
	}
	// the cached answer comes back at once
	t0 := time.Now()
	if c := wingetUpdates(false); c.Checked != v.Checked || time.Since(t0) > time.Second {
		t.Error("not cached")
	}
	if wingetUpgrade([]string{"--all"}, wgOpts{}) || wingetUpgrade([]string{"a b"}, wgOpts{}) || wingetUpgrade(nil, wgOpts{}) {
		t.Error("bad ids must be refused before winget runs")
	}
}

// Only reads what the drives report.
func TestDiskHealthRead(t *testing.T) {
	v := diskHealthList(true)
	if underWine() {
		t.Logf("Wine: %+v", v)
		return
	}
	if v.Error != "" || len(v.Disks) == 0 {
		t.Fatalf("%+v", v)
	}
	t.Logf("as administrator: %v (the drives' counters need it)", windows.GetCurrentProcessToken().IsElevated())
	for _, d := range v.Disks {
		show := func(p *int) string {
			if p == nil {
				return "-"
			}
			return strconv.Itoa(*p)
		}
		show64 := func(p *int64) string {
			if p == nil {
				return "-"
			}
			return strconv.FormatInt(*p, 10)
		}
		t.Logf("disk %s %q %s/%s %d GB letters=%v health=%s status=%v temp=%s wear=%s hours=%s starts=%s errors read=%s/%s write=%s/%s (all/uncorrected) verdict=%s %v",
			d.ID, d.Name, d.Media, d.Bus, d.Size>>30, d.Letters, d.Health, d.Status, show(d.Temp), show(d.Wear), show(d.Hours), show(d.Starts),
			show64(d.ReadErr), show64(d.ReadUnc), show64(d.WriteErr), show64(d.WriteUnc), d.Verdict, d.Reasons)
		if d.Verdict == "" {
			t.Errorf("no verdict: %+v", d)
		}
		if strings.EqualFold(d.Bus, "NVMe") {
			b, err := nvmeHealthLog(d.ID)
			l, ok := parseNVMeLog(b)
			t.Logf("  NVMe log: err=%v ok=%v critical=%#x temp=%dK used=%d%% spare=%d%% hours=%d cycles=%d unsafe=%d mediaErr=%d written=%.1f TB",
				err, ok, l.Critical, l.TempK, l.Used, l.Spare, l.Hours, l.Cycles, l.Unsafe, l.MediaErr, float64(l.Written)*512000/1e12)
		}
	}
}

// Game mode offers only the user's own programs: never Windows, security
// software, services or this program. Nothing is closed here.
func TestGameAppsList(t *testing.T) {
	apps := gameApps()
	self := strings.ToLower(filepath.Base(os.Args[0]))
	for _, a := range apps {
		if windowsProcs[a.Exe] || securityProcs[a.Exe] || a.Exe == self || a.Exe == "explorer.exe" || strings.Contains(a.Exe, "xki4ocleaner") || !strings.HasSuffix(a.Exe, ".exe") {
			t.Errorf("offered: %+v", a)
		}
		if a.Path != "" && underWindowsDir(a.Path) {
			t.Errorf("a program of Windows offered: %+v", a)
		}
	}
	listed := map[string]bool{}
	for _, a := range apps {
		listed[a.Exe] = true
	}
	for _, a := range apps {
		for _, h := range a.Helpers {
			if listed[h] {
				t.Errorf("%s is listed on its own and as a helper of %s", h, a.Exe)
			}
		}
	}
	t.Logf("%d programs could be closed in game mode", len(apps))
	for i, a := range apps {
		if i < 10 {
			t.Logf("  %s (%s) %d MB window=%v x%d helpers=%v", a.Name, a.Exe, a.Mem>>20, a.Window, a.Count, a.Helpers)
		}
	}
	if st := gameState(); st.Active {
		t.Logf("game mode is on (since %s)", time.UnixMilli(st.Since))
	}
}

// The command line of this test process is read back.
func TestProcessCommandLine(t *testing.T) {
	cl := processCommandLine(windows.GetCurrentProcessId())
	if cl == "" && underWine() {
		t.Skip("Wine does not give command lines this way")
	}
	if !strings.Contains(strings.ToLower(cl), strings.ToLower(strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"))) {
		t.Errorf("command line %q", cl)
	}
}

// A program started for game mode runs without administrator rights. The
// test starts ping.exe (no window), checks its token and ends it.
func TestStartAsUserIsNotElevated(t *testing.T) {
	if underWine() {
		t.Skip("Wine has no desktop shell token")
	}
	ping := filepath.Join(windowsDir(), "System32", "PING.EXE")
	h, err := startAsUserProc(ping, `"`+ping+`" -n 6 127.0.0.1`, 0x08000000)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer windows.CloseHandle(h)
	defer func() { _ = windows.TerminateProcess(h, 0) }()
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	var elevated uint32
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &n); err != nil {
		t.Fatal(err)
	}
	t.Logf("this test elevated=%v; started program elevated=%v", windows.GetCurrentProcessToken().IsElevated(), elevated != 0)
	if elevated != 0 && windows.GetCurrentProcessToken().IsElevated() {
		// both elevated is right only when the desktop itself runs elevated (UAC off)
		if ex := shellElevated(); !ex {
			t.Error("started with administrator rights")
		}
	}
}

func shellElevated() bool {
	hwnd, _, _ := procGetShellWindow.Call()
	var pid uint32
	_, _ = windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid)
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok) != nil {
		return false
	}
	defer tok.Close()
	return tok.IsElevated()
}

// Empty folders: found in a folder of this test; removing them runs only
// where it may change the PC (the Recycle Bin).
func TestEmptyFoldersFound(t *testing.T) {
	initPaths()
	root := t.TempDir()
	for _, d := range []string{`a\b\c`, `d\e`, `.git\refs\heads`, `keep`} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	mk(t, filepath.Join(root, "d", "x.txt"), 1, 0)
	mk(t, filepath.Join(root, "keep", "y.txt"), 1, 0)
	res := emptyScan([]string{root})
	var got []string
	for _, d := range res.Dirs {
		got = append(got, strings.TrimPrefix(d.Path, root+`\`))
	}
	if strings.Join(got, ",") != `a,d\e` {
		t.Fatalf("found %v", got)
	}
	for _, bad := range []string{`C:\`, windowsDir(), os.Getenv("ProgramFiles"), filepath.Join(os.Getenv("ProgramFiles"), "x")} {
		if tidyRootOK(bad) {
			t.Errorf("%s must not be searched", bad)
		}
	}
	places := tidyPlaces()
	t.Logf("places: %+v", places)
	// broken shortcuts of this PC: only read
	b := brokenScan()
	t.Logf("%d broken shortcuts", len(b))
	tidyLast.mu.Lock()
	for p, to := range tidyLast.found {
		t.Logf("  path gone, but Windows opens it: %s -> %s", p, to)
	}
	tidyLast.mu.Unlock()
	for i, x := range b {
		if i < 12 {
			li, _ := readLnkFile(x.Path)
			t.Logf("  [%s] %s -> %s (also tried %q)", x.Where, x.Path, x.Target, li.Alts)
		}
		li, err := readLnkFile(x.Path)
		if err != nil {
			continue
		}
		for _, c := range append([]string{li.Target}, li.Alts...) {
			if _, err := os.Stat(c); err == nil {
				t.Errorf("%s is called broken, but %s is there", x.Path, c)
			}
		}
	}
}

func TestEmptyFoldersRemoved(t *testing.T) {
	changesThisPC(t)
	initPaths()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, `a\b`), 0o755)
	_ = os.MkdirAll(filepath.Join(root, `c`), 0o755)
	res := emptyScan([]string{root})
	if len(res.Dirs) != 2 {
		t.Fatalf("%+v", res)
	}
	// c got a file after the search: it is skipped
	mk(t, filepath.Join(root, "c", "new.txt"), 1, 0)
	out := tidyDelete("dir", []string{filepath.Join(root, "a"), filepath.Join(root, "c"), filepath.Join(root, "zzz")})
	if len(out) != 3 || !out[0].OK || out[1].OK || out[2].OK || exists(filepath.Join(root, "a")) || !exists(filepath.Join(root, "c", "new.txt")) {
		t.Fatalf("%+v", out)
	}
	// a broken shortcut is removed; one that works is kept
	lnkDir := t.TempDir()
	target := filepath.Join(lnkDir, "app.exe")
	mk(t, target, 10, 0)
	makeLnk(t, filepath.Join(lnkDir, "gone.lnk"), filepath.Join(lnkDir, "missing.exe"))
	makeLnk(t, filepath.Join(lnkDir, "ok.lnk"), target)
	tidyLast.mu.Lock()
	tidyLast.broken = map[string]bool{strings.ToLower(filepath.Join(lnkDir, "gone.lnk")): true, strings.ToLower(filepath.Join(lnkDir, "ok.lnk")): true}
	tidyLast.mu.Unlock()
	out = tidyDelete("lnk", []string{filepath.Join(lnkDir, "gone.lnk"), filepath.Join(lnkDir, "ok.lnk")})
	if !out[0].OK || out[1].OK || !exists(filepath.Join(lnkDir, "ok.lnk")) {
		t.Fatalf("%+v", out)
	}
}

// A shortcut that names C:\Program Files\X while X is in Program Files (x86)
// works in Windows: it is not called broken (and is not removed). One whose
// program is gone everywhere is. Writes two shortcuts on the desktop.
func TestBrokenScanTwinFolders(t *testing.T) {
	changesThisPC(t)
	initPaths()
	pf, pf86 := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")
	if pf == "" || pf86 == "" || strings.EqualFold(pf, pf86) {
		t.Skip("no 32-bit Program Files here")
	}
	real := filepath.Join(pf86, "xki4o-test-twin", "app.exe")
	mk(t, real, 10, 0)
	defer os.RemoveAll(filepath.Dir(real))
	desktop := knownFolder(windows.FOLDERID_Desktop, filepath.Join(P.profile, "Desktop"))
	twin, gone := filepath.Join(desktop, "xki4o-test-twin.lnk"), filepath.Join(desktop, "xki4o-test-gone.lnk")
	makeLnk(t, twin, filepath.Join(pf, "xki4o-test-twin", "app.exe"))
	makeLnk(t, gone, filepath.Join(pf, "xki4o-test-gone", "app.exe"))
	defer os.Remove(twin)
	defer os.Remove(gone)
	if li, err := readLnkFile(twin); err != nil || !strings.EqualFold(li.Target, real) {
		t.Errorf("twin: %+v %v", li, err)
	}
	found := map[string]bool{}
	for _, b := range brokenScan() {
		found[strings.ToLower(b.Path)] = true
	}
	if found[strings.ToLower(twin)] || !found[strings.ToLower(gone)] {
		t.Fatalf("twin broken=%v, gone broken=%v", found[strings.ToLower(twin)], found[strings.ToLower(gone)])
	}
	out := tidyDelete("lnk", []string{twin, gone})
	if out[0].OK || !out[1].OK || !exists(twin) {
		t.Fatalf("%+v", out)
	}
}

// Windows' own answer for a shortcut: one that works resolves to its
// target, one whose target is gone does not. Temporary files only.
func TestShellResolve(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.exe")
	mk(t, target, 10, 0)
	ok := filepath.Join(dir, "ok.lnk")
	makeLnk(t, ok, target)
	if got, works := shellResolve(ok, 1500*time.Millisecond); !works || !strings.EqualFold(got, target) {
		t.Errorf("a working shortcut: %q %v", got, works)
	}
	gone := filepath.Join(dir, "gone.lnk")
	makeLnk(t, gone, filepath.Join(dir, "sub", "missing-xki4o-test.exe"))
	start := time.Now()
	if got, works := shellResolve(gone, 1500*time.Millisecond); works {
		t.Errorf("a broken shortcut resolved to %q", got)
	}
	t.Logf("broken shortcut checked in %v", time.Since(start).Round(time.Millisecond))
	if _, works := shellResolve(filepath.Join(dir, "no-such.lnk"), time.Second); works {
		t.Error("a missing shortcut file")
	}
}

// The details of every disk: S.M.A.R.T., the System log, the advice. Only reads.
func TestDiskDetailsRead(t *testing.T) {
	v := diskHealthList(false)
	if underWine() {
		dd := diskDetails("0")
		t.Logf("Wine: %+v", dd)
		return
	}
	for _, d := range v.Disks {
		start := time.Now()
		dd := diskDetails(d.ID)
		t.Logf("disk %s %q letters=%v system=%v: %d attributes %q, %d kinds of log entries %q (%v)", d.ID, d.Name, dd.Letters, dd.System,
			len(dd.Smart), dd.SmartNote, len(dd.Events), dd.EventNote, time.Since(start).Round(time.Millisecond))
		for _, a := range dd.Smart {
			t.Logf("    smart %3d cur=%d worst=%d thr=%d raw=%d count=%d bad=%v", a.ID, a.Cur, a.Worst, a.Thr, a.Raw, smartCount(a), a.Bad)
		}
		if dd.NVMe != nil {
			t.Logf("    nvme %+v", *dd.NVMe)
		}
		for _, e := range dd.Events {
			msg := e.Msg
			if r := []rune(msg); len(r) > 140 {
				msg = string(r[:140])
			}
			t.Logf("    log %s %d x%d last %s (%s): %s", e.Provider, e.ID, e.Count, time.UnixMilli(e.Last).Format("2006-01-02"), e.Key, msg)
		}
		for _, a := range dd.Advice {
			t.Logf("    advice %s n=%d action=%q", a.Key, a.N, a.Action)
		}
		if len(dd.Advice) == 0 {
			t.Errorf("disk %s: no advice", d.ID)
		}
	}
}

// How fragmented the HDDs are (Windows' own analysis: only reads).
func TestAnalyzeHDDs(t *testing.T) {
	if underWine() {
		t.Logf("Wine: %+v", analyzeDrive("C"))
		return
	}
	for _, d := range getDrives() {
		t.Logf("drive %s: %s disk %q system=%v", d.Letter, d.Media, d.Disk, d.System)
		if d.Media != "HDD" {
			continue
		}
		start := time.Now()
		f := analyzeDrive(d.Letter)
		t.Logf("    fragmented %d%% (files %d%%), recommended=%v error=%q (%v)", f.Total, f.Files, f.Recommended, f.Error, time.Since(start).Round(time.Second))
	}
}

// The disk checks a disk's details offer: a read-only check of any fixed
// drive, the bad sector check never on the Windows drive. Nothing runs here.
func TestRepairPlanForDrives(t *testing.T) {
	sys := strings.TrimSuffix(systemDrive(), ":")
	if p := repairPlan("scan:" + sys); len(p) != 1 || len(p[0].args) != 2 || p[0].args[1] != "/scan" {
		t.Errorf("scan: %+v", p)
	}
	if p := repairPlan("fix:" + sys); p != nil {
		t.Errorf("the Windows drive must not get chkdsk /r /x: %+v", p)
	}
	for _, bad := range []string{"fix:1", "scan:", "scan:CC", "rm:" + sys, "fix:" + sys + `\`} {
		if p := repairPlan(bad); p != nil {
			t.Errorf("%q: %+v", bad, p)
		}
	}
	for _, d := range getDrives() {
		if !d.System {
			if p := repairPlan("fix:" + d.Letter); len(p) != 1 || strings.Join(p[0].args, " ") != d.Letter+": /r /x" {
				t.Errorf("fix %s: %+v", d.Letter, p)
			}
		}
	}
}
