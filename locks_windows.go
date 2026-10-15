//go:build windows

package main

// Finding the programs that block cleaning, and closing them politely.
//
//   - A program is listed only when it is confirmed: either it is the app of
//     a selected category and it is running right now, or Windows' Restart
//     Manager reports that it holds one of the files open.
//   - Closing uses the Restart Manager (the mechanism installers use): the
//     app gets the normal "please close" messages and can save its work or
//     refuse. Nothing is ended by force unless the user confirms it.
//   - Windows components, services, Explorer, security software and other
//     users' programs are never offered for closing.

import (
	"errors"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmShutdown          = rstrtmgr.NewProc("RmShutdown")
	procRmRestart           = rstrtmgr.NewProc("RmRestart")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

const (
	rmService  = 3
	rmExplorer = 4
	rmCritical = 1000

	errMoreData = 234
)

type rmUniqueProcess struct {
	pid       uint32
	startTime windows.Filetime
}

// RM_PROCESS_INFO: 12 + 2*256 + 2*64 + 4*4 = 668 bytes.
type rmProcessInfo struct {
	process     rmUniqueProcess
	appName     [256]uint16
	serviceName [64]uint16
	appType     uint32
	appStatus   uint32
	tsSessionID uint32
	restartable int32
}

var _ = [1]struct{}{}[unsafe.Sizeof(rmProcessInfo{})-668]

func rmStart() (uint32, error) {
	if err := procRmStartSession.Find(); err != nil {
		return 0, err
	}
	var h uint32
	key := make([]uint16, 33) // CCH_RM_SESSION_KEY + 1
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&h)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return 0, syscall.Errno(r)
	}
	return h, nil
}

func rmEnd(h uint32) { _, _, _ = procRmEndSession.Call(uintptr(h)) }

func rmList(h uint32) ([]rmProcessInfo, error) {
	var needed, n, reasons uint32
	for i := 0; i < 4; i++ {
		buf := make([]rmProcessInfo, needed+1)
		n = uint32(len(buf))
		r, _, _ := procRmGetList.Call(uintptr(h), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&n)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&reasons)))
		if r == 0 {
			return buf[:n], nil
		}
		if r != errMoreData {
			return nil, syscall.Errno(r)
		}
	}
	return nil, errors.New("restart manager: list keeps growing")
}

// lockHolders asks Windows which processes hold the file open.
func lockHolders(path string) ([]rmProcessInfo, error) {
	h, err := rmStart()
	if err != nil {
		return nil, err
	}
	defer rmEnd(h)
	u, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	ptrs := []*uint16{u}
	r, _, _ := procRmRegisterResources.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&ptrs[0])), 0, 0, 0, 0)
	runtime.KeepAlive(ptrs)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return rmList(h)
}

// ---------------------------------------------------------------- processes

// processStart returns the start time of a running process (used together
// with the PID, so a PID that Windows reused is never mistaken for it).
func processStart(pid uint32) (windows.Filetime, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return windows.Filetime{}, false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) == nil && code != 259 { // not STILL_ACTIVE
		return windows.Filetime{}, false
	}
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return windows.Filetime{}, false
	}
	return c, true
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// Windows itself: never offered for closing.
var windowsProcs = map[string]bool{
	"system": true, "registry": true, "smss.exe": true, "csrss.exe": true, "wininit.exe": true, "winlogon.exe": true,
	"services.exe": true, "lsass.exe": true, "lsaiso.exe": true, "svchost.exe": true, "explorer.exe": true, "dwm.exe": true,
	"sihost.exe": true, "fontdrvhost.exe": true, "ctfmon.exe": true, "taskhostw.exe": true, "runtimebroker.exe": true,
	"searchindexer.exe": true, "searchhost.exe": true, "searchapp.exe": true, "searchprotocolhost.exe": true, "searchfilterhost.exe": true,
	"startmenuexperiencehost.exe": true, "shellexperiencehost.exe": true, "textinputhost.exe": true, "lockapp.exe": true,
	"audiodg.exe": true, "conhost.exe": true, "spoolsv.exe": true, "wmiprvse.exe": true, "dllhost.exe": true,
	"trustedinstaller.exe": true, "tiworker.exe": true, "memory compression": true, "secure system": true,
	"wudfhost.exe": true, "msiexec.exe": true, "msedgewebview2.exe": true, "applicationframehost.exe": true,
	"systemsettings.exe": true, "userinit.exe": true, "smartscreen.exe": true, "wlanext.exe": true, "dashost.exe": true,
	"mousocoreworker.exe": true, "usoclient.exe": true, "wuauclt.exe": true, "compattelrunner.exe": true,
	"crossdeviceresume.exe": true, "shellhost.exe": true, "widgets.exe": true, "widgetservice.exe": true,
	"useroobebroker.exe": true, "smss64.exe": true, "searchui.exe": true, "lsm.exe": true, "wermgr.exe": true,
}

