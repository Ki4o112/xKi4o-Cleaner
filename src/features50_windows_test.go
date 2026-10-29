//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The exe of a game an anti-cheat protects comes from the process list.
func TestPathFromList(t *testing.T) {
	exe, _ := os.Executable()
	got := pathFromList(windows.GetCurrentProcessId())
	if !strings.EqualFold(fullNames(got), fullNames(exe)) {
		t.Fatalf("got %q, want %q", got, exe)
	}
	if pathFromList(0xFFFFFFF0) != "" {
		t.Fatal("a PID that does not exist")
	}
}

// Windows' start measurements: read only.
func TestBootPerfRead(t *testing.T) {
	p := bootPerfRead(true)
	if p.Error != "" || p.Boots == nil || p.Slow == nil {
		t.Fatalf("%+v", p)
	}
	t.Logf("%d starts, %d slowing items", len(p.Boots), len(p.Slow))
	for _, b := range p.Boots {
		t.Logf("start %v: %d ms (%d + %d)", time.UnixMilli(b.At), b.Total, b.Main, b.Post)
	}
}

// The processor's limit counter; a VM may not have it.
func TestThrottleRead(t *testing.T) {
	s, ok := thRead()
	if !ok {
		t.Skip("no % Performance Limit counter here")
	}
	time.Sleep(time.Second)
	s, ok = thRead()
	t.Logf("cpu %.0f%% limit %.0f%% perf %.0f%% temp %.1f °C battery %v", s.CPU, s.Limit, s.Perf, s.TempC, s.Bat)
	if !ok || s.Limit < 0 || s.Limit > 100 || s.CPU < 0 || s.CPU > 100 {
		t.Fatalf("%+v", s)
	}
	v := throttleState()
	if !v.OK || v.Samples == nil || v.Events == nil {
		t.Fatalf("%+v", v)
	}
}

func TestBatteryInfoRead(t *testing.T) {
	b := batteryInfo()
	t.Logf("%+v", b)
	if b.Error != "" {
		t.Fatal(b.Error)
	}
	if b.OK != hasBattery() {
		t.Errorf("battery %v, Windows says %v", b.OK, hasBattery())
	}
}

func TestTrimQuery(t *testing.T) {
	out, _, err := runQuiet(15*time.Second, sys32("fsutil.exe"), "behavior", "query", "DisableDeleteNotify")
	t.Logf("%q %v", out, err)
	if s := trimState(out); s == "" {
		t.Fatalf("not read from %q", out)
	}
}

// A heavy background process goes to the lowest priority while the PC is
// used, and gets its own back after.
func TestBgTame(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "compattelrunner.exe")
	b, err := os.ReadFile(sys32("ping.exe"))
	if err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(fake, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fake, "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	time.Sleep(300 * time.Millisecond)
	prio := func() uint32 {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		p, err := windows.GetPriorityClass(h)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := prio()
	bgTameStep(true)
	if p := prio(); p != idlePriority {
		t.Fatalf("held back: priority %#x", p)
	}
	if v := bgTameState(); !v.Now || !strings.Contains(strings.Join(v.Tamed, ","), "compattelrunner.exe") {
		t.Fatalf("state %+v", v)
	}
	bgTameStep(false)
	if p := prio(); p != before {
		t.Fatalf("given back: %#x, had %#x", p, before)
	}
	if idleSeconds() < 0 {
		t.Fatal("idle time")
	}
}

var procGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")

// The card over the game: a click-through window that shows and goes away
// by itself.
func TestGameNotifyCard(t *testing.T) {
	gameNotify("Test", "a card")
	deadline := time.Now().Add(2 * time.Second)
	for gameNote.hwnd.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	h := gameNote.hwnd.Load()
	if h == 0 {
		t.Fatal("no window")
	}
	if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
		t.Error("not shown")
	}
	ex, _, _ := procGetWindowLongPtrW.Call(h, ^uintptr(19)) // GWL_EXSTYLE (-20)
	if ex&wsExTransparent == 0 || ex&wsExNoActivate == 0 || ex&wsExTopmost == 0 {
		t.Errorf("styles %#x", ex)
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg == h {
		t.Error("it took the focus")
	}
	deadline = time.Now().Add(gnShow + 3*time.Second)
	for gameNote.hwnd.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if gameNote.hwnd.Load() != 0 {
		t.Fatal("it did not go away")
	}
}
