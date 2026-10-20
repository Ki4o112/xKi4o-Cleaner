//go:build windows

package main

// The frame counter on the Game mode page. Frames are counted by PresentMon
// (https://github.com/GameTechDev/PresentMon, Intel, MIT): it reads Windows'
// own graphics events (ETW), so nothing is injected into the game and
// anti-cheat has nothing to object to. PresentMon is not bundled: on the
// user's click its console exe is downloaded from its GitHub release (a fixed
// version, checked by its SHA-256) into the app's folder. While it runs, a
// small click-through window in the screen's corner shows the numbers above
// the game (borderless and windowed games; exclusive full screen hides it).

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	pmVersion = "2.6.0"
	pmURL     = "https://github.com/GameTechDev/PresentMon/releases/download/v" + pmVersion + "/PresentMon-" + pmVersion + "-x64.exe"
	pmDigest  = "sha256:b2a706bc6ad475749e3b7e3409263aa1e6906d45bdcf993f6dbc0f660188f1af"
	pmSession = "xKi4oFPS"
)

var (
	procRegisterClassExW        = user32.NewProc("RegisterClassExW")
	procCreateWindowExW         = user32.NewProc("CreateWindowExW")
	procDestroyWindow           = user32.NewProc("DestroyWindow")
	procSetLayeredWindowAttribs = user32.NewProc("SetLayeredWindowAttributes")
	procGetMessageW             = user32.NewProc("GetMessageW")
	procTranslateMessage        = user32.NewProc("TranslateMessage")
	procDispatchMessageW        = user32.NewProc("DispatchMessageW")
	procPostQuitMessage         = user32.NewProc("PostQuitMessage")
	procSetTimer                = user32.NewProc("SetTimer")
	procInvalidateRect          = user32.NewProc("InvalidateRect")
	procBeginPaint              = user32.NewProc("BeginPaint")
	procEndPaint                = user32.NewProc("EndPaint")
	procFillRect                = user32.NewProc("FillRect")
	procDrawTextW               = user32.NewProc("DrawTextW")
	procCreateSolidBrush        = gdi32.NewProc("CreateSolidBrush")
	procSetTextColor            = gdi32.NewProc("SetTextColor")
	procSetBkMode               = gdi32.NewProc("SetBkMode")
	procCreateFontW             = gdi32.NewProc("CreateFontW")
	procSelectObject            = gdi32.NewProc("SelectObject")
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

type fpsStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Overlay   bool   `json:"overlay"`
	Error     string `json:"error,omitempty"`
}

var fpsRun struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	run     int         // which start the cancel belongs to
	busy    atomic.Bool // downloading
	overlay atomic.Bool
	text    atomic.Value // what the overlay shows
	hwnd    atomic.Uintptr
}

func pmExe() string {
	base := P.local
	if base == "" {
		base = os.Getenv("LOCALAPPDATA")
	}
	return filepath.Join(base, "xKi4oCleaner", "tools", "PresentMon-"+pmVersion+"-x64.exe")
}

func fpsState() fpsStatus {
	fpsRun.mu.Lock()
	running := fpsRun.cancel != nil
	fpsRun.mu.Unlock()
	return fpsStatus{Installed: fileExists(pmExe()), Running: running, Overlay: fpsRun.overlay.Load()}
}

// fpsInstall downloads PresentMon's console exe (github.com only, checked by
// its published SHA-256).
func fpsInstall() fpsStatus {
	if !fpsRun.busy.CompareAndSwap(false, true) {
		return fpsState()
	}
	defer fpsRun.busy.Store(false)
	op := beginOp("fps", "", T("log.fpsInstall", pmVersion), nil)
	dst := pmExe()
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := downloadVerified(ctx, ghAsset{Name: filepath.Base(dst), URL: pmURL, Digest: pmDigest}, dst+".part", func(done, total int64) {
		emit("fps-dl", map[string]any{"done": done})
	})
	if err == nil {
		err = os.Rename(dst+".part", dst)
	}
	if err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		st := fpsState()
		st.Error = T("fps.err.download", T(err.Error()))
		return st
	}
	op.end(T("log.fpsInstalled"), nil)
	return fpsState()
}