// windowsComponent: a part of Windows itself. It runs from the Windows
// folder (System32, SystemApps, ...) or its version info names Windows as
// the product. Such processes are never offered for closing: Windows needs
// them or starts them again at once.
func windowsComponent(path, exe string) bool {
	if windowsProcs[exe] {
		return true
	}
	if path == "" {
		return false
	}
	if underWindowsDir(path) {
		return true
	}
	return isWindowsProduct(fileVersionStrings(path, "ProductName")["ProductName"])
}

// isWindowsProduct: "Microsoft® Windows® Operating System" and its spellings.
func isWindowsProduct(name string) bool {
	n := strings.NewReplacer("®", "", "(r)", "", "\u00a0", " ").Replace(strings.ToLower(name))
	return strings.Contains(strings.Join(strings.Fields(n), " "), "microsoft windows operating system")
}

// Security software: never offered for closing.
var securityProcs = map[string]bool{
	"msmpeng.exe": true, "nissrv.exe": true, "mpdefendercoreservice.exe": true, "securityhealthservice.exe": true,
	"securityhealthsystray.exe": true, "mpcmdrun.exe": true, "msseces.exe": true,
	"avp.exe": true, "avpui.exe": true, "ekrn.exe": true, "egui.exe": true, "bdagent.exe": true, "vsserv.exe": true,
	"bdservicehost.exe": true, "avastsvc.exe": true, "avastui.exe": true, "avgsvc.exe": true, "avgui.exe": true,
	"nortonsecurity.exe": true, "ns.exe": true, "norton.exe": true, "mcshield.exe": true, "mfemms.exe": true, "mcuicnt.exe": true,
	"mbam.exe": true, "mbamservice.exe": true, "mbamtray.exe": true, "savservice.exe": true, "sophosui.exe": true,
	"avguard.exe": true, "avgnt.exe": true, "fsorsp.exe": true, "fshoster32.exe": true, "psanhost.exe": true, "wrsa.exe": true,
	"cis.exe": true, "cmdagent.exe": true, "360tray.exe": true, "zhudongfangyu.exe": true, "qhsafetray.exe": true,
}

// ---------------------------------------------------------------- blocker check

type blockerApp struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Exe      string   `json:"exe"`
	Path     string   `json:"path,omitempty"`
	PIDs     []uint32 `json:"pids"`
	Items    []string `json:"items"`
	Files    int      `json:"files"`   // files in use by it, confirmed by Windows
	Bytes    int64    `json:"bytes"`   // their size
	Running  bool     `json:"running"` // it is the app of a selected category and it runs
	Closable bool     `json:"closable"`
	Why      string   `json:"why,omitempty"` // why it cannot be offered for closing
	Kind     string   `json:"kind"`          // app, background, service, windows, security, other-user
	Graceful bool     `json:"graceful"`      // has a window that can be asked to close
}

type lockedInfo struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

type blockerReport struct {
	Apps    []blockerApp          `json:"apps"`
	Locked  map[string]lockedInfo `json:"locked"`  // category -> files in use found
	Probed  int                   `json:"probed"`  // files checked
	Partial bool                  `json:"partial"` // not every file could be checked in time
}

var (
	blockMu   sync.Mutex
	lastBlock = map[string]*blockerApp{} // key -> app, from the latest check
	blockPIDs = map[uint32]windows.Filetime{}
)

