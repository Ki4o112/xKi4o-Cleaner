//go:build windows

package main

import (
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procClientToScreen   = user32.NewProc("ClientToScreen")
	procSendMessageW     = user32.NewProc("SendMessageW")
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

// testWindow: a window of the app's style whose messages go through the
// app's hook first, as the main window's do. Not shown.
func testWindow(t *testing.T) uintptr {
	t.Helper()
	runtime.LockOSThread() // a window answers on the thread that made it
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	name, _ := windows.UTF16PtrFromString("xKi4oTestFrame")
	wc := wndClassEx{
		WndProc: windows.NewCallback(func(h, m, wp, lp uintptr) uintptr {
			if r, ok := winHook(h, m, wp, lp); ok {
				return r
			}
			return defWindowProc(h, m, wp, lp)
		}),
		Instance:  hinst,
		ClassName: name,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	h, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0xCF0000, 100, 100, 800, 600, 0, 0, uintptr(hinst), 0)
	if h == 0 {
		t.Fatalf("window: %v", err)
	}
	t.Cleanup(func() {
		win.own.Store(false)
		win.inTray.Store(false)
		app.hwnd = 0
		_, _, _ = procDestroyWindow.Call(h)
		runtime.UnlockOSThread()
	})
	return h
}

// clientTop: how far below the window's top edge its client area starts.
func clientTop(h uintptr) int32 {
	var wr rect
	_, _, _ = procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	var pt point
	_, _, _ = procClientToScreen.Call(h, uintptr(unsafe.Pointer(&pt)))
	return pt.Y - wr.Top
}

func hitTest(h uintptr, x, y int32) uintptr {
	r, _, _ := procSendMessageW.Call(h, wmNCHitTest, 0, uintptr(uint16(x))|uintptr(uint16(y))<<16)
	return r
}

func TestWindowStructSizes(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layouts")
	}
	if s := unsafe.Sizeof(notifyIconData{}); s != 976 {
		t.Errorf("NOTIFYICONDATAW is %d bytes, Windows expects 976", s)
	}
	if s := unsafe.Sizeof(mouseInput{}); s != 40 {
		t.Errorf("INPUT is %d bytes, Windows expects 40", s)
	}
}

// With the app's own title bar the page reaches the top edge of the window
// (no Windows title bar), a thin strip at the top still resizes the
// window, and switching back gives Windows' title bar again. The window is
// never shown.
func TestOwnTitleBar(t *testing.T) {
	h := testWindow(t)
	classic := clientTop(h)
	if classic <= 0 {
		t.Fatalf("Windows' title bar: the client area starts %d px below the top", classic)
	}
	applyFrame(h, true)
	if got := clientTop(h); got != 0 {
		t.Errorf("own title bar: the client area starts %d px below the top (Windows' bar was %d)", got, classic)
	}
	var wr rect
	_, _, _ = procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	mid := (wr.Left + wr.Right) / 2
	strip := topStrip(h)
	t.Logf("Windows' title bar %d px; top strip %d px; dpi %d", classic, strip, windowDPI(h))
	if strip < 3 {
		t.Errorf("top strip %d", strip)
	}
	if r := hitTest(h, mid, wr.Top+1); r != htTop {
		t.Errorf("top edge: hit test %d, want %d (resize from the top)", r, htTop)
	}
	var cr rect
	_, _, _ = procGetClientRect.Call(h, uintptr(unsafe.Pointer(&cr)))
	var origin point
	_, _, _ = procClientToScreen.Call(h, uintptr(unsafe.Pointer(&origin)))
	if r := hitTest(h, origin.X+2, wr.Top+1); r != htTopLeft {
		t.Errorf("top-left corner: hit test %d, want %d", r, htTopLeft)
	}
	if r := hitTest(h, origin.X+cr.Right-2, wr.Top+1); r != htTopRight {
		t.Errorf("top-right corner: hit test %d, want %d", r, htTopRight)
	}
	if r := hitTest(h, mid, wr.Top+int32(strip)+20); r != htClient {
		t.Errorf("below the strip: hit test %d, want the page (%d)", r, htClient)
	}
	applyFrame(h, false)
	if got := clientTop(h); got != classic {
		t.Errorf("back to Windows' title bar: %d px, want %d", got, classic)
	}
	if r := hitTest(h, mid, wr.Top+classic/2); r != htCaption {
		t.Errorf("Windows' title bar: hit test %d, want %d", r, htCaption)
	}
}

// Maximized, the page starts at the top of the screen (not cut off), and
// there is no resize strip. Shows the window: under Wine only.
func TestOwnTitleBarMaximized(t *testing.T) {
	changesThisPC(t)
	h := testWindow(t)
	applyFrame(h, true)
	_, _, _ = procShowWindow.Call(h, 3) // SW_MAXIMIZE
	defer procShowWindow.Call(h, swHide) //nolint:errcheck // hidden again
	if !zoomed(h) {
		t.Skip("this desktop does not maximize windows")
	}
	if s := topStrip(h); s != 0 {
		t.Errorf("maximized: top strip %d", s)
	}
	var wr rect
	_, _, _ = procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	if got, want := clientTop(h), frameY(h); wr.Top < 0 && got != want {
		t.Errorf("maximized: the client area starts %d px below the window top, want %d (the part outside the screen)", got, want)
	}
	t.Logf("maximized: window top %d, client starts %d below it", wr.Top, clientTop(h))
}

// Hiding in the tray and coming back: the tray icon's click and the
// message of a second copy of the app both bring the window back.
func TestTrayAndShowAgain(t *testing.T) {
	changesThisPC(t)
	h := testWindow(t)
	app.hwnd = h
	registerWindowMessages()
	if win.msgShow == 0 {
		t.Fatal("no window message")
	}
	visible := func() bool { r, _, _ := procIsWindowVisible.Call(h); return r != 0 }
	ok := trayAdd(false)
	t.Logf("tray icon added: %v", ok)
	win.inTray.Store(true)
	_, _, _ = procShowWindow.Call(h, swHide)
	_, _, _ = procSendMessageW.Call(h, wmTray, 1, wmLButtonUp)
	if !visible() || win.inTray.Load() {
		t.Fatalf("a click on the tray icon: visible=%v inTray=%v", visible(), win.inTray.Load())
	}
	win.inTray.Store(true)
	_, _, _ = procShowWindow.Call(h, swHide)
	_, _, _ = procSendMessageW.Call(h, uintptr(win.msgShow), 0, 0)
	if !visible() || win.inTray.Load() {
		t.Fatalf("the second copy's message: visible=%v inTray=%v", visible(), win.inTray.Load())
	}
	time.Sleep(50 * time.Millisecond)
	if fg, _, _ := procGetForegroundWindow.Call(); fg != h {
		t.Logf("in front: %v (the desktop decides)", fg == h)
	}
	trayRemove()
	_, _, _ = procShowWindow.Call(h, swHide)
}