// fpsStart starts counting; the page gets an "fps" event every second.
func fpsStart(overlay bool) fpsStatus {
	if !app.admin {
		return fpsStatus{Installed: fileExists(pmExe()), Error: T("blocked.admin")} // ETW needs admin rights
	}
	exe := pmExe()
	if !fileExists(exe) {
		return fpsStatus{Error: T("fps.err.missing")}
	}
	fpsRun.mu.Lock()
	if fpsRun.cancel != nil {
		fpsRun.mu.Unlock()
		fpsSetOverlay(overlay)
		return fpsState()
	}
	ctx, cancel := context.WithCancel(context.Background())
	fpsRun.cancel = cancel
	fpsRun.run++
	run := fpsRun.run
	fpsRun.mu.Unlock()
	fpsSetOverlay(overlay)
	logEvent("fps", "info", "", T("log.fpsStart"), nil)

	var mu sync.Mutex
	book := newFpsBook()
	go func() {
		defer recoverLog("fps")
		code, err := runStreamCtx(ctx, exe, []string{"--output_stdout", "--session_name", pmSession, "--stop_existing_session"},
			func(text string, _ bool) {
				mu.Lock()
				book.line(text, time.Now())
				mu.Unlock()
			})
		stopped := ctx.Err() != nil
		if stopped {
			return // fpsStop ended it (a newer start may already run)
		}
		fpsRun.mu.Lock()
		mine := fpsRun.run == run
		fpsRun.mu.Unlock()
		if mine {
			fpsEnd()
			logEvent("fps", "warn", "", T("fps.err.ended", errText(err, code)), nil)
			emit("fps", map[string]any{"state": "end", "error": T("fps.err.ended", errText(err, code))})
		}
		cancel()
	}()
	go func() {
		defer recoverLog("fps")
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			mu.Lock()
			v, ok := book.now(foregroundPID(), time.Now())
			mu.Unlock()
			if ok {
				fpsRun.text.Store(strings.Join([]string{nf0(v.FPS) + " FPS", "1% " + nf0(v.Low1)}, "\n"))
				emit("fps", map[string]any{"state": "run", "now": v})
			} else {
				fpsRun.text.Store("— FPS")
				emit("fps", map[string]any{"state": "run"})
			}
		}
	}()
	return fpsStatus{Installed: true, Running: true, Overlay: overlay}
}

// fpsEnd: the counter is gone (stopped or ended by itself).
func fpsEnd() {
	fpsRun.mu.Lock()
	c := fpsRun.cancel
	fpsRun.cancel = nil
	fpsRun.mu.Unlock()
	if c != nil {
		c()
	}
	overlayClose()
}

func fpsStop() fpsStatus {
	fpsEnd()
	// the ETW session can outlive a killed PresentMon: end it by its name
	if exe := pmExe(); fileExists(exe) {
		_, _, _ = runQuiet(15*time.Second, exe, "--terminate_existing_session", "--session_name", pmSession)
	}
	logEvent("fps", "info", "", T("log.fpsStop"), nil)
	return fpsState()
}

func fpsSetOverlay(on bool) fpsStatus {
	fpsRun.overlay.Store(on)
	running := fpsState().Running
	if on && running {
		overlayOpen()
	} else {
		overlayClose()
	}
	return fpsState()
}

func nf0(f float64) string {
	if f <= 0 {
		return "—"
	}
	return itoa(int(f + 0.5))
}

func foregroundPID() uint32 {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return 0
	}
	var pid uint32
	_, _ = windows.GetWindowThreadProcessId(windows.HWND(h), &pid)
	return pid
}

