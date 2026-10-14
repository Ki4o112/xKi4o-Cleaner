//go:build windows

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

//go:embed ui/index.html
var indexHTML string

const (
	appName    = "xKi4o Cleaner"
	appVersion = "2.3.0"
	appAuthor  = "xKi4o"
	bgColor    = 0x0b1020
)

type App struct {
	w        webview2.WebView
	hwnd     uintptr
	admin    bool
	busy     atomic.Bool // any disk job, scans included
	working  atomic.Bool // a job worth asking about before closing
	critical atomic.Bool // a system tool is running right now
	stop     atomic.Bool
}

var app = &App{}

func init() { runtime.LockOSThread() }

func main() {
	if !acquireSingleInstance() {
		return
	}
	app.admin = isElevated()
	initPaths()
	buildCatalog()
	buildTweaks()
	applySettings(getSettings())

	scale := dpiScale()
	width, height := int(1180*scale), int(820*scale)
	if ww, wh := workArea(); ww > 0 && wh > 0 {
		width = min(width, ww-int(24*scale))
		height = min(height, wh-int(24*scale))
	}

	// Dark background from the very first frame (no white flash).
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", fmt.Sprintf("FF%06X", bgColor))
	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner")
	_ = os.MkdirAll(dataDir, 0o755)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus:  true,
		DataPath:   dataDir,
		Background: bgColor,
		OnClose:    confirmClose,
		WindowOptions: webview2.WindowOptions{
			Title:     appName,
			Width:     uint(width),
			Height:    uint(height),
			IconId:    1,
			Center:    true,
			OnCreated: func(h uintptr) { app.hwnd = h; styleWindow(h) },
		},
	})
	if w == nil {
		webviewMissing()
		return
	}
	app.w = w
	defer w.Destroy()
	go memAutoLoop() // started after app.w is set, so its events can reach the page
	w.SetSize(min(int(980*scale), width), min(int(660*scale), height), webview2.HintMin)

	binds := map[string]any{
		"appInfo":            appInfo,
		"startAnalyze":       startAnalyze,
		"startClean":         startClean,
		"stopClean":          func() bool { app.stop.Store(true); return true },
		"getDrives":          getDrives,
		"startTrim":          startTrim,
		"startRepair":        startRepair,
		"createRestorePoint": startRestorePoint,
		"openSystemRestore":  func() bool { shellOpen(sys32("rstrui.exe")); return true },
		"getSettings":        getSettings,
		"saveSettings":       saveAndApplySettings,
		"getMemory":          getMemory,
		"cleanMemory":        func(regions []string) memResult { return cleanMemory(regions) },
		"openLink":           openLink,
		"closeApps":          closeApps,
		"tweakList":          tweakList,
		"tweakStates":        tweakStates,
		"setTweak":           setTweak,
		"debloatApps":        debloatApps,
		"restartExplorer":    restartExplorer,
	}
	for name, f := range binds {
		must(w.Bind(name, f))
	}

	w.SetHtml(indexHTML)
	w.Run()
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// applySettings pushes saved settings into the backend (language for
// dialogs, auto memory cleaning).
func applySettings(m map[string]any) {
	if l, ok := m["lang"].(string); ok {
		setLang(l)
	}
	if raw, ok := m["mem"]; ok {
		b, _ := json.Marshal(raw)
		var c memConfig
		if json.Unmarshal(b, &c) == nil {
			setMemConfig(c)
		}
	}
}

func saveAndApplySettings(m map[string]any) bool {
	applySettings(m)
	return saveSettings(m)
}

// openLink opens only the fixed links the credits page shows.
func openLink(url string) bool {
	switch url {
	case "https://github.com/jchv/go-webview2", "https://go.dev", "https://github.com/JulietaUla/Montserrat",
		"https://github.com/henrypp/memreduct", "https://www.bleachbit.org", "https://github.com/MoscaDotTo/Winapp2",
		"https://github.com/Parcoil/Sparkle", "https://github.com/ChrisTitusTech/winutil", "https://github.com/NVIDIA/nvapi":
		shellOpen(url)
		return true
	}
	return false
}

func webviewMissing() {
	if messageBox(app.hwnd, T("box.webview"), appName, mbYesNo|mbIconError) == idYes {
		shellOpen("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
	}
}

func confirmClose() bool {
	if !app.working.Load() {
		return true
	}
	msg := T("box.closeBusy")
	if app.critical.Load() {
		msg = T("box.closeCritical")
	}
	return messageBox(app.hwnd, msg, appName, mbYesNo|mbIconWarning) == idYes
}

// emit sends an event to the page: window.__ev(name, data).
func emit(name string, data any) {
	b, err := json.Marshal(data)
	if err != nil || app.w == nil {
		return
	}
	js := "window.__ev&&window.__ev(" + strconv.Quote(name) + "," + string(b) + ")"
	app.w.Dispatch(func() { app.w.Eval(js) })
}

// ---------------------------------------------------------------- bindings

func appInfo() map[string]any {
	return map[string]any{
		"version": appVersion,
		"author":  appAuthor,
		"admin":   app.admin,
		"os":      osName(),
		"drive":   sysDriveInfo(),
		"year":    time.Now().Year(),
	}
}

func startAnalyze() bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		pending := false
		func() {
			defer func() { _ = recover() }()
			pending = analyzeAll(app.admin, func(r report) { emit("item", r) })
		}()
		app.busy.Store(false)
		emit("analyze-done", map[string]any{"drive": sysDriveInfo(), "pending": pending})
	}()
	return true
}

