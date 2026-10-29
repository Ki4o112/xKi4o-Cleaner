//go:build windows

package main

// The window: its own title bar (the page draws it; Windows keeps the
// side and bottom borders for resizing, the shadow and snapping), hiding in
// the notification area (the tray), and coming to the front on start.
// Everything here runs on the UI thread (the window's message hook, or
// through app.w.Dispatch).

import (
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	uxtheme = windows.NewLazySystemDLL("uxtheme.dll")

	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procSendInput                = user32.NewProc("SendInput")
	procReleaseCapture           = user32.NewProc("ReleaseCapture")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procIsZoomed                 = user32.NewProc("IsZoomed")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procScreenToClient           = user32.NewProc("ScreenToClient")
	procGetClientRect            = user32.NewProc("GetClientRect")
	procGetAsyncKeyState         = user32.NewProc("GetAsyncKeyState")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmSize              = 0x0005
	wmActivate          = 0x0006
	wmClose             = 0x0010
	wmNull              = 0x0000
	wmContextMenu       = 0x007B
	wmNCCalcSize        = 0x0083
	wmNCHitTest         = 0x0084
	wmNCLButtonDown     = 0x00A1
	wmSysCommand        = 0x0112
	wmLButtonUp         = 0x0202
	wmLButtonDblClk     = 0x0203
	wmRButtonUp         = 0x0205
	wmTray              = 0x8000 + 77 // WM_APP + 77: the tray icon's messages
	ninBalloonUserClick = 0x0405      // the notification was clicked

	htClient   = 1
	htCaption  = 2
	htTop      = 12
	htTopLeft  = 13
	htTopRight = 14

	scMinimize = 0xF020
	scMaximize = 0xF030
	scRestore  = 0xF120

	swHide    = 0
	swShow    = 5
	swRestore = 9

	sizeMaximized = 2
	sizeMinimized = 1
)

// win: the state of the window.
var win struct {
	own      atomic.Bool // the app's own title bar (false: Windows' title bar)
	inTray   atomic.Bool // hidden, with an icon in the notification area
	quitting atomic.Bool // Exit was chosen in the tray menu: closing really ends the app
	max      atomic.Bool
	msgShow  uint32 // "come to the front", sent by a second copy of the app
	msgTBar  uint32 // "TaskbarCreated": Explorer restarted, the tray icon must be added again
	msgAuto  uint32 // "clean now", sent by the copy the automatic cleaning task started
	trayIcon uintptr
	noteUpd  atomic.Bool // the last Windows notification is about a new version
}

func registerWindowMessages() {
	for _, m := range []struct {
		name string
		to   *uint32
	}{{"xKi4oCleaner.Show", &win.msgShow}, {"TaskbarCreated", &win.msgTBar}, {"xKi4oCleaner.AutoClean", &win.msgAuto}} {
		p, _ := windows.UTF16PtrFromString(m.name)
		r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
		*m.to = uint32(r)
	}
}

type rect struct{ Left, Top, Right, Bottom int32 }

type point struct{ X, Y int32 }

type ncCalcSizeParams struct {
	Rgrc  [3]rect
	Lppos uintptr
}

