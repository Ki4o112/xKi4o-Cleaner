//go:build windows

package main

// Asking Windows itself where a shortcut leads: the way Explorer does when
// the shortcut is opened (it follows a target that was moved and searches
// for it), without any window and without changing the shortcut.

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")

	clsidShellLink = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// comCall calls method idx of a COM interface (obj is the interface pointer).
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(osPointer(obj))
	fn := *(*uintptr)(osPointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

// shellResolve: where Windows finds the shortcut's target (ok: a file or
// folder that exists). Read-only: the shortcut is not updated.
func shellResolve(lnk string, timeout time.Duration) (target string, ok bool) {
	target, ok, _ = shellResolveWhy(lnk, timeout)
	return target, ok
}

// shellResolveWhy is shellResolve that also says which step failed (for
// the log and the tests).
func shellResolveWhy(lnk string, timeout time.Duration) (target string, ok bool, why string) {
	p, err := windows.UTF16PtrFromString(lnk)
	if err != nil {
		return "", false, "name"
	}
	why = "com"
	withCOM(func() {
		var psl uintptr
		const clsctxInprocServer = 1
		if r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(&psl))); r != 0 || psl == 0 {
			why = fmt.Sprintf("CoCreateInstance %#x", uint32(r))
			return
		}
		defer comCall(psl, 2) // Release
		var ppf uintptr
		if r := comCall(psl, 0, uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&ppf))); r != 0 || ppf == 0 { // QueryInterface
			why = fmt.Sprintf("QueryInterface %#x", uint32(r))
			return
		}
		defer comCall(ppf, 2)
		if r := comCall(ppf, 5, uintptr(unsafe.Pointer(p)), 0); r != 0 { // IPersistFile.Load, STGM_READ
			why = fmt.Sprintf("Load %#x", uint32(r))
			return
		}
		ms := uintptr(timeout / time.Millisecond)
		ms = max(min(ms, 0xFFFF), 1)
		const slrNoUI, slrNoUpdate = 0x1, 0x8                             // no window; the shortcut stays as it is
		if r := comCall(psl, 19, 0, slrNoUI|slrNoUpdate|ms<<16); r != 0 { // IShellLinkW.Resolve
			why = fmt.Sprintf("Resolve %#x", uint32(r))
			return
		}
		buf := make([]uint16, 1024)
		if r := comCall(psl, 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); r != 0 { // GetPath
			why = fmt.Sprintf("GetPath %#x", uint32(r))
			return
		}
		target = windows.UTF16ToString(buf)
		why = "not there"
		if target != "" {
			if _, err := os.Stat(target); err == nil {
				ok, why = true, ""
			}
		}
	})
	return target, ok, why
}