type lockedFile struct {
	path string
	size int64
}

// checkBlockers looks for programs that stop the selected categories from
// being cleaned. It changes nothing.
func checkBlockers(ids []string) blockerReport {
	rep := blockerReport{Locked: map[string]lockedInfo{}}
	apps := map[string]*blockerApp{}
	selfPID := windows.GetCurrentProcessId()
	var mySession uint32
	_ = windows.ProcessIdToSessionId(selfPID, &mySession)

	all := map[string][]uint32{} // running processes by image name
	if snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0); err == nil {
		var pe windows.ProcessEntry32
		pe.Size = uint32(unsafe.Sizeof(pe))
		for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
			n := strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))
			all[n] = append(all[n], pe.ProcessID)
		}
		windows.CloseHandle(snap)
	}

	get := func(exe, name string) *blockerApp {
		key := "exe:" + exe
		a := apps[key]
		if a == nil {
			a = &blockerApp{Key: key, Exe: exe, Name: name}
			apps[key] = a
		}
		return a
	}
	addItem := func(a *blockerApp, id string) {
		if !containsStr(a.Items, id) {
			a.Items = append(a.Items, id)
		}
	}

	deadline := time.Now().Add(4 * time.Second) // keep the check quick: locked files are skipped safely anyway
	for _, id := range ids {
		it := findItem(id)
		if it == nil {
			continue
		}
		// 1. the category's own app is running
		running := false
		for _, p := range it.procs {
			for _, pid := range all[p] {
				if pid == selfPID {
					continue
				}
				a := get(p, "")
				a.Running = true
				if !containsPID(a.PIDs, pid) {
					a.PIDs = append(a.PIDs, pid)
				}
				addItem(a, id)
				running = true
			}
		}
		if running || it.targets == nil || it.clean != nil || time.Now().After(deadline) {
			if time.Now().After(deadline) {
				rep.Partial = true
			}
			continue
		}
		// 2. files that another program has open (checked without deleting)
		until := time.Now().Add(900 * time.Millisecond)
		if deadline.Before(until) {
			until = deadline
		}
		locked, probed, full := probeLocked(it.targets(), 1500, until)
		rep.Probed += probed
		if !full {
			rep.Partial = true
		}
		if len(locked) == 0 {
			continue
		}
		var li lockedInfo
		for _, f := range locked {
			li.Files++
			li.Bytes += f.size
		}
		rep.Locked[id] = li
		// who holds them: asked file by file, so every count is confirmed
		for i, f := range locked {
			if i >= 24 || time.Now().After(deadline.Add(2*time.Second)) {
				rep.Partial = true
				break
			}
			holders, err := lockHolders(f.path)
			if err != nil {
				continue
			}
			for _, h := range holders {
				pid := h.process.pid
				if pid == selfPID {
					continue
				}
				exe := strings.ToLower(filepath.Base(processPath(pid)))
				if exe == "" || exe == "." {
					exe = strings.ToLower(windows.UTF16ToString(h.appName[:]))
				}
				a := get(exe, windows.UTF16ToString(h.appName[:]))
				if !containsPID(a.PIDs, pid) {
					a.PIDs = append(a.PIDs, pid)
				}
				a.Files++
				a.Bytes += f.size
				addItem(a, id)
				switch h.appType {
				case rmService:
					a.Kind = "service"
				case rmCritical, rmExplorer:
					a.Kind = "windows"
				}
			}
		}
	}

	blockMu.Lock()
	defer blockMu.Unlock()
	lastBlock = map[string]*blockerApp{}
	blockPIDs = map[uint32]windows.Filetime{}
	for _, a := range apps {
		if len(a.PIDs) == 0 {
			continue
		}
		a.Path = processPath(a.PIDs[0])
		if a.Name == "" || strings.EqualFold(a.Name, a.Exe) {
			a.Name = friendlyName(a.Path, a.Exe)
		}
		var session uint32
		_ = windows.ProcessIdToSessionId(a.PIDs[0], &session)
		a.Graceful = hasVisibleWindow(a.PIDs)
		switch {
		case securityProcs[a.Exe]:
			a.Kind, a.Why = "security", T("blk.security")
		case a.Kind == "windows" || windowsComponent(a.Path, a.Exe):
			a.Kind, a.Why = "windows", T("blk.windows")
		case a.Kind == "service":
			a.Why = T("blk.service")
		case session != mySession:
			a.Kind, a.Why = "other-user", T("blk.session")
		default:
			a.Closable = true
			if !a.Graceful && !a.Running {
				a.Kind = "background"
			} else {
				a.Kind = "app"
			}
		}
		if a.Closable {
			n := 0
			for _, pid := range a.PIDs {
				if st, ok := processStart(pid); ok {
					blockPIDs[pid] = st
					n++
				}
			}
			if n == 0 { // e.g. it runs as administrator and this app does not
				a.Closable, a.Why = false, T("blk.access")
			}
		}
		lastBlock[a.Key] = a
		rep.Apps = append(rep.Apps, *a)
	}
	sort.Slice(rep.Apps, func(i, j int) bool {
		x, y := rep.Apps[i], rep.Apps[j]
		if x.Closable != y.Closable {
			return x.Closable
		}
		if x.Running != y.Running {
			return x.Running
		}
		return strings.ToLower(x.Name) < strings.ToLower(y.Name)
	})
	return rep
}

