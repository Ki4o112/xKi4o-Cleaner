//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const autoMutexName = `Local\xKi4oCleaner.AutoClean`

type autoLast struct {
	At      int64  `json:"at"`
	Freed   int64  `json:"freed"`
	Files   int    `json:"files"`
	Skipped int    `json:"skipped"`
	State   string `json:"state"`
}

type autoState struct {
	Freq  string    `json:"freq"`  // off, daily, weekly, monthly
	Task  bool      `json:"task"`  // the task is in Task Scheduler
	Moved bool      `json:"moved"` // the task starts a copy of the app that is somewhere else
	Day   int       `json:"day"`   // weekly: 0 = Sunday; monthly: day of the month
	Last  *autoLast `json:"last,omitempty"`
	IDs   []string  `json:"ids"`
	Error string    `json:"error,omitempty"`
}

// taskFile: where Windows keeps the task (only administrators can read it).
func taskFile() string {
	return filepath.Join(P.windir, "System32", "Tasks", autoTaskName)
}

// readTaskXML: the task as Windows stores it (UTF-16 XML), "" when there is none.
func readTaskXML() string {
	b, err := os.ReadFile(taskFile())
	if err != nil || len(b) < 2 {
		return ""
	}
	if b[0] == 0xFF && b[1] == 0xFE { // UTF-16 LE with BOM
		u := make([]uint16, (len(b)-2)/2)
		for i := range u {
			u[i] = uint16(b[2+2*i]) | uint16(b[3+2*i])<<8
		}
		return string(utf16.Decode(u))
	}
	return string(b)
}

func autoCleanState() autoState {
	m := getSettings()
	st := autoState{Freq: "off", IDs: autoCleanPick(asMap(m["choices"]))}
	if f, _ := m["autoClean"].(string); autoFreqs[f] {
		st.Freq = f
	}
	if d, ok := m["autoCleanDay"].(float64); ok {
		st.Day = int(d)
	}
	if l, ok := m["autoCleanLast"].(map[string]any); ok {
		st.Last = &autoLast{At: int64(num(l["at"])), Freed: int64(num(l["freed"])), Files: int(num(l["files"])), Skipped: int(num(l["skipped"])), State: str(l["state"])}
	}
	if x := readTaskXML(); x != "" {
		st.Task = true
		if exe, err := os.Executable(); err == nil && taskCommand(x) != "" {
			st.Moved = !strings.EqualFold(filepath.Clean(taskCommand(x)), filepath.Clean(exe))
		}
	}
	return st
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// utf16File writes s as UTF-16 LE with a BOM (what Task Scheduler expects
// for an XML that says encoding="UTF-16").
func utf16File(path, s string) error {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2+2*len(u))
	b[0], b[1] = 0xFF, 0xFE
	for i, c := range u {
		b[2+2*i], b[3+2*i] = byte(c), byte(c>>8)
	}
	return os.WriteFile(path, b, 0o600)
}

// autoCleanSet turns automatic cleaning on (daily, weekly, monthly) or
// off. On: the task is made (or made again) for this copy of the app.
func autoCleanSet(freq string) autoState {
	if freq != "off" && !autoFreqs[freq] {
		st := autoCleanState()
		st.Error = T("auto.err.freq")
		return st
	}
	op := beginOp("schedule", "", T("log.auto", T("auto.f."+freq)), nil)
	err := func() error {
		if !app.admin {
			return errors.New(T("blocked.admin")) // the task can be neither made nor removed
		}
		if freq == "off" {
			if _, err := os.Stat(taskFile()); errors.Is(err, os.ErrNotExist) {
				return nil // no task: nothing to remove
			}
			return runTool(sys32("schtasks.exe"), "/Delete", "/TN", autoTaskName, "/F")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		sid := ""
		if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
			sid = tu.User.Sid.String()
		}
		start := autoStart(time.Now(), 12, freq)
		x, err := autoTaskXML(exe, sid, freq, start, T("auto.taskDesc"))
		if err != nil {
			return err
		}
		tmp := dataDirPath("autoclean-task.xml")
		_ = os.MkdirAll(filepath.Dir(tmp), 0o755)
		if err := utf16File(tmp, x); err != nil {
			return err
		}
		defer os.Remove(tmp)
		if err := runTool(sys32("schtasks.exe"), "/Create", "/TN", autoTaskName, "/XML", tmp, "/F"); err != nil {
			return err
		}
		updateSettings(func(m map[string]any) {
			if freq == "weekly" {
				m["autoCleanDay"] = int(start.Weekday())
			} else {
				m["autoCleanDay"] = start.Day()
			}
		})
		return nil
	}()
	if err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		st := autoCleanState()
		st.Error = err.Error()
		return st
	}
	updateSettings(func(m map[string]any) { m["autoClean"] = freq })
	op.info("", T("auto.f."+freq), map[string]any{"exe": func() string { e, _ := os.Executable(); return e }()})
	op.end(T("auto.f."+freq), nil)
	return autoCleanState()
}

