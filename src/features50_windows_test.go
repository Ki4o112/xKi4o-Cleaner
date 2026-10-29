//go:build windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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

// The start-time script's way of reading events, on a log every Windows
// has (the start log itself is empty on Windows Server).
func TestBootPerfScriptShape(t *testing.T) {
	script := strings.Replace(bootPerfScript, `LogName = 'Microsoft-Windows-Diagnostics-Performance/Operational'; Id = 100,101,102,103`, `LogName = 'System'; Id = 6005,6006,6013,7036,7040`, 1)
	script = strings.Replace(script, "-MaxEvents 400", "-MaxEvents 5", 1)
	if script == bootPerfScript {
		t.Fatal("the script changed: update this test")
	}
	out, _, err := runPS(60*time.Second, script)
	var recs []bootRec
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &recs); jerr != nil {
		t.Fatalf("%v %v: %q", err, jerr, out)
	}
	if len(recs) == 0 {
		t.Skip("no such events here")
	}
	for _, r := range recs {
		t.Logf("%d at %v: %v", r.ID, time.UnixMilli(r.T), r.D)
		if r.T < time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() || r.T > time.Now().Add(time.Hour).UnixMilli() || r.D == nil {
			t.Fatalf("bad record %+v", r)
		}
	}
}

// Going back to a restore point that does not exist is refused before
// Windows is asked anything.
func TestRestoreToPointGuard(t *testing.T) {
	was := app.admin
	defer func() { app.admin = was }()
	app.admin = false
	if got := restoreToPoint(1); got != T("blocked.admin") {
		t.Fatalf("no admin: %q", got)
	}
	app.admin = true
	if got := restoreToPoint(-12345); got != T("rc.err.noPoint") {
		t.Fatalf("unknown point: %q", got)
	}
}

// The full maintenance runs end to end (Windows Update only as administrator).
func TestAutoMaintenance(t *testing.T) {
	initPaths()
	logInit(t.TempDir())
	was := app.admin
	defer func() { app.admin = was }()
	app.admin = false
	start := time.Now()
	m := autoMaintenance(123)
	t.Logf("%+v in %v: %s", m, time.Since(start).Round(time.Millisecond), strings.Join(maintLines(m, T), " · "))
	if m.Freed != 123 || m.RAM < 0 {
		t.Fatalf("%+v", m)
	}
}

// Priority, Efficiency mode and pause on a program that is not the app's
// own (started through cmd, which then ends).
func TestProcControls(t *testing.T) {
	initPaths()
	logInit(t.TempDir())
	dir := t.TempDir()
	fake := filepath.Join(dir, "xki4o-prio-probe.exe")
	b, err := os.ReadFile(sys32("ping.exe"))
	if err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(fake, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(sys32("cmd.exe"), "/c", "start", "", "/min", fake, "-n", "60", "127.0.0.1").Run(); err != nil {
		t.Skip(err)
	}
	var tg procTarget
	for i := 0; i < 50 && tg.PID == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		raw, _ := queryProcesses()
		for _, p := range raw {
			if strings.EqualFold(p.name, "xki4o-prio-probe.exe") {
				tg = procTarget{PID: p.pid, Start: startID(p.created)}
			}
		}
	}
	if tg.PID == 0 {
		t.Skip("the probe did not start")
	}
	defer func() {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, tg.PID); err == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}()
	st := procCtlState(tg)
	t.Logf("state %+v", st)
	if !st.Can || st.Game || st.Prio == "" { // (GitHub's runner starts its programs below normal)
		t.Fatalf("state %+v", st)
	}
	if r := procPriority([]procTarget{tg}, "above", false); r.Done != 1 {
		t.Fatalf("priority %+v", r)
	}
	if st := procCtlState(tg); st.Prio != "above" {
		t.Fatalf("after priority %+v", st)
	}
	if r := procEco([]procTarget{tg}, true); r.Done != 1 {
		t.Fatalf("eco %+v", r)
	}
	if st := procCtlState(tg); !st.Eco || st.Prio != "idle" {
		t.Fatalf("after eco %+v", st)
	}
	if r := procEco([]procTarget{tg}, false); r.Done != 1 {
		t.Fatalf("eco off %+v", r)
	}
	if st := procCtlState(tg); st.Eco || st.Prio != "normal" {
		t.Fatalf("after eco off %+v", st)
	}
	if r := procPause([]procTarget{tg}, true); r.Done != 1 || !procCtlState(tg).Paused {
		t.Fatalf("pause %+v", r)
	}
	if r := procPause([]procTarget{tg}, false); r.Done != 1 || procCtlState(tg).Paused {
		t.Fatalf("resume %+v", r)
	}
	if r := procPriority([]procTarget{{PID: tg.PID, Start: tg.Start + 1}}, "high", false); r.Done != 0 {
		t.Fatal("a process that is not the one shown")
	}
	if r := procPriority([]procTarget{{PID: windows.GetCurrentProcessId(), Start: 0}}, "high", false); r.Done != 0 {
		t.Fatal("the app itself")
	}
}