func containsPID(s []uint32, v uint32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// probeLocked walks the planned files and returns those another program has
// open. It stops at max files or at the deadline (full = false).
func probeLocked(ts []target, max int, deadline time.Time) (locked []lockedFile, probed int, full bool) {
	now := time.Now()
	var walk func(p planned) bool
	walk = func(p planned) bool {
		if probed >= max || time.Now().After(deadline) {
			return false
		}
		if isLink(p.info) {
			return true
		}
		if p.info.IsDir() {
			for _, e := range readDir(p.path) {
				if !walk(e) {
					return false
				}
			}
			return true
		}
		probed++
		if probeDelete(p.path) == "locked" && len(locked) < 500 {
			locked = append(locked, lockedFile{p.path, p.info.Size()})
		}
		return true
	}
	for _, t := range ts {
		for _, p := range t.plan(now) {
			if !walk(p) {
				return locked, probed, false
			}
		}
	}
	return locked, probed, true
}

// friendlyName reads the program's description from its version info.
func friendlyName(path, exe string) string {
	if path != "" {
		v := fileVersionStrings(path, "FileDescription", "ProductName")
		if d := v["FileDescription"]; d != "" {
			return d
		}
		if d := v["ProductName"]; d != "" {
			return d
		}
	}
	return strings.TrimSuffix(exe, ".exe")
}

var (
	visMu      sync.Mutex
	visTargets map[uint32]bool
	visFound   bool
)

var visibleWindowCB = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil && visTargets[pid] && windows.IsWindowVisible(hwnd) {
		visFound = true
		return 0
	}
	return 1
})

func hasVisibleWindow(pids []uint32) bool {
	visMu.Lock()
	defer visMu.Unlock()
	visTargets = map[uint32]bool{}
	for _, p := range pids {
		visTargets[p] = true
	}
	visFound = false
	_ = windows.EnumWindows(visibleWindowCB, nil)
	visTargets = nil
	return visFound
}

// ---------------------------------------------------------------- closing

type closeResult struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Closed bool   `json:"closed"`
	Note   string `json:"note,omitempty"`
}

var (
	restartMu      sync.Mutex
	restartSession uint32 // Restart Manager session kept to reopen apps
)

// alive returns the processes of the list that still run (same start time,
// so a process ID that Windows reused is never touched).
func alive(pids []uint32) []uint32 {
	var out []uint32
	for _, pid := range pids {
		want, ok := blockPIDs[pid]
		if !ok {
			continue
		}
		if st, ok := processStart(pid); ok && st == want {
			out = append(out, pid)
		}
	}
	return out
}

