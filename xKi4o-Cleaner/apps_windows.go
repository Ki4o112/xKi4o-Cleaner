//go:build windows

package main

// Closing apps whose cache is locked while they run (browsers, Discord,
// Steam, ...). Each app is first asked to close normally; apps that stay
// running (many keep a tray icon) are ended after a short wait.

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procPIDs returns the running processes whose image name is in names
// (lower case), as pid -> name.
func procPIDs(names map[string]bool) map[uint32]string {
	out := map[uint32]string{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)
	self := windows.GetCurrentProcessId()
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		n := strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))
		if names[n] && pe.ProcessID != self {
			out[pe.ProcessID] = n
		}
	}
	return out
}

var (
	closeMu      sync.Mutex
	closeTargets map[uint32]string
)

var enumWindowsCB = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil {
		if _, ok := closeTargets[pid]; ok && windows.IsWindowVisible(hwnd) {
			_, _, _ = procPostMessageW.Call(uintptr(hwnd), 0x0010, 0, 0) // WM_CLOSE
		}
	}
	return 1
})

// closeProcesses asks the matching apps to close, waits up to grace, then
// ends whatever is still running. Never touches this app or the shell.
func closeProcesses(names map[string]bool, grace time.Duration) {
	delete(names, "explorer.exe")
	delete(names, "msedgewebview2.exe")
	pids := procPIDs(names)
	if len(pids) == 0 {
		return
	}
	closeMu.Lock()
	closeTargets = pids
	_ = windows.EnumWindows(enumWindowsCB, nil)
	closeTargets = nil
	closeMu.Unlock()
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if len(procPIDs(names)) == 0 {
			return
		}
	}
	for pid := range procPIDs(names) {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 0)
		_, _ = windows.WaitForSingleObject(h, 3000)
		windows.CloseHandle(h)
	}
}

// closeApps closes the apps of the given cleaning categories and reports
// which ones are closed now.
func closeApps(ids []string) map[string]bool {
	names := map[string]bool{}
	steam := false
	for _, id := range ids {
		it := findItem(id)
		if it == nil {
			continue
		}
		for _, p := range it.procs {
			names[p] = true
			steam = steam || p == "steam.exe"
		}
	}
	if steam && len(procPIDs(map[string]bool{"steam.exe": true})) > 0 {
		// Steam's own clean shutdown (it ignores a normal close).
		if d := steamDir(); d != "" && fileExists(j(d, "steam.exe")) {
			_, _, _ = runQuiet(5*time.Second, j(d, "steam.exe"), "-shutdown")
			for i := 0; i < 32 && len(procPIDs(map[string]bool{"steam.exe": true})) > 0; i++ {
				time.Sleep(250 * time.Millisecond)
			}
		}
	}
	closeProcesses(names, 4*time.Second)
	running := runningProcs()
	out := map[string]bool{}
	for _, id := range ids {
		if it := findItem(id); it != nil {
			out[id] = !it.runningIn(running)
		}
	}
	return out
}