// osPointer: a pointer Windows passed in a message (memory Windows owns,
// not Go's: the garbage collector never moves or frees it).
func osPointer(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

func defWindowProc(hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func windowDPI(hwnd uintptr) uintptr {
	if procGetDpiForWindow.Find() == nil {
		if d, _, _ := procGetDpiForWindow.Call(hwnd); d >= 96 {
			return d
		}
	}
	return 96
}

func metric(hwnd uintptr, idx uintptr) int32 {
	if procGetSystemMetricsForDpi.Find() == nil {
		r, _, _ := procGetSystemMetricsForDpi.Call(idx, windowDPI(hwnd))
		return int32(r)
	}
	r, _, _ := procGetSystemMetrics.Call(idx)
	return int32(r)
}

// frameY: the height of the resizing border (SM_CYFRAME + SM_CXPADDEDBORDER).
func frameY(hwnd uintptr) int32 { return metric(hwnd, 33) + metric(hwnd, 92) }

// topStrip: pixels left above the page for resizing from the top edge (the
// rest of the border is Windows' own). None when maximized.
func topStrip(hwnd uintptr) int {
	if !win.own.Load() || zoomed(hwnd) {
		return 0
	}
	return max(int(frameY(hwnd)/2), 3)
}

func zoomed(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

func iconic(hwnd uintptr) bool {
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}

// winHook sees every message of the main window first.
func winHook(hwnd, msg, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case wmNCCalcSize:
		if wp == 0 || !win.own.Load() {
			return 0, false
		}
		// Windows works out the borders; the title bar is left out (the
		// page draws its own). Maximized, the window reaches past the
		// screen by the border: the page starts below that.
		p := (*ncCalcSizeParams)(osPointer(lp))
		top := p.Rgrc[0].Top
		defWindowProc(hwnd, msg, wp, lp)
		p.Rgrc[0].Top = top
		if zoomed(hwnd) {
			p.Rgrc[0].Top += frameY(hwnd)
		}
		return 0, true
	case wmNCHitTest:
		if !win.own.Load() {
			return 0, false
		}
		r := defWindowProc(hwnd, msg, wp, lp)
		if r != htClient || zoomed(hwnd) {
			return r, true
		}
		pt := point{int32(int16(lp & 0xFFFF)), int32(int16((lp >> 16) & 0xFFFF))}
		_, _, _ = procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
		if strip := topStrip(hwnd); strip > 0 && int(pt.Y) < strip {
			var cr rect
			_, _, _ = procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
			corner := int32(16 * windowDPI(hwnd) / 96)
			switch {
			case pt.X < corner:
				return htTopLeft, true
			case pt.X >= cr.Right-corner:
				return htTopRight, true
			}
			return htTop, true
		}
		return r, true
	case wmSize:
		if wp != sizeMinimized {
			m := wp == sizeMaximized
			if win.max.Swap(m) != m {
				emitWin()
			}
			if app.w != nil {
				app.w.SetTopInset(topStrip(hwnd))
			}
		}
		return 0, false // the page is resized as usual
	case wmActivate:
		emit("win", map[string]any{"active": wp&0xFFFF != 0, "max": win.max.Load(), "own": win.own.Load()})
		return 0, false
	case wmTray:
		switch lp & 0xFFFF {
		case wmLButtonUp, wmLButtonDblClk:
			winShow()
		case wmRButtonUp, wmContextMenu:
			trayMenu(hwnd)
		case ninBalloonUserClick:
			winShow()
			if win.noteUpd.Load() {
				emit("upd-open", nil) // the page opens the update
			}
		}
		return 0, true
	}
	if win.msgShow != 0 && uint32(msg) == win.msgShow {
		winShow()
		return 1, true
	}
	if win.msgAuto != 0 && uint32(msg) == win.msgAuto {
		go runAutoClean("task")
		return 1, true
	}
	if win.msgTBar != 0 && uint32(msg) == win.msgTBar && win.inTray.Load() {
		trayAdd(false)
	}
	return 0, false
}

func emitWin() {
	emit("win", map[string]any{"active": true, "max": win.max.Load(), "own": win.own.Load()})
}

// applyFrame switches between the app's own title bar and Windows' one.
func applyFrame(hwnd uintptr, own bool) {
	win.own.Store(own)
	const swpFrame = 0x0020 | 0x0002 | 0x0001 | 0x0004 | 0x0010 // FRAMECHANGED, NOMOVE, NOSIZE, NOZORDER, NOACTIVATE
	_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFrame)
	if app.w != nil {
		app.w.SetTopInset(topStrip(hwnd))
	}
}

// bringToFront puts the window above the others and gives it the keyboard.
// Windows lets a program take the front only in some cases (it was started
// by the user just now, or it got the last input); started through the
// administrator prompt, or while the page loads, the app may have lost
// that, and would open behind the browser.
func bringToFront(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if iconic(hwnd) {
		_, _, _ = procShowWindow.Call(hwnd, swRestore)
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != hwnd {
		me := windows.GetCurrentThreadId()
		var other uint32
		if fg != 0 {
			other, _ = windows.GetWindowThreadProcessId(windows.HWND(fg), nil)
		}
		if other != 0 && other != me {
			if r, _, _ := procAttachThreadInput.Call(uintptr(me), uintptr(other), 1); r != 0 {
				defer procAttachThreadInput.Call(uintptr(me), uintptr(other), 0) //nolint:errcheck // detaching again
			}
		}
		// an input event of this program (a mouse move of zero) lifts the lock
		in := mouseInput{Type: 0, Flags: 0x0001}
		_, _, _ = procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	}
	const swp = 0x0002 | 0x0001 | 0x0040 // NOMOVE, NOSIZE, SHOWWINDOW
	hwndTopmost, hwndNoTopmost := ^uintptr(0), ^uintptr(1)
	_, _, _ = procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swp)
	_, _, _ = procSetWindowPos.Call(hwnd, hwndNoTopmost, 0, 0, 0, 0, swp)
	_, _, _ = procBringWindowToTop.Call(hwnd)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

// mouseInput is INPUT with a MOUSEINPUT (64-bit layout).
type mouseInput struct {
	Type      uint32
	_         uint32
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	_         uint32
	Extra     uintptr
}

// ---------------------------------------------------------------- the title bar's buttons (called from the page)

// winDrag moves the window with the mouse, as its title bar would. Only
// while the button is still down: a click that is already over must not
// start a move that follows the mouse.
func winDrag() bool {
	if app.w == nil {
		return false
	}
	app.w.Dispatch(func() {
		// the primary button: VK_LBUTTON, or VK_RBUTTON when swapped (SM_SWAPBUTTON)
		btn := uintptr(0x01)
		if r, _, _ := procGetSystemMetrics.Call(23); r != 0 {
			btn = 0x02
		}
		if s, _, _ := procGetAsyncKeyState.Call(btn); s&0x8000 == 0 {
			return
		}
		var pt point
		_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		_, _, _ = procReleaseCapture.Call()
		_, _, _ = procPostMessageW.Call(app.hwnd, wmNCLButtonDown, htCaption, uintptr(uint16(pt.X))|uintptr(uint16(pt.Y))<<16)
	})
	return true
}

func winSys(cmd uintptr) bool {
	if app.hwnd == 0 {
		return false
	}
	_, _, _ = procPostMessageW.Call(app.hwnd, wmSysCommand, cmd, 0)
	return true
}

func winMinimize() bool { return winSys(scMinimize) }

// winMaximize maximizes the window, or puts it back to its size.
func winMaximize() bool {
	if app.hwnd != 0 && zoomed(app.hwnd) {
		return winSys(scRestore)
	}
	return winSys(scMaximize)
}

func winClose() bool {
	if app.hwnd == 0 {
		return false
	}
	_, _, _ = procPostMessageW.Call(app.hwnd, wmClose, 0, 0)
	return true
}

// winFrame switches the title bar (own: the app's; else Windows').
func winFrame(own bool) map[string]any {
	if app.w != nil && app.hwnd != 0 {
		app.w.Dispatch(func() { applyFrame(app.hwnd, own) })
	}
	return map[string]any{"own": own, "max": win.max.Load(), "active": true}
}

// winTray hides the window; the app goes on working, with an icon in the
// notification area. hint: say so in a notification (the first time).
func winTray(hint bool) bool {
	if app.w == nil || app.hwnd == 0 {
		return false
	}
	app.w.Dispatch(func() {
		if !trayAdd(hint) {
			return // no icon, no way back: the window stays
		}
		win.inTray.Store(true)
		_, _, _ = procShowWindow.Call(app.hwnd, swHide)
	})
	return true
}

// trayStart hides the window from the start (started with Windows). Right
// after sign-in the taskbar may not be there yet: the icon is tried again
// every 2 seconds (and Explorer's "TaskbarCreated" adds it too); after a
// minute without it the window shows, so the app is never out of reach.
func trayStart(hint bool) {
	win.inTray.Store(true)
	_, _, _ = procShowWindow.Call(app.hwnd, swHide)
	if trayAdd(hint) {
		return
	}
	go func() {
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			ok := make(chan bool, 1)
			app.w.Dispatch(func() { ok <- !win.inTray.Load() || trayAdd(false) })
			if <-ok {
				return
			}
		}
		app.w.Dispatch(func() {
			win.inTray.Store(false)
			_, _, _ = procShowWindow.Call(app.hwnd, swShow)
			bringToFront(app.hwnd)
		})
	}()
}

// winShow brings the window back from the tray (or from behind others).
func winShow() {
	h := app.hwnd
	if h == 0 {
		return
	}
	if win.inTray.Swap(false) {
		trayRemove()
		_, _, _ = procShowWindow.Call(h, swShow)
	}
	bringToFront(h)
	emitWin()
}

// ---------------------------------------------------------------- the tray icon

// notifyIconData is NOTIFYICONDATAW.
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GUIDItem         windows.GUID
	HBalloonIcon     uintptr
}

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	nifMsg    = 0x01
	nifIcon   = 0x02
	nifTip    = 0x04
	nifInfo   = 0x10
)

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func trayData() *notifyIconData {
	if win.trayIcon == 0 {
		var hinst windows.Handle
		_ = windows.GetModuleHandleEx(0, nil, &hinst)
		cx, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON
		cy, _, _ := procGetSystemMetrics.Call(50)
		win.trayIcon, _, _ = procLoadImageW.Call(uintptr(hinst), 1, 1, cx, cy, 0x8000) // IMAGE_ICON, LR_SHARED
	}
	d := &notifyIconData{HWnd: app.hwnd, UID: 1, UFlags: nifMsg | nifIcon | nifTip, UCallbackMessage: wmTray, HIcon: win.trayIcon}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	copyUTF16(d.SzTip[:], T("tray.tip"))
	return d
}

