//go:build windows

package main

// Asking Windows itself where a shortcut leads: the way Explorer does when
// the shortcut is opened (it follows a target that was moved and searches
// for it), without any window and without changing the shortcut.

import (
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
	p, err := windows.UTF16PtrFromString(lnk)
	if err != nil {
		return "", false
	}
	withCOM(func() {
		var psl uintptr
		const clsctxInprocServer = 1
		if r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(&psl))); r != 0 || psl == 0 {
			return
		}
		defer comCall(psl, 2) // Release
		var ppf uintptr
		if comCall(psl, 0, uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&ppf))) != 0 || ppf == 0 { // QueryInterface
			return
		}
		defer comCall(ppf, 2)
		if comCall(ppf, 5, uintptr(unsafe.Pointer(p)), 0) != 0 { // IPersistFile.Load, STGM_READ
			return
		}
		ms := uintptr(timeout / time.Millisecond)
		ms = max(min(ms, 0xFFFF), 1)
		const slrNoUI, slrNoUpdate = 0x1, 0x8                     // no window; the shortcut stays as it is
		if comCall(psl, 19, 0, slrNoUI|slrNoUpdate|ms<<16) != 0 { // IShellLinkW.Resolve
			return
		}
		buf := make([]uint16, 1024)
		if comCall(psl, 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0) != 0 { // GetPath
			return
		}
		target = windows.UTF16ToString(buf)
		if target != "" {
			if _, err := os.Stat(target); err == nil {
				ok = true
			}
		}
	})
	return target, ok
}
