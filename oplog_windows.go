//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func logDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", "logs")
}

// logOpenFolder shows the log folder in Explorer.
func logOpenFolder() bool {
	d := olog.dir
	if d == "" {
		return false
	}
	_ = os.MkdirAll(d, 0o755)
	shellOpen(d)
	return true
}

var (
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
)

// copyText puts text on the clipboard (the page cannot always do it
// itself).
func copyText(s string) bool {
	u, err := windows.UTF16FromString(strings.ReplaceAll(s, "\x00", ""))
	if err != nil {
		return false
	}
	ok := false
	withCOM(func() {
		opened := false
		for i := 0; i < 10; i++ { // another program may hold the clipboard for a moment
			if r, _, _ := procOpenClipboard.Call(app.hwnd); r != 0 {
				opened = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !opened {
			return
		}
		defer procCloseClipboard.Call()
		if r, _, _ := procEmptyClipboard.Call(); r == 0 {
			return
		}
		size := uintptr(len(u) * 2)
		h, _, _ := procGlobalAlloc.Call(0x0002, size) // GMEM_MOVEABLE
		if h == 0 {
			return
		}
		p, _, _ := procGlobalLock.Call(h)
		if p == 0 {
			_, _, _ = procGlobalFree.Call(h)
			return
		}
		_, _, _ = procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
		_, _, _ = procGlobalUnlock.Call(h)
		if r, _, _ := procSetClipboardData.Call(13, h); r == 0 { // CF_UNICODETEXT
			_, _, _ = procGlobalFree.Call(h) // still ours when it was not taken
			return
		}
		ok = true
	})
	return ok
}

// saveTextFile asks where to save a text (a report) and writes it.
func saveTextFile(name, content string) profileResult {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, name)
	path, ok := fileDialog(true, T("log.saveTitle"), name, []string{T("log.txtFilter"), "*.txt"}, "txt")
	if !ok {
		return profileResult{}
	}
	// a BOM so that Notepad and others read the Bulgarian text right
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte(strings.ReplaceAll(content, "\n", "\r\n"))...), 0o644); err != nil {
		return profileResult{Error: err.Error()}
	}
	return profileResult{OK: true, Path: path}
}