// autoCleanBusy: an automatic cleaning started by the task is running in
// another copy of the app right now.
func autoCleanBusy() bool {
	name, _ := windows.UTF16PtrFromString(autoMutexName)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// autoCleanMain: the app started by the task (no window). When the app is
// open, its window does the cleaning (and shows it); else it is done here.
func autoCleanMain() {
	name, _ := windows.UTF16PtrFromString(autoMutexName)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || h == 0 {
		return // one is already running
	}
	defer windows.CloseHandle(h) //nolint:errcheck // released on exit anyway
	initPaths()
	logInit(logDir())
	applySettings(getSettings())
	app.admin = isElevated()
	if askOpenCopy() {
		return
	}
	buildCatalog()
	runAutoClean("task")
}

// askOpenCopy: the app is open — it gets the job.
func askOpenCopy() bool {
	cls, _ := windows.UTF16PtrFromString("webview")
	title, _ := windows.UTF16PtrFromString(appName)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	registerWindowMessages()
	if win.msgAuto == 0 {
		return false
	}
	r, _, _ := procPostMessageW.Call(hwnd, uintptr(win.msgAuto), 0, 0)
	return r != 0
}

// runAutoClean cleans the automatic categories once. Open apps and the
// categories unticked on the Cleanup page are left out.
func runAutoClean(source string) {
	defer recoverLog("clean")
	if !app.busy.CompareAndSwap(false, true) {
		logEvent("clean", "warn", "", T("auto.skipBusy"), map[string]any{"source": source})
		saveAutoLast(autoLast{At: time.Now().Unix(), State: "busy"})
		return
	}
	app.working.Store(true)
	app.stop.Store(false)
	ids := autoCleanPick(asMap(getSettings()["choices"]))
	op := beginOp("clean", "", T("log.autoClean"), map[string]any{"categories": ids, "source": source, "admin": app.admin})
	var sum cleanSummary
	func() {
		defer func() {
			if r := recover(); r != nil {
				op.panicked(r)
			}
		}()
		sum = runClean(ids, CleanOpts{OpenApps: "skip", Quiet: true}, op)
	}()
	app.working.Store(false)
	app.busy.Store(false)
	op.end(T("log.cleanSum", sizeText(sum.freed), sum.files), map[string]any{"freed": sum.freed, "files": sum.files, "locked": sum.locked, "failed": sum.failed})
	state := "ok"
	if s := opState(op); s != "" {
		state = s
	}
	saveAutoLast(autoLast{At: time.Now().Unix(), Freed: sum.freed, Files: sum.files, Skipped: sum.locked + sum.denied, State: state})
	emit("auto-clean", map[string]any{"freed": sum.freed, "files": sum.files})
}

func saveAutoLast(l autoLast) {
	updateSettings(func(m map[string]any) {
		m["autoCleanLast"] = map[string]any{"at": l.At, "freed": l.Freed, "files": l.Files, "skipped": l.Skipped, "state": l.State}
	})
}

// opState: how an ended operation went (ok, warn, error).
func opState(o *opHandle) string {
	if o == nil {
		return ""
	}
	olog.mu.Lock()
	defer olog.mu.Unlock()
	if s := olog.ops[o.id]; s != nil {
		return s.State
	}
	return ""
}