// ---- the overlay: a small click-through, always-on-top window ----

const (
	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	wsExNoActivate  = 0x08000000
	wmPaint         = 0x000F
	wmTimer         = 0x0113
	wmDestroy       = 0x0002
	lwaAlpha        = 0x2
	swShowNoActive  = 4
)

type paintStruct struct {
	HDC       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

var overlayClass struct {
	once sync.Once
	name *uint16
	ok   bool
}

func overlayProc(h, m, wp, lp uintptr) uintptr {
	switch m {
	case wmTimer:
		_, _, _ = procInvalidateRect.Call(h, 0, 1)
		const hwndTopmost = ^uintptr(0)                                                   // -1
		_, _, _ = procSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, 0x0001|0x0002|0x0010) // keep above a game that took the top
		return 0
	case wmPaint:
		var ps paintStruct
		dc, _, _ := procBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		var r rect
		_, _, _ = procGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
		br, _, _ := procCreateSolidBrush.Call(0x0D0B0A)
		_, _, _ = procFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), br)
		_, _, _ = procDeleteObject.Call(br)
		s := dpiScale()
		face, _ := windows.UTF16PtrFromString("Segoe UI")
		font, _, _ := procCreateFontW.Call(uintptr(int32(-15*s)), 0, 0, 0, 700, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
		old, _, _ := procSelectObject.Call(dc, font)
		_, _, _ = procSetBkMode.Call(dc, 1)           // transparent
		_, _, _ = procSetTextColor.Call(dc, 0xB0E83E) // the app's emerald (BGR)
		txt, _ := fpsRun.text.Load().(string)
		if txt == "" {
			txt = "— FPS"
		}
		u, _ := windows.UTF16FromString(txt)
		r.Left += int32(8 * s)
		r.Top += int32(4 * s)
		_, _, _ = procDrawTextW.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), 0)
		_, _, _ = procSelectObject.Call(dc, old)
		_, _, _ = procDeleteObject.Call(font)
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

// overlayOpen makes the window on its own thread, with its own message loop.
func overlayOpen() {
	if fpsRun.hwnd.Load() != 0 {
		return
	}
	ready := make(chan struct{})
	go func() {
		defer recoverLog("fps overlay")
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var hinst windows.Handle
		_ = windows.GetModuleHandleEx(0, nil, &hinst)
		overlayClass.once.Do(func() {
			overlayClass.name, _ = windows.UTF16PtrFromString("xKi4oFpsOverlay")
			wc := wndClassEx{WndProc: windows.NewCallback(overlayProc), Instance: hinst, ClassName: overlayClass.name}
			wc.Size = uint32(unsafe.Sizeof(wc))
			a, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
			overlayClass.ok = a != 0
		})
		if !overlayClass.ok {
			close(ready)
			return
		}
		s := dpiScale()
		h, _, _ := procCreateWindowExW.Call(wsExTopmost|wsExTransparent|wsExToolWindow|wsExLayered|wsExNoActivate,
			uintptr(unsafe.Pointer(overlayClass.name)), 0, wsPopup,
			uintptr(int32(12*s)), uintptr(int32(12*s)), uintptr(int32(96*s)), uintptr(int32(46*s)), 0, 0, uintptr(hinst), 0)
		if h == 0 {
			close(ready)
			return
		}
		fpsRun.hwnd.Store(h)
		close(ready)
		_, _, _ = procSetLayeredWindowAttribs.Call(h, 0, 200, lwaAlpha)
		_, _, _ = procShowWindow.Call(h, swShowNoActive)
		_, _, _ = procSetTimer.Call(h, 1, 500, 0)
		var m winMsg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		fpsRun.hwnd.Store(0)
	}()
	<-ready
}

func overlayClose() {
	if h := fpsRun.hwnd.Load(); h != 0 {
		_, _, _ = procPostMessageW.Call(h, wmClose, 0, 0)
	}
}