// closeBlockers asks the chosen apps to close normally and waits for them.
// An app may refuse (for example to ask about unsaved work): it then stays
// open and is reported as such.
func closeBlockers(keys []string, reopen bool) []closeResult {
	blockMu.Lock()
	defer blockMu.Unlock()
	var rmApps []rmUniqueProcess
	var plain []*blockerApp
	for _, k := range keys {
		a := lastBlock[k]
		if a == nil || !a.Closable {
			continue
		}
		pids := alive(a.PIDs)
		if len(pids) == 0 {
			continue
		}
		if a.Exe == "steam.exe" {
			// Steam ignores the normal close message; this is its own clean exit.
			if d := steamDir(); d != "" && fileExists(j(d, "steam.exe")) {
				_, _, _ = runQuiet(5*time.Second, j(d, "steam.exe"), "-shutdown")
				continue
			}
		}
		for _, pid := range pids {
			rmApps = append(rmApps, rmUniqueProcess{pid: pid, startTime: blockPIDs[pid]})
		}
		plain = append(plain, a)
	}
	var session uint32
	if len(rmApps) > 0 {
		if h, err := rmStart(); err == nil {
			r, _, _ := procRmRegisterResources.Call(uintptr(h), 0, 0, uintptr(len(rmApps)), uintptr(unsafe.Pointer(&rmApps[0])), 0, 0)
			if r == 0 {
				_, _, _ = procRmShutdown.Call(uintptr(h), 0, 0) // no force flag: apps may refuse
				session = h
			} else {
				rmEnd(h)
			}
		}
		if session == 0 {
			// Restart Manager unavailable: send the normal close message.
			targets := map[uint32]string{}
			for _, a := range plain {
				for _, pid := range alive(a.PIDs) {
					targets[pid] = a.Exe
				}
			}
			postClose(targets)
		}
	}
	// give them time to finish closing (saving, shutting down helpers)
	for i := 0; i < 40; i++ {
		still := false
		for _, k := range keys {
			if a := lastBlock[k]; a != nil && a.Closable && len(alive(a.PIDs)) > 0 {
				still = true
				break
			}
		}
		if !still {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	var res []closeResult
	for _, k := range keys {
		a := lastBlock[k]
		if a == nil {
			continue
		}
		r := closeResult{Key: k, Name: a.Name}
		switch {
		case !a.Closable:
			r.Note = a.Why
		case len(alive(a.PIDs)) == 0:
			r.Closed = true
		default:
			r.Note = T("blk.stillOpen")
		}
		res = append(res, r)
	}
	if session != 0 {
		restartMu.Lock()
		if restartSession != 0 {
			rmEnd(restartSession)
		}
		restartSession = 0
		if reopen {
			restartSession = session
		} else {
			rmEnd(session)
		}
		restartMu.Unlock()
	}
	return res
}

// forceCloseBlockers ends the chosen apps at once; unsaved work in them is
// lost. The page says so: with the "close by force" setting (the default)
// it is the normal way to close apps before cleaning, otherwise it asks
// for a confirmation first. Windows components, security software and other
// users' programs are never closable, so they never get here.
func forceCloseBlockers(keys []string) []closeResult {
	blockMu.Lock()
	defer blockMu.Unlock()
	var res []closeResult
	for _, k := range keys {
		a := lastBlock[k]
		if a == nil || !a.Closable {
			continue
		}
		for _, pid := range alive(a.PIDs) {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid); err == nil {
				_ = windows.TerminateProcess(h, 1)
				_, _ = windows.WaitForSingleObject(h, 3000)
				windows.CloseHandle(h)
			}
		}
		r := closeResult{Key: k, Name: a.Name, Closed: len(alive(a.PIDs)) == 0}
		if !r.Closed {
			r.Note = T("blk.stillOpen")
		}
		res = append(res, r)
	}
	return res
}

// finishClosedApps ends the Restart Manager session of the last closing.
// With reopen, the apps that told Windows how to restart themselves
// (browsers do) are started again first.
func finishClosedApps(reopen bool) {
	restartMu.Lock()
	h := restartSession
	restartSession = 0
	restartMu.Unlock()
	if h == 0 {
		return
	}
	if reopen {
		_, _, _ = procRmRestart.Call(uintptr(h), 0, 0)
	}
	rmEnd(h)
}