type CleanOpts struct {
	ResetBase    bool     `json:"resetBase"`
	RestorePoint bool     `json:"restorePoint"`
	Memory       bool     `json:"memory"`  // "clean everything": also free RAM
	Trim         bool     `json:"trim"`    // "clean everything": also TRIM SSDs
	MemRegions   []string `json:"regions"` // memory regions for Memory
}

type stepMsg struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Freed int64  `json:"freed,omitempty"`
	Note  string `json:"note,omitempty"`
}

func startClean(ids []string, opts CleanOpts) bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	app.working.Store(true)
	app.stop.Store(false)
	go func() {
		var total, ram int64
		restart := false
		func() {
			defer func() { _ = recover() }()
			total, ram, restart = runClean(ids, opts)
		}()
		app.working.Store(false)
		app.busy.Store(false)
		emit("clean-done", map[string]any{
			"freed": total, "ram": ram, "drive": sysDriveInfo(), "restart": restart, "stopped": app.stop.Load(),
		})
	}()
	return true
}

func runClean(ids []string, opts CleanOpts) (total, ram int64, restart bool) {
	sel := map[string]bool{}
	for _, id := range ids {
		sel[id] = true
	}
	var chosen []*item
	deep := false
	for _, it := range catalog {
		if sel[it.id] {
			chosen = append(chosen, it)
			deep = deep || it.deep
		}
	}

	rpOK := true
	if deep && opts.RestorePoint && app.admin {
		emit("restore", map[string]string{"state": "running"})
		app.critical.Store(true)
		rpOK = createRestorePoint()
		app.critical.Store(false)
		if rpOK {
			emit("restore", map[string]string{"state": "done"})
		} else {
			emit("restore", map[string]string{"state": "failed"})
		}
	}

	for _, it := range chosen {
		if app.stop.Load() {
			break
		}
		skip := func(note string) { emit("step", stepMsg{ID: it.id, State: "skipped", Note: note}) }
		procs := runningProcs()
		switch {
		case it.deep && !rpOK:
			skip(T("skip.rp"))
			continue
		case it.admin && !app.admin:
			skip(T("blocked.admin"))
			continue
		case it.waitsWU && updatesPending(procs):
			skip(T("skip.pending"))
			continue
		case it.runningIn(procs):
			skip(T("skip.open"))
			continue
		}

		emit("step", stepMsg{ID: it.id, State: "running"})
		lastNote := ""
		ctx := &cleanCtx{
			opts: opts,
			stop: func() bool { return app.stop.Load() },
			note: func(n string) {
				if n != lastNote {
					lastNote = n
					emit("step", stepMsg{ID: it.id, State: "running", Note: n})
				}
			},
		}
		freed, err := cleanItem(it, ctx)
		total += freed
		if err != nil {
			emit("step", stepMsg{ID: it.id, State: "failed", Freed: freed, Note: err.Error()})
			continue
		}
		if it.restart {
			restart = true
		}
		emit("step", stepMsg{ID: it.id, State: "done", Freed: freed})
	}

	if opts.Memory && !app.stop.Load() && app.admin {
		emit("step", stepMsg{ID: "ram", State: "running"})
		res := cleanMemory(opts.MemRegions)
		ram = res.Freed
		emit("step", stepMsg{ID: "ram", State: "done", Freed: res.Freed})
	}
	if opts.Trim && !app.stop.Load() && app.admin {
		emit("step", stepMsg{ID: "trim", State: "running"})
		for _, d := range getDrives() {
			if d.Media != "HDD" {
				_, _, _ = runQuiet(15*time.Minute, sys32("defrag.exe"), d.Letter+":", "/L")
			}
		}
		emit("step", stepMsg{ID: "trim", State: "done"})
	}
	return total, ram, restart
}

func cleanItem(it *item, c *cleanCtx) (freed int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s", T("err.unexpected", r))
		}
	}()
	if it.clean != nil {
		f, _, e := it.clean(c)
		return f, e
	}
	if it.targets == nil {
		return 0, nil
	}
	st := cleanTargets(it.targets(), c.stop)
	return st.bytes, nil
}

func startTrim() bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	app.working.Store(true)
	go func() {
		defer func() {
			_ = recover()
			app.working.Store(false)
			app.busy.Store(false)
			emit("trim-done", map[string]bool{"ok": true})
		}()
		for _, d := range getDrives() {
			if d.Media == "HDD" {
				continue
			}
			_, code, err := runQuiet(15*time.Minute, sys32("defrag.exe"), d.Letter+":", "/L")
			res := map[string]any{"letter": d.Letter, "ok": err == nil && code == 0}
			if code != 0 {
				res["note"] = T("trim.unsupported")
			}
			emit("trim-drive", res)
		}
	}()
	return true
}

func startRepair(kind string) bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	app.working.Store(true)
	go func() {
		ok, summary := false, ""
		func() {
			defer func() {
				if r := recover(); r != nil {
					summary = fmt.Sprint(r)
				}
			}()
			ok, summary = runRepair(kind)
		}()
		app.working.Store(false)
		app.busy.Store(false)
		emit("repair-done", map[string]any{"ok": ok, "summary": summary})
	}()
	return true
}

func startRestorePoint() bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	app.working.Store(true)
	go func() {
		app.critical.Store(true)
		ok := app.admin && createRestorePoint()
		app.critical.Store(false)
		app.working.Store(false)
		app.busy.Store(false)
		note := ""
		if !app.admin {
			note = T("rp.admin")
		}
		emit("rp-done", map[string]any{"ok": ok, "note": note})
	}()
	return true
}
