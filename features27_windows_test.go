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
	// the cached answer comes back at once
	t0 := time.Now()
	if c := wingetUpdates(false); c.Checked != v.Checked || time.Since(t0) > time.Second {
		t.Error("not cached")
	}
	if wingetUpgrade([]string{"--all"}) || wingetUpgrade([]string{"a b"}) || wingetUpgrade(nil) {
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
	for _, d := range v.Disks {
		show := func(p *int) string {
			if p == nil {
				return "-"
			}
			return strconv.Itoa(*p)
		}
		t.Logf("disk %s %q %s/%s %d GB letters=%v health=%s status=%v temp=%s wear=%s hours=%s verdict=%s %v",
			d.ID, d.Name, d.Media, d.Bus, d.Size>>30, d.Letters, d.Health, d.Status, show(d.Temp), show(d.Wear), show(d.Hours), d.Verdict, d.Reasons)
		if d.Verdict == "" {
			t.Errorf("no verdict: %+v", d)
		}
	}
}

// Game mode offers only the user's own programs: never Windows, security
// software, services or this program. Nothing is closed here.
func TestGameAppsList(t *testing.T) {
	apps := gameApps()
	self := strings.ToLower(filepath.Base(os.Args[0]))
	for _, a := range apps {
		if windowsProcs[a.Exe] || securityProcs[a.Exe] || a.Exe == self || a.Exe == "explorer.exe" {
			t.Errorf("offered: %+v", a)
		}
		if a.Path != "" && underWindowsDir(a.Path) {
			t.Errorf("a program of Windows offered: %+v", a)
		}
	}
	t.Logf("%d programs could be closed in game mode", len(apps))
	for i, a := range apps {
		if i < 10 {
			t.Logf("  %s (%s) %d MB window=%v x%d", a.Name, a.Exe, a.Mem>>20, a.Window, a.Count)
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
	for i, x := range b {
		if i < 10 {
			t.Logf("  [%s] %s -> %s", x.Where, x.Path, x.Target)
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