// A remembered priority goes into Windows' own per-program setting and
// comes out again without leaving keys behind.
func TestPrioRemember(t *testing.T) {
	changesThisPC(t)
	initPaths()
	was := app.admin
	defer func() { app.admin = was }()
	app.admin = isElevated()
	if !app.admin {
		t.Skip("needs administrator rights")
	}
	exe := "xki4o-prio-rule-test.exe"
	if err := prioRemember(exe, "above"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range prioRules() {
		if r.Exe == exe && r.Level == "above" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rules %+v", prioRules())
	}
	if !prioForget(exe) {
		t.Fatal("forget")
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe, registry.QUERY_VALUE); err == nil {
		k.Close()
		t.Fatal("the key stayed")
	}
}

// Game mode's power: the best plan (or power mode) while playing, the
// user's own back after.
func TestGamePower(t *testing.T) {
	changesThisPC(t)
	orig := activeScheme()
	defer func() { _ = runTool(sys32("powercfg.exe"), "-setactive", orig) }()
	if orig != balancedPlan { // start from Balanced, as most PCs are
		if err := runTool(sys32("powercfg.exe"), "-setactive", balancedPlan); err != nil {
			t.Skip(err)
		}
	}
	before := activeScheme()
	ov0, hasOv := overlayGet()
	t.Logf("plan %s, power mode %s (%v)", before, ov0, hasOv)
	var st gmState
	gamePowerUp(&st)
	t.Logf("game mode: %+v", st)
	if st.Plan == "" && st.Overlay == "" {
		t.Skip("this PC is already at its best performance, or has no such setting")
	}
	if st.Plan != "" && activeScheme() != st.Plan {
		t.Fatalf("plan now %s", activeScheme())
	}
	gamePowerBack(st)
	if got := activeScheme(); got != before {
		t.Fatalf("plan after %s, want %s", got, before)
	}
	if ov, _ := overlayGet(); hasOv && ov != ov0 {
		t.Fatalf("power mode after %s, want %s", ov, ov0)
	}
}

// Delayed start end to end: a Run entry becomes a sign-in task with a
// delay, the page shows it once, and taking it back restores the entry.
func TestStartupDelay(t *testing.T) {
	changesThisPC(t)
	initPaths()
	logInit(t.TempDir())
	was := app.admin
	defer func() { app.admin = was }()
	app.admin = isElevated()
	if !app.admin {
		t.Skip("needs administrator rights")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "xki4o-delay-probe.exe")
	b, _ := os.ReadFile(sys32("ping.exe"))
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE) // (GitHub's runner has none)
	if err != nil {
		t.Fatal(err)
	}
	const name = "xKi4oDelayProbe"
	if err := k.SetStringValue(name, `"`+exe+`" -n 1 127.0.0.1`); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.DeleteValue(name); k.Close(); _ = os.Remove(delaysPath()) }()
	key := "reg|user|" + name
	find := func() (startupEntry, int) {
		n := 0
		var e startupEntry
		for _, x := range listStartup().Items {
			if x.Key == key {
				e = x
			}
			if strings.Contains(x.Key, "xKi4oDelayProbe") {
				n++
			}
		}
		return e, n
	}
	e, _ := find()
	t.Logf("entry %+v", e)
	if !e.Enabled || e.Kind != "app" || !e.Exists {
		t.Fatalf("entry %+v", e)
	}
	if r := startupDelay(key, 60); !r.OK {
		t.Fatalf("delay: %+v", r)
	}
	task := delayTaskName(name)
	if _, code, _ := runQuiet(30*time.Second, sys32("schtasks.exe"), "/Query", "/TN", task); code != 0 {
		t.Fatal("no task")
	}
	out, _, _ := runQuiet(30*time.Second, sys32("schtasks.exe"), "/Query", "/TN", task, "/XML")
	// Windows writes 60 s as PT1M, and leaves out the run level when it is the lowest (LeastPrivilege)
	if !(strings.Contains(out, "PT60S") || strings.Contains(out, "PT1M")) || strings.Contains(out, "HighestAvailable") {
		t.Fatalf("task xml %s", out)
	}
	e, n := find()
	if e.Delayed != 60 || !e.Enabled || n != 1 {
		t.Fatalf("shown %+v (%d entries)", e, n)
	}
	if on, _, _, _ := approvedState(registry.CURRENT_USER, `Run`, name); on {
		t.Fatal("the entry itself stayed on")
	}
	if r := startupDelay(key, 0); !r.OK {
		t.Fatalf("undo: %+v", r)
	}
	if _, code, _ := runQuiet(30*time.Second, sys32("schtasks.exe"), "/Query", "/TN", task); code == 0 {
		t.Fatal("the task stayed")
	}
	if e, _ := find(); e.Delayed != 0 || !e.Enabled {
		t.Fatalf("after undo %+v", e)
	}
}

