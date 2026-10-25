//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetShellWindow          = windows.NewLazySystemDLL("user32.dll").NewProc("GetShellWindow")
	procCreateProcessWithTokenW = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")
)

// processCommandLine reads a process's command line (Windows 8.1 and later).
func processCommandLine(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint64, 8<<10) // 64 KB, aligned
	var n uint32
	st, _, _ := procNtQueryInformationProcess.Call(uintptr(h), 60, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*8), uintptr(unsafe.Pointer(&n)))
	if st != 0 {
		return ""
	}
	us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil || us.Length == 0 {
		return ""
	}
	return string(utf16.Decode(unsafe.Slice(us.Buffer, us.Length/2)))
}

// startAsUser starts a program as the signed-in user without administrator
// rights (this app runs elevated; programs it starts must not). It uses
// the token of the desktop shell (Explorer).
func startAsUser(exe, cmdline string) error {
	h, err := startAsUserProc(exe, cmdline, 0)
	if err == nil {
		windows.CloseHandle(h)
	}
	return err
}

// startAsUserProc is startAsUser that returns the process (the caller closes it).
func startAsUserProc(exe, cmdline string, flags uint32) (windows.Handle, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return startPlain(exe, cmdline, flags) // already the user's own rights
	}
	hwnd, _, _ := procGetShellWindow.Call()
	if hwnd == 0 {
		return 0, errors.New("no desktop shell")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid); err != nil || pid == 0 {
		return 0, errors.New("no desktop shell")
	}
	hp, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(hp)
	var tok windows.Token
	if err := windows.OpenProcessToken(hp, windows.TOKEN_DUPLICATE, &tok); err != nil {
		return 0, err
	}
	defer tok.Close()
	var prim windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_SESSIONID,
		nil, windows.SecurityImpersonation, windows.TokenPrimary, &prim); err != nil {
		return 0, err
	}
	defer prim.Close()
	enablePrivilege("SeImpersonatePrivilege")
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	app, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	cl, err := windows.UTF16FromString(cmdline)
	if err != nil {
		return 0, err
	}
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	const createUnicodeEnvironment = 0x400
	r, _, e := procCreateProcessWithTokenW.Call(uintptr(prim), 0, uintptr(unsafe.Pointer(app)), uintptr(unsafe.Pointer(&cl[0])),
		uintptr(createUnicodeEnvironment|flags), 0, uintptr(unsafe.Pointer(dir)), uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)))
	if r == 0 {
		return 0, e
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

// startAgain starts a closed program again as the user. When that is not
// possible, Explorer starts it (as the user, but without its arguments).
func startAgain(c gmClosed) (plain bool, err error) {
	if err = startAsUser(c.Path, gmCommand(c)); err == nil {
		return false, nil
	}
	if e2 := exec.CommandContext(context.Background(), filepath.Join(windowsDir(), "explorer.exe"), c.Path).Start(); e2 != nil { //nolint:gosec // a program the user chose, at the path it ran from
		return false, err
	}
	return true, nil
}

// startPlain starts a program with this process's own rights.
func startPlain(exe, cmdline string, flags uint32) (windows.Handle, error) {
	app, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	cl, err := windows.UTF16FromString(cmdline)
	if err != nil {
		return 0, err
	}
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, &cl[0], nil, nil, false, flags, nil, dir, &si, &pi); err != nil {
		return 0, err
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

// gmSkip: never offered for closing in game mode: this app (any copy of
// it), net-merge, setups running from Temp and other non-programs (.tmp).
func gmSkip(exe, path string) bool {
	// net-merge carries the network: closing it would cut every connection
	if exe == "net-merge.exe" || exe == "net-merge-tray.exe" {
		return true
	}
	if strings.Contains(exe, "xki4ocleaner") || !strings.HasSuffix(exe, ".exe") {
		return true
	}
	if path != "" {
		for _, t := range []string{P.temp, os.Getenv("TEMP"), filepath.Join(P.local, "Temp")} {
			if t != "" && under(path, t) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- the programs that can be closed

type gmApp struct {
	Exe     string   `json:"exe"`
	Name    string   `json:"name"`
	Path    string   `json:"path,omitempty"`
	Mem     uint64   `json:"mem"`
	Count   int      `json:"count"`
	Window  bool     `json:"window"`
	Running bool     `json:"running"`
	Helpers []string `json:"helpers,omitempty"` // its helper programs, counted with it (they close with it)
}

// gameApps lists the user's running programs that game mode may close
// (never Windows, security software, services or other users' programs).
func gameApps() []gmApp {
	v := procList()
	var rows []procRow
	var gr []gmRow
	for _, r := range v.Rows {
		if !r.Closable || (r.Kind != "app" && r.Kind != "background") {
			continue
		}
		exe := strings.ToLower(r.Name)
		if windowsComponent(r.Path, exe) || securityProcs[exe] || gmSkip(exe, r.Path) {
			continue
		}
		rows = append(rows, r)
		gr = append(gr, gmRow{PID: r.PID, PPID: r.PPID, Start: r.Start, Exe: exe, Path: r.Path})
	}
	helpers := gmHelpers(gr, gmGenericDir)
	by := map[string]*gmApp{}
	for _, r := range rows {
		exe := strings.ToLower(r.Name)
		if root, ok := helpers[exe]; ok {
			exe = root // counted with the program it belongs to
		}
		a := by[exe]
		if a == nil {
			a = &gmApp{Exe: exe, Running: true}
			by[exe] = a
		}
		if a.Name == "" && strings.EqualFold(r.Name, exe) { // the name and path of the program itself, not of a helper
			a.Path = r.Path
			a.Name = r.Desc
			if a.Name == "" {
				a.Name = strings.TrimSuffix(r.Name, filepath.Ext(r.Name))
			}
		}
		if !strings.EqualFold(r.Name, exe) && !slices.Contains(a.Helpers, strings.ToLower(r.Name)) {
			a.Helpers = append(a.Helpers, strings.ToLower(r.Name))
		}
		a.Mem += r.Mem
		a.Count++
		a.Window = a.Window || r.Window
	}
	out := make([]gmApp, 0, len(by))
	for _, a := range by {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mem > out[j].Mem })
	return out
}

// gmGenericDir: a folder that holds many programs, not one program's own.
func gmGenericDir(dir string) bool {
	d := strings.TrimSuffix(lowerClean(dir), `\`)
	if len(d) <= 2 || protectedRoots[normPath(dir)] || underWindowsDir(dir+`\`) {
		return true
	}
	for _, g := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), P.local, P.roaming, P.programData, P.profile,
		filepath.Join(P.local, "Programs"), P.temp, filepath.Join(P.profile, "Desktop"), filepath.Join(P.profile, "Downloads")} {
		if g != "" && strings.EqualFold(strings.TrimSuffix(lowerClean(g), `\`), d) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- state (kept on disk: a restart of this app does not lose it)

var gm struct {
	mu sync.Mutex
	st gmState
	ok bool
}

func gmStatePath() string { return dataDirPath("gamemode.json") }

func gmLoad() gmState {
	if !gm.ok {
		gm.ok = true
		if b, err := os.ReadFile(gmStatePath()); err == nil {
			_ = json.Unmarshal(b, &gm.st)
		}
	}
	return gm.st
}

func gmSave(st gmState) {
	gm.st, gm.ok = st, true
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(filepath.Dir(gmStatePath()), 0o755)
	_ = os.WriteFile(gmStatePath(), b, 0o644)
}

func gameState() gmState {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	return gmLoad()
}

type gmOpts struct {
	Apps  []string `json:"apps"`  // exe names
	Force bool     `json:"force"` // end the ones that do not close in time
	RAM   bool     `json:"ram"`
}

type gmResult struct {
	State gmState `json:"state"`
	Error string  `json:"error,omitempty"`
}

// gameStart closes the chosen programs (politely, then by force only if
// asked; programs with no window can only be ended), frees memory and
// remembers how to start them again.
func gameStart(o gmOpts) gmResult {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	st := gmLoad()
	if st.Active {
		return gmResult{State: st}
	}
	want := map[string]bool{}
	for _, a := range o.Apps {
		want[strings.ToLower(strings.TrimSpace(a))] = true
	}
	op := beginOp("game", "", T("log.gmStart"), map[string]any{"apps": o.Apps, "force": o.Force, "ram": o.RAM})
	allowed := map[string]bool{}
	for _, a := range gameApps() {
		if want[a.Exe] {
			allowed[a.Exe] = true
		}
	}
	raw, err := queryProcesses()
	if err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return gmResult{State: st, Error: err.Error()}
	}
	self := windows.GetCurrentProcessId()
	var mySession uint32
	_ = windows.ProcessIdToSessionId(self, &mySession)
	var procs []gmProc
	pids := map[uint32]string{}
	before := map[uint32]rawProc{}
	for _, p := range raw {
		exe := strings.ToLower(p.name)
		if !allowed[exe] || p.session != mySession || p.pid == self {
			continue
		}
		procs = append(procs, gmProc{PID: p.pid, PPID: p.ppid, Created: p.created, Exe: exe})
		pids[p.pid] = exe
		before[p.pid] = p
	}
	for i := range procs {
		procs[i].Path = processPath(procs[i].PID)
		procs[i].Cmd = processCommandLine(procs[i].PID)
	}
	st = gmState{Active: true, Since: time.Now().UnixMilli()}
	names := map[string]string{}
	for _, a := range gameApps() {
		names[a.Exe] = a.Name
	}
	for _, r := range gmRoots(procs, allowed) {
		if r.Path == "" {
			continue
		}
		st.Closed = append(st.Closed, gmClosed{Exe: r.Exe, Name: names[r.Exe], Path: r.Path, Cmd: r.Cmd})
	}
	gmSave(st) // first: if anything below fails, the programs can still be started again
	windowed := windowPIDs()
	polite, quiet := map[uint32]string{}, map[uint32]string{}
	for pid, exe := range pids {
		if windowed[pid] {
			polite[pid] = exe
		} else {
			quiet[pid] = exe
		}
	}
	left := endPIDs(polite, before, false, 8*time.Second)
	// helpers without a window close with their program; what is left of them is ended
	time.Sleep(time.Second)
	rest := stillRunning(quiet, before)
	toEnd := map[uint32]string{}
	for pid := range rest {
		if exe := quiet[pid]; !hasWindowedSibling(exe, polite, left) {
			toEnd[pid] = exe
		}
	}
	if o.Force {
		for pid := range left {
			toEnd[pid] = polite[pid]
		}
	}
	endPIDs(toEnd, before, true, 0)
	still := stillRunning(pids, before)
	leftNames := map[string]bool{}
	for pid := range still {
		leftNames[pids[pid]] = true
	}
	for exe := range leftNames {
		st.Left = append(st.Left, exe)
	}
	sort.Strings(st.Left)
	// a program that stayed open is not started again later
	var closed []gmClosed
	for _, c := range st.Closed {
		if !leftNames[c.Exe] {
			closed = append(closed, c)
		}
	}
	st.Closed = closed
	if o.RAM {
		res := cleanMemory(gameRAMRegions)
		st.Freed = res.Freed
		op.info("", T("log.gmRAM"), map[string]any{"freed": res.Freed})
	}
	gmSave(st)
	for _, c := range st.Closed {
		op.info(c.Name, T("log.gmClosed"), map[string]any{"exe": c.Exe})
	}
	for _, e := range st.Left {
		op.warnf(e, T("log.gmLeft"), nil)
	}
	op.end(T("log.gmOn", len(st.Closed)), nil)
	return gmResult{State: st}
}

// hasWindowedSibling: a windowless process of a program whose window is
// still open (refused to close) is left alone too.
func hasWindowedSibling(exe string, polite map[uint32]string, left map[uint32]bool) bool {
	for pid, e := range polite {
		if e == exe && left[pid] {
			return true
		}
	}
	return false
}

// gameStop ends game mode: the closed programs start again (as the user,
// without administrator rights), unless already running.
func gameStop() gmResult {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	st := gmLoad()
	if !st.Active {
		return gmResult{State: st}
	}
	op := beginOp("game", "", T("log.gmStop"), nil)
	running := map[string]bool{}
	if raw, err := queryProcesses(); err == nil {
		for _, p := range raw {
			running[strings.ToLower(p.name)] = true
		}
	}
	var failed []string
	for _, c := range gmToStart(st, running) {
		if !fileExists(c.Path) {
			op.warnf(c.Name, T("log.gmMissing"), map[string]any{"path": c.Path})
			continue
		}
		if plain, err := startAgain(c); err != nil {
			failed = append(failed, c.Name)
			op.fail(c.Name, err.Error(), nil)
			continue
		} else if plain {
			op.info(c.Name, T("log.gmStartedPlain"), nil)
			continue
		}
		op.info(c.Name, T("log.gmStarted"), nil)
	}
	gmSave(gmState{})
	op.end(T("log.gmOff"), nil)
	res := gmResult{State: gmState{}}
	if len(failed) > 0 {
		res.Error = T("gm.err.start", strings.Join(failed, ", "))
	}
	return res
}