// trayAdd puts the icon in the notification area (hint: with a notification).
func trayAdd(hint bool) bool {
	d := trayData()
	r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(d)))
	if r == 0 {
		// already there (Explorer kept it): update it instead
		r, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
	}
	if r != 0 && hint {
		d.UFlags = nifInfo
		copyUTF16(d.SzInfoTitle[:], appName)
		copyUTF16(d.SzInfo[:], T("tray.hint"))
		d.DwInfoFlags = 0x04 | 0x10 // NIIF_USER (the app's icon), NIIF_NOSOUND
		d.HBalloonIcon = win.trayIcon
		_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
	}
	return r != 0
}

// trayNotify shows a Windows notification from the tray icon (only while
// the app is hidden there: otherwise the page itself shows the result).
func trayNotify(title, text string) bool {
	if !win.inTray.Load() || app.hwnd == 0 {
		return false
	}
	win.noteUpd.Store(false)
	d := trayData()
	d.UFlags = nifInfo
	copyUTF16(d.SzInfoTitle[:], title)
	copyUTF16(d.SzInfo[:], text)
	d.DwInfoFlags = 0x04 // NIIF_USER: the app's icon
	d.HBalloonIcon = win.trayIcon
	r, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
	return r != 0
}

func trayRemove() {
	d := &notifyIconData{HWnd: app.hwnd, UID: 1}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(d)))
}