// The app's modes set Windows' power plan (or power mode).
func TestPowerProfile(t *testing.T) {
	changesThisPC(t)
	initPaths()
	logInit(t.TempDir())
	orig := activeScheme()
	defer func() { _ = runTool(sys32("powercfg.exe"), "-setactive", orig) }()
	out, _, _ := runQuiet(20*time.Second, sys32("powercfg.exe"), "/list")
	t.Logf("plans:\n%s", out)
	r := powerProfile("games")
	t.Logf("games %+v", r)
	if !r.OK || r.Plan == balancedPlan && !strings.Contains(strings.ToLower(out), highPerfPlan) && r.Mode != bestPerfOverlay {
		if !r.OK || (r.Plan != highPerfPlan && r.Plan != ultimateOurs && r.Plan != ultimateStdPlan && r.Mode != bestPerfOverlay) {
			t.Fatalf("games %+v", r)
		}
	}
	if r := powerProfile("balanced"); !r.OK || r.Plan != balancedPlan {
		t.Fatalf("balanced %+v", r)
	}
	if strings.Contains(strings.ToLower(out), powerSaverPlan) {
		if r := powerProfile("saver"); !r.OK || r.Plan != powerSaverPlan {
			t.Fatalf("saver %+v", r)
		}
	}
	if r := powerProfile("nope"); r.OK {
		t.Fatal("an unknown mode")
	}
}

// The real app on this PC: started with Windows (--tray) or with "start in
// the tray" on, its window stays hidden even after the page has loaded; a
// plain start shows it.
func TestStartsInTray(t *testing.T) {
	changesThisPC(t)
	if os.Getenv("XKI4O_APP_TESTS") != "1" {
		t.Skip("starts the whole app: only with XKI4O_APP_TESTS=1")
	}
	exe := filepath.Join(t.TempDir(), "xKi4oCleaner.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	data := t.TempDir()
	cls, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString(appName)
	run := func(settings string, args ...string) (found, visible bool) {
		_ = os.MkdirAll(filepath.Join(data, "xKi4oCleaner"), 0o755)
		_ = os.WriteFile(filepath.Join(data, "xKi4oCleaner", "settings.json"), []byte(settings), 0o644)
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), "LOCALAPPDATA="+data)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); time.Sleep(time.Second) }()
		var h uintptr
		for i := 0; i < 60 && h == 0; i++ {
			time.Sleep(500 * time.Millisecond)
			h, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
		}
		if h == 0 {
			return false, false
		}
		time.Sleep(12 * time.Second) // the page loads and says it is ready
		v, _, _ := procIsWindowVisible.Call(h)
		return true, v != 0
	}
	if found, vis := run(`{"lang":"en"}`, "--tray"); !found || vis {
		t.Errorf("started with Windows: window found %v, visible %v", found, vis)
	}
	if found, vis := run(`{"lang":"en","startTray":true}`); !found || vis {
		t.Errorf("start in the tray on: window found %v, visible %v", found, vis)
	}
	if found, vis := run(`{"lang":"en"}`); !found || !vis {
		t.Errorf("a plain start: window found %v, visible %v", found, vis)
	}
}
