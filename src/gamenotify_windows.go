//go:build windows

package main

// The card that says game mode freed the memory for a game that just started:
// a small click-through window over the game (top right of its screen), like
// the FPS overlay. It never takes the focus, so the game keeps the keyboard
// and a full-screen game is not minimized. It fades in, shows a bar running
// out, and fades away by itself.

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateRoundRectRgn     = gdi32.NewProc("CreateRoundRectRgn")
	procSetWindowRgn           = user32.NewProc("SetWindowRgn")
	procDrawIconEx             = user32.NewProc("DrawIconEx")
)

const (
	gnShow = 4500 * time.Millisecond // how long the card stays
	gnFade = 220 * time.Millisecond
)

var gameNote struct {
	once        sync.Once
	class       *uint16
	ok          bool
	hwnd        atomic.Uintptr
	mu          sync.Mutex
	title, text string
	at          time.Time
	icon        uintptr
}

// gameNotify shows the card; a new one replaces the one on screen.
func gameNotify(title, text string) {
	gameNote.mu.Lock()
	gameNote.title, gameNote.text, gameNote.at = title, text, time.Now()
	gameNote.mu.Unlock()
	if h := gameNote.hwnd.Load(); h != 0 {
		_, _, _ = procInvalidateRect.Call(h, 0, 0)
		return
	}
	go gameNoteRun()
}

func gameNoteRun() {
	defer recoverLog("game notification")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	s := dpiScale()
	gameNote.once.Do(func() {
		gameNote.class, _ = windows.UTF16PtrFromString("xKi4oGameNote")
		wc := wndClassEx{WndProc: windows.NewCallback(gameNoteProc), Instance: hinst, ClassName: gameNote.class}
		wc.Size = uint32(unsafe.Sizeof(wc))
		a, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		gameNote.ok = a != 0
		gameNote.icon, _, _ = procLoadImageW.Call(uintptr(hinst), 1, 1, uintptr(int32(40*s)), uintptr(int32(40*s)), 0)
	})
	if !gameNote.ok {
		return
	}
	// the screen of the game (the window in front), less the taskbar
	var area rect
	fg, _, _ := procGetForegroundWindow.Call()
	mon, _, _ := procMonitorFromWindow.Call(fg, 1) // MONITOR_DEFAULTTOPRIMARY
	mi := struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
		area = mi.Monitor
	} else {
		area = rect{0, 0, 1920, 1080}
	}
	w, hgt := int32(360*s), int32(84*s)
	x, y := area.Right-w-int32(20*s), area.Top+int32(20*s)
	h, _, _ := procCreateWindowExW.Call(wsExTopmost|wsExTransparent|wsExToolWindow|wsExLayered|wsExNoActivate,
		uintptr(unsafe.Pointer(gameNote.class)), 0, wsPopup, uintptr(x), uintptr(y), uintptr(w), uintptr(hgt), 0, 0, uintptr(hinst), 0)
	if h == 0 {
		return
	}
	gameNote.hwnd.Store(h)
	defer gameNote.hwnd.Store(0)
	rgn, _, _ := procCreateRoundRectRgn.Call(0, 0, uintptr(w+1), uintptr(hgt+1), uintptr(int32(18*s)), uintptr(int32(18*s)))
	_, _, _ = procSetWindowRgn.Call(h, rgn, 0) // the window owns the region now
	_, _, _ = procSetLayeredWindowAttribs.Call(h, 0, 0, lwaAlpha)
	_, _, _ = procShowWindow.Call(h, swShowNoActive)
	_, _, _ = procSetTimer.Call(h, 1, 16, 0)
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// gameNoteAlpha: the card's opacity this long after it showed (0 = gone).
func gameNoteAlpha(d time.Duration) int {
	const full = 240
	switch {
	case d < 0 || d >= gnShow:
		return 0
	case d < gnFade:
		return int(full * d / gnFade)
	case d > gnShow-gnFade:
		return int(full * (gnShow - d) / gnFade)
	}
	return full
}

