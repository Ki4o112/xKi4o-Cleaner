//go:build windows
// +build windows

package edge

import (
	"unsafe"

	"github.com/jchv/go-webview2/internal/w32"
)

func (e *Chromium) Resize() {
	if e.controller == nil {
		return
	}

	var bounds w32.Rect
	w32.User32GetClientRect.Call(e.hwnd, uintptr(unsafe.Pointer(&bounds)))
	if e.topInset > 0 && bounds.Bottom > bounds.Top+e.topInset {
		bounds.Top += e.topInset
	}

	words := (*[2]uintptr)(unsafe.Pointer(&bounds))
	e.controller.vtbl.PutBounds.Call(
		uintptr(unsafe.Pointer(e.controller)),
		words[0],
		words[1],
	)
}