// trayMenu: Open / Exit, at the mouse.
func trayMenu(hwnd uintptr) {
	m, _, _ := procCreatePopupMenu.Call()
	if m == 0 {
		return
	}
	defer procDestroyMenu.Call(m) //nolint:errcheck // freeing the menu
	add := func(id uintptr, text string) {
		p, _ := windows.UTF16PtrFromString(text)
		_, _, _ = procAppendMenuW.Call(m, 0, id, uintptr(unsafe.Pointer(p)))
	}
	add(1, T("tray.open"))
	_, _, _ = procAppendMenuW.Call(m, 0x800, 0, 0) // MF_SEPARATOR
	add(2, T("tray.exit"))
	_, _, _ = procSetMenuDefaultItem.Call(m, 1, 0)
	var pt point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// in front first, so the menu closes when clicking elsewhere; the menu
	// returns the chosen item (TPM_RETURNCMD | TPM_RIGHTBUTTON | TPM_NONOTIFY)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(m, 0x0100|0x0002|0x0080, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	_, _, _ = procPostMessageW.Call(hwnd, wmNull, 0, 0)
	switch cmd {
	case 1:
		winShow()
	case 2:
		winShow() // a question about running work needs the window
		win.quitting.Store(true)
		_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
}

// darkMenus: the tray and window menus in dark colours, like the app
// (uxtheme's SetPreferredAppMode, Windows 10 1903 and later).
func darkMenus() {
	if windows.RtlGetVersion().BuildNumber < 18362 || uxtheme.Load() != nil {
		return
	}
	if p, err := windows.GetProcAddressByOrdinal(windows.Handle(uxtheme.Handle()), 135); err == nil && p != 0 {
		_, _, _ = syscall.SyscallN(p, 2) // ForceDark
	}
	if p, err := windows.GetProcAddressByOrdinal(windows.Handle(uxtheme.Handle()), 136); err == nil && p != 0 {
		_, _, _ = syscall.SyscallN(p) // FlushMenuThemes
	}
}

// signalRunningCopy asks the copy of the app that is already running to
// come to the front (also out of the tray). This copy was just started by
// the user, so it may let the other one take the front.
func signalRunningCopy() bool {
	_, _, _ = procAllowSetForegroundWindow.Call(^uintptr(0)) // ASFW_ANY
	cls, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString(appName)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	registerWindowMessages()
	if win.msgShow != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, uintptr(win.msgShow), 0, 0)
	}
	if v, _, _ := procIsWindowVisible.Call(hwnd); v != 0 {
		if iconic(hwnd) {
			_, _, _ = procShowWindow.Call(hwnd, swRestore)
		}
		_, _, _ = procSetForegroundWindow.Call(hwnd)
	}
	return true
}

var (
	startTray atomic.Bool // Settings: the app starts hidden in the notification area
	bootOn    atomic.Bool // it starts with Windows
)