func gameNoteProc(h, m, wp, lp uintptr) uintptr {
	switch m {
	case wmTimer:
		gameNote.mu.Lock()
		d := time.Since(gameNote.at)
		gameNote.mu.Unlock()
		a := gameNoteAlpha(d)
		if a == 0 && d > gnFade {
			_, _, _ = procDestroyWindow.Call(h)
			return 0
		}
		_, _, _ = procSetLayeredWindowAttribs.Call(h, 0, uintptr(a), lwaAlpha)
		const hwndTopmost = ^uintptr(0)
		_, _, _ = procSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, 0x0001|0x0002|0x0010)
		_, _, _ = procInvalidateRect.Call(h, 0, 0)
		return 0
	case wmPaint:
		var ps paintStruct
		dc, _, _ := procBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		var r rect
		_, _, _ = procGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
		mdc, _, _ := procCreateCompatibleDC.Call(dc)
		bmp, _, _ := procCreateCompatibleBitmap.Call(dc, uintptr(r.Right), uintptr(r.Bottom))
		oldBmp, _, _ := procSelectObject.Call(mdc, bmp)
		gameNotePaint(mdc, r)
		_, _, _ = procBitBlt.Call(dc, 0, 0, uintptr(r.Right), uintptr(r.Bottom), mdc, 0, 0, 0x00CC0020) // SRCCOPY
		_, _, _ = procSelectObject.Call(mdc, oldBmp)
		_, _, _ = procDeleteObject.Call(bmp)
		_, _, _ = procDeleteDC.Call(mdc)
		_, _, _ = procEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmClose:
		_, _, _ = procDestroyWindow.Call(h)
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	return defWindowProc(h, m, wp, lp)
}

func gameNoteFill(dc uintptr, r rect, color uintptr) {
	br, _, _ := procCreateSolidBrush.Call(color)
	_, _, _ = procFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), br)
	_, _, _ = procDeleteObject.Call(br)
}

func gameNoteText(dc uintptr, s string, r rect, size, weight int32, color uintptr) {
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	font, _, _ := procCreateFontW.Call(uintptr(size), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
	old, _, _ := procSelectObject.Call(dc, font)
	_, _, _ = procSetBkMode.Call(dc, 1)
	_, _, _ = procSetTextColor.Call(dc, color)
	u, _ := windows.UTF16FromString(s)
	// DT_SINGLELINE | DT_END_ELLIPSIS | DT_NOPREFIX
	_, _, _ = procDrawTextW.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), 0x20|0x8000|0x800)
	_, _, _ = procSelectObject.Call(dc, old)
	_, _, _ = procDeleteObject.Call(font)
}

// colors are BGR: the Carbon theme's near-black card and its emerald
func gameNotePaint(dc uintptr, r rect) {
	s := dpiScale()
	px := func(v float64) int32 { return int32(v * s) }
	gameNote.mu.Lock()
	title, text, d := gameNote.title, gameNote.text, time.Since(gameNote.at)
	gameNote.mu.Unlock()
	gameNoteFill(dc, r, 0x14110F)
	gameNoteFill(dc, rect{0, 0, px(4), r.Bottom}, 0xB0E83E) // the accent edge
	if gameNote.icon != 0 {
		_, _, _ = procDrawIconEx.Call(dc, uintptr(px(18)), uintptr((r.Bottom-px(40))/2), gameNote.icon, uintptr(px(40)), uintptr(px(40)), 0, 0, 3) // DI_NORMAL
	}
	gameNoteText(dc, title, rect{px(72), px(16), r.Right - px(14), px(42)}, -px(17), 700, 0xF5F5F5)
	gameNoteText(dc, text, rect{px(72), px(42), r.Right - px(14), px(64)}, -px(13), 400, 0xA8A29E)
	// the bar running out while the card stays
	left := 1 - float64(d)/float64(gnShow)
	if left < 0 {
		left = 0
	}
	bar := rect{px(4), r.Bottom - px(3), 0, r.Bottom}
	bar.Right = bar.Left + int32(float64(r.Right-bar.Left)*left)
	gameNoteFill(dc, rect{px(4), r.Bottom - px(3), r.Right, r.Bottom}, 0x2A2520)
	gameNoteFill(dc, bar, 0xB0E83E)
}
