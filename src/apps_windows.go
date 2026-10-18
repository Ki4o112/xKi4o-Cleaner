//go:build windows

package main

// Asking apps to close the normal way (the same message as clicking the
// window's X). Nothing here ends a process by force.

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procPIDs returns the running processes whose image name is in names
// (lower case), as pid -> name. This app itself is never included.
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

// postClose sends WM_CLOSE to the visible windows of the processes.
func postClose(pids map[uint32]string) {
	if len(pids) == 0 {
		return
	}
	closeMu.Lock()
	closeTargets = pids
	_ = windows.EnumWindows(enumWindowsCB, nil)
	closeTargets = nil
	closeMu.Unlock()
}

// closeProcesses asks the matching apps to close and waits up to grace.
// It reports whether none of them is running any more. Apps that stay open
// (for example to ask about unsaved work) are left alone. Never touches
// this app or the shell.
func closeProcesses(names map[string]bool, grace time.Duration) bool {
	delete(names, "explorer.exe")
	delete(names, "msedgewebview2.exe")
	pids := procPIDs(names)
	if len(pids) == 0 {
		return true
	}
	postClose(pids)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if len(procPIDs(names)) == 0 {
			return true
		}
	}
	return len(procPIDs(names)) == 0
}
