//go:build windows

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

//go:embed ui/index.html
var indexHTML string

// pageURL writes the page into the app's data folder and gives its file://
// address. NavigateToString puts the page into a data: URL, which Chromium
// cuts at 2 MB: past that (as the page is since 4.19) the window stays empty.
func pageURL(dataDir string) string {
	p := filepath.Join(dataDir, "ui", "index.html")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return ""
	}
	if old, err := os.ReadFile(p); err != nil || string(old) != indexHTML {
		if err := os.WriteFile(p, []byte(indexHTML), 0o644); err != nil {
			return ""
		}
	}
	return (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(p)}).String()
}

const (
	appName   = "xKi4o Cleaner"
	appAuthor = "xKi4o"
	bgColor   = 0x070708 // the Graphite theme's background (no flash before the page paints)
)

type App struct {
	w          webview2.WebView
	hwnd       uintptr
	admin      bool
	trayClose  atomic.Bool // the app starts with Windows: closing the window hides it in the tray
	trayHinted atomic.Bool
	busy       atomic.Bool // any disk job, scans included
	working    jobCount    // jobs worth asking about before closing
	critical   jobCount    // system tools running right now
	stop       atomic.Bool
}

// jobCount counts running jobs of one kind. Several can overlap (a tweak
// while a cleaning runs), so the first one to end must not clear the flag
// for the others. Every Store(true) is paired with one Store(false).
type jobCount struct{ n atomic.Int32 }

func (c *jobCount) Store(on bool) {
	if on {
		c.n.Add(1)
	} else if c.n.Add(-1) < 0 {
		c.n.Store(0)
	}
}

func (c *jobCount) Load() bool { return c.n.Load() > 0 }

var app = &App{}

func init() { runtime.LockOSThread() }

func main() {
	for _, a := range os.Args[1:] {
		if a == "--auto-clean" { // started by the automatic cleaning task: no window
			autoCleanMain()
			return
		}
	}
	updated := afterUpdate(os.Args[1:]) // started by the old version: wait until it has closed
	if !acquireSingleInstance() {
		return
	}
	if exe, err := os.Executable(); err == nil {
		go cleanupOldExe(exe) // what an update left behind
	}
	app.admin = isElevated()
	initPaths()
	logInit(logDir())
	onOpEnd = func(s opSummary) {
		emit("op-end", s)
		notifyOpEnd(s)
	}
	rlog.path = dataDirPath("recycled.json")
	rlog.load()
	logEvent("app", "info", "", T("log.start", appVersion), map[string]any{"os": osName(), "admin": app.admin, "afterUpdate": updated})
	buildCatalog()
	buildTweaks()
	settings := getSettings()
	applySettings(settings)
	ownFrame := settings["classicFrame"] != true // the app's own title bar unless Windows' one was chosen
	registerWindowMessages()
	darkMenus()

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
		OnMessage:  winHook,
		WindowOptions: webview2.WindowOptions{
			Title:  appName,
			Width:  uint(width),
			Height: uint(height),
			IconId: 1,
			Center: true,
			OnCreated: func(h uintptr) {
				app.hwnd = h
				styleWindow(h)
				if ownFrame {
					applyFrame(h, true) // before the first paint: no Windows title bar shows up
				}
			},
		},
	})
	if w == nil {
		webviewMissing()
		return
	}
	app.w = w
	defer w.Destroy()
	applyFrame(app.hwnd, ownFrame) // now the page leaves its top edge for resizing
	if boot := slices.Contains(os.Args[1:], "--tray"); boot || startTray.Load() {
		trayStart(!boot) // started with Windows (or the option is on): straight to the notification area
	} else {
		bringToFront(app.hwnd)
	}
	go bootGet()     // closing goes to the tray when the app starts with Windows
	go memAutoLoop() // started after app.w is set, so its events can reach the page
	go gameAutoLoop()
	go updateLoop()
	go throttleLoop()
	go bgTameLoop()
	go startupWatchLoop()
	w.SetSize(min(int(980*scale), width), min(int(660*scale), height), webview2.HintMin)

	binds := map[string]any{
		"appInfo":        appInfo,
		"startAnalyze":   startAnalyze,
		"startClean":     startClean,
		"systemInfo":     systemInfo,
		"netState":       getNetState,
		"dnsSet":         dnsSet,
		"dnsRestore":     dnsRestore,
		"dnsTest":        dnsTest,
		"netPing":        netPing,
		"quickFix":       quickFix,
		"shutdownPC":     shutdownPC,
		"dnsFlush":       dnsFlush,
		"ctxMenuList":    ctxMenuList,
		"ctxMenuSet":     ctxMenuSet,
		"autoCleanState": autoCleanState,
		"bootState":      bootGet,
		"bootSet":        bootSet,
		"autoCleanSet":   autoCleanSet,
		"autoCleanNow": func() bool {
			if app.busy.Load() || autoCleanBusy() {
				return false
			}
			go runAutoClean("now")
			return true
		},
		"stopClean":            func() bool { app.stop.Store(true); return true },
		"cleanState":           cleanState,
		"getDrives":            getDrives,
		"startTrim":            startTrim,
		"startRepair":          startRepair,
		"createRestorePoint":   startRestorePoint,
		"openSystemRestore":    func() bool { shellOpen(sys32("rstrui.exe")); return true },
		"getSettings":          getSettings,
		"saveSettings":         saveAndApplySettings,
		"getMemory":            getMemory,
		"cleanMemory":          cleanMemoryLogged,
		"openLink":             openLink,
		"checkBlockers":        checkBlockers,
		"closeBlockers":        closeBlockersLogged,
		"forceCloseBlockers":   forceCloseBlockersLogged,
		"previewItem":          previewItem,
		"showInFolder":         showInFolder,
		"listStartup":          listStartup,
		"bootPerf":             bootPerfRead,
		"throttleState":        throttleState,
		"batteryInfo":          batteryInfo,
		"batteryReport":        batteryReport,
		"trimEnable":           trimEnable,
		"bgTameState":          bgTameState,
		"startupDelay":         startupDelay,
		"debloatInfo":          debloatInfo,
		"debloatState":         debloatState,
		"debloatRun":           debloatRun,
		"powerProfile":         powerProfile,
		"setStartup":           setStartup,
		"restoreStartup":       restoreStartup,
		"startupLog":           startupLogList,
		"diskVolumes":          diskVolumes,
		"diskScan":             diskScan,
		"diskCancel":           diskCancel,
		"diskChildren":         diskChildren,
		"diskTop":              diskTop,
		"diskTypes":            diskTypes,
		"diskRefresh":          diskRefresh,
		"diskDelete":           diskDelete,
		"diskPickFolder":       diskPickFolder,
		"tweakList":            tweakList,
		"tweakStates":          tweakStates,
		"tweakPresets":         tweakPresets,
		"liveStats":            liveStats,
		"gpuState":             gpuStateRead,
		"gpuSensors":           gpuSensorsRead,
		"gpuNvDrivers":         gpuNvDrivers,
		"gpuNvDownload":        gpuNvDownload,
		"gpuCancelDownload":    gpuCancelDownload,
		"gpuRunInstaller":      gpuRunInstaller,
		"gpuUseDriver":         gpuUseDriver,
		"gpuDeletePackage":     gpuDeletePackage,
		"gpuRemove":            gpuRemove,
		"gpuRestoreBackup":     gpuRestoreBackup,
		"gpuDeleteBackup":      gpuDeleteBackup,
		"gpuWU":                gpuWU,
		"gpuSetRefresh":        gpuSetRefresh,
		"gpuKeepRefresh":       gpuKeepRefresh,
		"gpuOpenBackups":       gpuOpenBackups,
		"gpuOpenDetails":       gpuOpenDetails,
		"restartPC":            restartPC,
		"wuStatus":             wuStatus,
		"wuSearch":             wuSearch,
		"wuInstall":            wuInstall,
		"wuCancel":             wuCancel,
		"wuOpenSettings":       wuOpenSettings,
		"nmState":              nmState,
		"nmSet":                nmSet,
		"nmInstall":            nmInstall,
		"nmSetBalance":         nmSetBalance,
		"benchRun":             benchRun,
		"benchStop":            benchStop,
		"benchSave":            benchSave,
		"openGraphicsSettings": openGraphicsSettings,
		"benchGPUSample":       benchGPUSample,
		"benchHistory":         benchHistory,
		"benchClear":           benchClearHistory,
		"extScan":              extScan,
		"extOpen":              extOpen,
		"extShowFolder":        extShowFolder,
		"reportMake":           reportMake,
		"reportOpen":           reportOpen,
		"reportShow":           reportShow,
		"speedRun":             speedRun,
		"speedStop":            speedStop,
		"nmOpenSite":           nmOpenSite,
		"defenderHelp":         defenderHelp,
		"openWindowsSecurity":  openWindowsSecurity,
		"reportFalsePositive":  reportFalsePositive,
		"setTweak":             setTweak,
		"debloatApps":          debloatApps,
		"tweakHistory":         tweakHistory,
		"undoTweak":            undoTweak,
		"appliedTweaks":        appliedTweaks,
		"undoAllTweaks":        undoAllTweaks,
		"exportTweakProfile":   exportTweakProfile,
		"importTweakProfile":   importTweakProfile,
		"restartExplorer":      restartExplorerLogged,
		// 4.3: passwords, blue screens, the frame counter, recovering files
		"wifiNets":       wifiNets,
		"pwnedCheck":     pwnedCheck,
		"bsodRecent":     bsodRecent,
		"memTest":        memTest,
		"fpsState":       fpsState,
		"fpsInstall":     fpsInstall,
		"fpsStart":       fpsStart,
		"fpsStop":        fpsStop,
		"fpsOverlay":     fpsSetOverlay,
		"recoverState":   recoverState,
		"recoverInstall": recoverInstall,
		"recoverStart":   recoverStart,
		"recoverStop":    recoverStop,
		"recoverOpen":    recoverOpen,
		// 4.5: virus scan (Defender's engine), game server ping
		"avState":     avStateRead,
		"avScan":      avScan,
		"avStop":      avStop,
		"avRemove":    avRemove,
		"avCheck":     avCheck,
		"avFix":       avFix,
		"clamInstall": clamInstall,
		"gamePing":    gamePing,
		// 4.7: the junk reminder, game sessions
		"notify": func(title, text string) bool {
			app.w.Dispatch(func() { trayNotify(title, text) })
			return true
		},
		"gameSessions": gameSessions,
		// 4.9: Windows Update control, locked folders
		"wuCtlState":     wuCtlState,
		"wuPause":        wuPause,
		"wuSecurityOnly": wuSecurityOnly,
		"lockList":       lockList,
		"lockFolder":     lockFolder,
		"unlockFolder":   unlockFolder,
		// Logs & error reports
		"logOps":        logOps,
		"logEntries":    logEntries,
		"logProblems":   logRecentProblems,
		"logClear":      logClear,
		"logClient":     logClient,
		"logOpenFolder": logOpenFolder,
		"copyText":      copyText,
		"saveTextFile":  saveTextFile,
		// Process Manager
		"procList":         procList,
		"procPriority":     procPriority,
		"procEco":          procEco,
		"procPause":        procPause,
		"procCtlState":     procCtlState,
		"prioRules":        prioRules,
		"prioForget":       prioForget,
		"procEnd":          procEnd,
		"procOpenLocation": procOpenLocation,
		"sysVitals":        sysVitals,
		// Large Files and Duplicate Finder
		"findPlaces":     findPlaces,
		"findStart":      findStart,
		"findCancel":     findCancel,
		"findResult":     findResultOf,
		"findDelete":     findDelete,
		"findPickFolder": findPickFolder,
		// Restore Center
		"restoreOverview":     restoreOverview,
		"restorePointList":    restorePointList,
		"restoreToPoint":      restoreToPoint,
		"restoreFiles":        restoreFiles,
		"openRecycleBin":      openRecycleBin,
		"deleteRestorePoints": deleteRestorePoints,
		// updates from GitHub
		"checkUpdate":     checkUpdate,
		"installUpdate":   installUpdate,
		"openReleasePage": openReleasePage,
		"changelog":       changelog,
		// real icons of apps, files and folders
		"iconsFor": iconsFor,
		// Programs: uninstaller with leftover scan, Store apps, registry backups
		"appsList":           appsList,
		"storeApps":          storeApps,
		"appsMeasure":        appsMeasure,
		"uninstStart":        uninstStart,
		"uninstContinue":     uninstContinue,
		"uninstRemove":       uninstRemove,
		"uninstFinish":       uninstFinish,
		"uninstOpenLocation": uninstOpenLocation,
		"regBackups":         regBackups,
		"regRestore":         regRestore,
		"openBackupFolder":   func() bool { _ = os.MkdirAll(regBackupDir(), 0o755); shellOpen(regBackupDir()); return true },
		// program updates through winget
		"wingetUpdates":    wingetUpdates,
		"wingetUpgrade":    wingetUpgrade,
		"wingetOpen":       wingetOpen,
		"wingetSite":       wingetSite,
		"wingetStop":       wingetStop,
		"openAppInstaller": openAppInstaller,
		// disk health (S.M.A.R.T.), the details of a disk, drive optimization
		"diskHealth":     diskHealthList,
		"diskDetails":    diskDetails,
		"openDrive":      openDrive,
		"analyzeDrive":   analyzeDrive,
		"optimizeDrives": optimizeDrives,
		"optimizeStop":   optimizeStop,
		// game mode
		"gameApps":  gameApps,
		"gameState": gameState,
		"gameStart": gameStart,
		"gameStop":  gameStop,
		// empty folders and broken shortcuts
		"tidyPlaces": tidyPlaces,
		"emptyScan":  emptyScan,
		"brokenScan": brokenScan,
		"tidyDelete": tidyDelete,
		// the window: own title bar, tray
		"winDrag":     winDrag,
		"winMinimize": winMinimize,
		"winMaximize": winMaximize,
		"winClose":    winClose,
		"winTray":     winTray,
		"winFrame":    winFrame,
		// what the page sees (visible, painted, splash): to stderr, for the tests
		"winDiag": func(d map[string]any) bool {
			b, _ := json.Marshal(d)
			fmt.Fprintf(os.Stderr, "DIAG %s\n", b)
			return true
		},
		"winReady": func() map[string]any {
			w.Dispatch(func() {
				if !win.inTray.Load() { // started in the tray: it stays there
					bringToFront(app.hwnd) // the page is there: in front, even if loading took a while
				}
			})
			return map[string]any{"own": win.own.Load(), "max": win.max.Load(), "active": true}
		},
	}
	webview2.BindingPanic = func(method string, r any, stack []byte) {
		logEvent("app", "error", method, T("err.unexpected", r), map[string]any{"stack": string(stack)})
	}
	for name, f := range binds {
		must(w.Bind(name, f))
	}

	if u := pageURL(dataDir); u != "" {
		w.Navigate(u)
	} else {
		w.SetHtml(indexHTML)
	}
	w.Run()
	if fpsState().Running {
		fpsStop()
	}
	recoverStop()
	bgTameStep(false) // the held-back processes get their priority back
	resumeAllPaused()
	if win.inTray.Load() {
		trayRemove()
	}
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
	updOff.Store(m["updates"] == false)
	startTray.Store(m["startTray"] == true)
	app.trayClose.Store(startTray.Load() || bootOn.Load()) // closing hides it in the tray
	bgOff.Store(m["bgTame"] == false)
	if g, ok := m["game"].(map[string]any); ok {
		gmCfgCache.Store(g)
		if g["prio"] != true {
			go gamePrioClear() // the option turned off: Windows' own setting goes too
		}
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
	return savePageSettings(m)
}

// openLink opens only fixed links: the credits, help and the card makers' driver pages.
func openLink(url string) bool {
	switch url {
	case "https://github.com/jchv/go-webview2", "https://go.dev", "https://github.com/rsms/inter",
		"https://github.com/henrypp/memreduct", "https://www.bleachbit.org", "https://github.com/MoscaDotTo/Winapp2",
		"https://github.com/Parcoil/Sparkle", "https://github.com/ChrisTitusTech/winutil", "https://github.com/NVIDIA/nvapi",
		"https://github.com/meetrevision/playbook", "https://github.com/Ki4o112/xKi4o-Cleaner/issues/new",
		"https://www.nvidia.com/en-us/drivers/", "https://www.amd.com/en/support/download/drivers.html",
		"https://www.intel.com/content/www/us/en/download-center/home.html":
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
	if app.trayClose.Load() && !win.quitting.Load() {
		winTray(!app.trayHinted.Swap(true)) // started with Windows: closing hides it, game mode keeps watching
		return false
	}
	win.quitting.Store(false)
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
		"build":   osBuild(),
		"drive":   sysDriveInfo(),
		"year":    time.Now().Year(),
		"profile": P.profile,
		"logDir":  olog.dir,
		"pc":      pcName(),
		"user":    userDisplayName(),
	}
}

// ---------------------------------------------------------------- logged wrappers

func cleanMemoryLogged(regions []string) memResult {
	op := beginOp("memory", "", T("log.mem"), map[string]any{"regions": regions})
	res := cleanMemory(regions)
	op.info("", T("log.ramFreed", sizeText(res.Freed)), map[string]any{"freed": res.Freed})
	op.end("", nil)
	return res
}

func closeBlockersLogged(keys []string, reopen bool) []closeResult {
	op := beginOp("process", "", T("log.blkClose"), map[string]any{"apps": keys, "reopen": reopen})
	res := closeBlockers(keys, reopen)
	logCloseResults(op, res)
	return res
}

func forceCloseBlockersLogged(keys []string) []closeResult {
	op := beginOp("process", "", T("log.blkForce"), map[string]any{"apps": keys})
	res := forceCloseBlockers(keys)
	logCloseResults(op, res)
	return res
}

func logCloseResults(op *opHandle, res []closeResult) {
	n := 0
	for _, r := range res {
		if r.Closed {
			n++
			op.info(r.Name, T("pm.ended"), nil)
		} else {
			op.warnf(r.Name, r.Note, nil)
		}
	}
	op.end(T("pm.sum", n, len(res)), nil)
}

// Long jobs worth a notification when they end while the app is in the tray.
var notifyKinds = map[string]bool{"clean": true, "optimize": true, "winget": true, "repair": true, "uninstall": true, "dupes": true, "disk": true}

func notifyOpEnd(s opSummary) {
	if !notifyKinds[s.Kind] || s.End-s.Start < 5000 && s.Kind != "clean" {
		return
	}
	text := s.Summary
	if text == "" {
		text = T("tray.done")
	}
	if s.State == "error" {
		text = T("tray.failed") + " " + text
	}
	title := s.Title
	if app.w != nil {
		app.w.Dispatch(func() { trayNotify(title, text) })
	}
}

func restartExplorerLogged() bool {
	op := beginOp("tweak", "", T("log.explorer"), nil)
	ok := restartExplorer()
	if !ok {
		op.fail("", T("log.explorerFail"), nil)
	}
	op.end("", nil)
	return ok
}

func startAnalyze() bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		emit("analyze-start", map[string]int{"total": len(catalog)})
		pending := false
		func() {
			defer func() { _ = recover() }()
			pending = analyzeAll(app.admin, func(r report) { emit("item", r) })
		}()
		app.busy.Store(false)
		reason := ""
		if pending {
			reason = wuBusy(runningProcs())
		}
		emit("analyze-done", map[string]any{"drive": sysDriveInfo(), "pending": pending, "pendingReason": reason})
	}()
	return true
}

type CleanOpts struct {
	ResetBase    bool     `json:"resetBase"`
	RestorePoint bool     `json:"restorePoint"`
	Memory       bool     `json:"memory"`   // "clean everything": also free RAM
	Trim         bool     `json:"trim"`     // "clean everything": also TRIM SSDs
	MemRegions   []string `json:"regions"`  // memory regions for Memory
	DryRun       bool     `json:"dryRun"`   // Preview: report what would go, delete nothing
	Exclude      []string `json:"exclude"`  // files and folders the user unticked in the preview
	OpenApps     string   `json:"openApps"` // "skip": categories of open apps are skipped; "partial": cleaned, files in use stay
	Reopen       bool     `json:"reopen"`   // start again the apps closed for this cleaning
	Quiet        bool     `json:"-"`        // automatic cleaning: no news to the page's Cleanup rows
}

// stepMsg reports the state and the result of one category.
type stepMsg struct {
	ID          string  `json:"id"`
	State       string  `json:"state"` // running, done, skipped, failed
	Freed       int64   `json:"freed,omitempty"`
	Files       int     `json:"files,omitempty"`
	Locked      int     `json:"locked,omitempty"`
	LockedBytes int64   `json:"lockedBytes,omitempty"`
	Denied      int     `json:"denied,omitempty"`
	DeniedBytes int64   `json:"deniedBytes,omitempty"`
	Failed      int     `json:"failed,omitempty"`
	FailedBytes int64   `json:"failedBytes,omitempty"`
	Issues      []issue `json:"issues,omitempty"`
	Note        string  `json:"note,omitempty"`
	Tool        bool    `json:"tool,omitempty"`    // a Windows tool did the work: Freed is measured, no file list
	Planned     bool    `json:"planned,omitempty"` // dry run of a Windows tool: estimate, nothing ran
}

func (m *stepMsg) setStats(st stats) {
	m.Freed, m.Files = st.bytes, st.files
	m.Locked, m.LockedBytes = st.locked, st.lockedBytes
	m.Denied, m.DeniedBytes = st.denied, st.deniedBytes
	m.Failed, m.FailedBytes = st.failed, st.failedBytes
	m.Issues = st.issues
}

func startClean(ids []string, opts CleanOpts) bool {
	if autoCleanBusy() {
		return false // the automatic cleaning is running right now
	}
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	if !opts.DryRun {
		app.working.Store(true)
	}
	app.stop.Store(false)
	setPhase("start", "")
	title := T("log.clean")
	if opts.DryRun {
		title = T("log.preview")
	}
	op := beginOp("clean", "", title, map[string]any{"categories": ids, "dryRun": opts.DryRun, "restorePoint": opts.RestorePoint,
		"openApps": opts.OpenApps, "memory": opts.Memory, "trim": opts.Trim, "excluded": len(opts.Exclude), "admin": app.admin})
	go func() {
		emit("clean-start", map[string]any{"count": len(ids), "dryRun": opts.DryRun, "op": op.id})
		before := freeBytes()
		var sum cleanSummary
		func() {
			defer func() {
				if r := recover(); r != nil {
					op.panicked(r)
					emit("clean-error", map[string]any{"error": T("err.unexpected", r), "op": op.id})
				}
			}()
			sum = runClean(ids, opts, op)
		}()
		setPhase("", "")
		finishClosedApps(opts.Reopen && !opts.DryRun)
		if !opts.DryRun {
			app.working.Store(false)
		}
		app.busy.Store(false)
		drive := sysDriveInfo()
		sumKey := "log.cleanSum"
		if opts.DryRun {
			sumKey = "log.previewSum"
		}
		if app.stop.Load() {
			op.warnf("", T("note.stopped"), nil)
		}
		op.end(T(sumKey, sizeText(sum.freed), sum.files), map[string]any{"freed": sum.freed, "files": sum.files, "locked": sum.locked,
			"denied": sum.denied, "failed": sum.failed, "ram": sum.ram, "driveDelta": int64(drive.Free) - before})
		if !opts.DryRun {
			addLifetime(sum.freed)
		}
		emit("clean-done", map[string]any{"op": op.id,
			"freed": sum.freed, "files": sum.files, "locked": sum.locked, "denied": sum.denied, "failed": sum.failed,
			"ram": sum.ram, "drive": drive, "driveDelta": int64(drive.Free) - before,
			"restart": sum.restart, "stopped": app.stop.Load(), "dryRun": opts.DryRun,
		})
	}()
	return true
}

var (
	phaseMu   sync.Mutex
	phaseName string
	phaseItem string
	phaseAt   time.Time
)

func setPhase(name, item string) {
	phaseMu.Lock()
	phaseName, phaseItem, phaseAt = name, item, time.Now()
	phaseMu.Unlock()
}

// cleanState lets the page ask what a running cleaning is doing (used when
// no news arrived for a while).
func cleanState() map[string]any {
	phaseMu.Lock()
	defer phaseMu.Unlock()
	return map[string]any{"busy": app.busy.Load(), "phase": phaseName, "item": phaseItem, "secs": int(time.Since(phaseAt).Seconds())}
}

type cleanSummary struct {
	freed, ram            int64
	files, locked, denied int
	failed                int
	restart               bool
}

func runClean(ids []string, opts CleanOpts, op *opHandle) (sum cleanSummary) {
	send := emit
	if opts.Quiet {
		send = func(string, any) {}
	}
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
	excl := map[string]bool{}
	for _, p := range opts.Exclude {
		if p != "" {
			excl[normPath(p)] = true
		}
	}

	rpOK := true
	if deep && opts.RestorePoint && app.admin && !opts.DryRun {
		send("restore", map[string]any{"state": "running"})
		setPhase("restore", "")
		app.critical.Store(true)
		rpOK = restorePointTicking(func(sec int) { send("restore", map[string]any{"state": "running", "secs": sec}) })
		app.critical.Store(false)
		if rpOK {
			send("restore", map[string]any{"state": "done"})
			op.info("", T("log.rpDone"), nil)
		} else {
			send("restore", map[string]any{"state": "failed"})
			op.fail("", T("log.rpFail"), nil)
		}
	}

	for _, it := range chosen {
		if app.stop.Load() {
			break
		}
		skip := func(note string) {
			send("step", stepMsg{ID: it.id, State: "skipped", Note: note})
			op.warnf(it.id, note, map[string]any{"skipped": true})
		}
		procs := runningProcs()
		if it.deep && !rpOK {
			skip(T("skip.rp"))
			continue
		}
		if it.admin && !app.admin {
			skip(T("blocked.admin"))
			continue
		}
		if it.busy != nil {
			if why := it.busy(procs); why != "" {
				skip(why)
				continue
			}
		}
		if it.runningIn(procs) && (opts.OpenApps != "partial" || it.targets == nil || it.clean != nil) {
			skip(T("skip.open"))
			continue
		}

		send("step", stepMsg{ID: it.id, State: "running"})
		setPhase("item", it.id)
		lastNote := ""
		ctx := &cleanCtx{
			opts: opts,
			stop: func() bool { return app.stop.Load() },
			note: func(n string) {
				if n != lastNote {
					lastNote = n
					send("step", stepMsg{ID: it.id, State: "running", Note: n})
				}
			},
			cl: &cleaner{stop: func() bool { return app.stop.Load() }, dry: opts.DryRun, exclude: excl},
		}
		msg, err := cleanItem(it, ctx)
		msg.ID = it.id
		sum.freed += msg.Freed
		sum.files += msg.Files
		sum.locked += msg.Locked
		sum.denied += msg.Denied
		sum.failed += msg.Failed
		switch {
		case err != nil:
			msg.State, msg.Note = "failed", err.Error()
		case app.stop.Load():
			msg.State, msg.Note = "done", T("note.stopped")
		default:
			msg.State = "done"
			if it.restart && !opts.DryRun {
				sum.restart = true
			}
		}
		send("step", msg)
		logStep(op, it.id, msg, opts.DryRun)
	}

	if opts.DryRun {
		return sum
	}
	if opts.Memory && !app.stop.Load() && app.admin {
		send("step", stepMsg{ID: "ram", State: "running"})
		setPhase("item", "ram")
		res := cleanMemory(opts.MemRegions)
		sum.ram = res.Freed
		send("step", stepMsg{ID: "ram", State: "done", Freed: res.Freed})
		op.info("ram", T("log.ramFreed", sizeText(res.Freed)), map[string]any{"freed": res.Freed})
	}
	if opts.Trim && !app.stop.Load() && app.admin {
		send("step", stepMsg{ID: "trim", State: "running"})
		setPhase("item", "trim")
		for _, d := range getDrives() {
			if d.Media != "HDD" {
				_, _, _ = runQuiet(15*time.Minute, sys32("defrag.exe"), d.Letter+":", "/L")
			}
		}
		send("step", stepMsg{ID: "trim", State: "done"})
		op.info("trim", T("log.trimDone"), nil)
	}
	return sum
}

// logStep writes the result of one category to the operation log: what
// was freed, and every file that stayed with the reason (the first ones).
func logStep(op *opHandle, id string, m stepMsg, dry bool) {
	data := map[string]any{"freed": m.Freed, "files": m.Files}
	if m.Locked+m.Denied+m.Failed > 0 {
		data["locked"], data["denied"], data["failed"] = m.Locked, m.Denied, m.Failed
		n := min(len(m.Issues), 25)
		data["issues"] = m.Issues[:n]
	}
	if m.Note != "" {
		data["note"] = m.Note
	}
	key := "log.stepDone"
	if dry {
		key = "log.stepDry"
	}
	switch {
	case m.State == "failed":
		op.fail(id, m.Note, data)
	case m.Locked+m.Denied+m.Failed > 0:
		op.warnf(id, T("log.stepLeft", sizeText(m.Freed), m.Files, m.Locked, m.Denied, m.Failed), data)
	default:
		op.info(id, T(key, sizeText(m.Freed), m.Files), data)
	}
}

// cleanItem cleans (or, in a dry run, inspects) one category.
func cleanItem(it *item, c *cleanCtx) (msg stepMsg, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s", T("err.unexpected", r))
		}
	}()
	if it.clean != nil {
		if c.cl.dry {
			return planItem(it, c), nil
		}
		f, _, e := it.clean(c)
		msg.setStats(c.files)
		msg.Freed = f
		msg.Tool = true
		return msg, e
	}
	if it.targets == nil {
		return msg, nil
	}
	msg.setStats(cleanWith(it.targets(), c.cl))
	return msg, nil
}

// planItem is the dry run of a category that a Windows tool cleans: it says
// what would happen without running anything.
func planItem(it *item, c *cleanCtx) stepMsg {
	m := stepMsg{Planned: true, Tool: true}
	switch it.id {
	case "wucache":
		m.setStats(cleanWith(it.targets(), c.cl))
		m.Freed += deliveryOptimizationSize()
		m.Tool, m.Note = false, T("dry.wu")
	case "nvdrivers":
		var vers []string
		for _, d := range findOldNvidiaDrivers(true) {
			m.Freed += d.size
			m.Files++
			if d.version != "" {
				vers = append(vers, d.version)
			}
		}
		m.Note = T("dry.drivers", strings.Join(vers, ", "))
	default:
		if it.size != nil {
			if s, _ := it.size(); s > 0 {
				m.Freed = s
			}
		}
		m.Note = T("dry.tool")
	}
	return m
}

func startTrim() bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	app.working.Store(true)
	op := beginOp("optimize", "", T("log.trim"), nil)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				op.panicked(r)
			}
			op.end("", nil)
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
				op.warnf(d.Letter+":", T("trim.unsupported"), map[string]any{"code": code})
			} else {
				op.info(d.Letter+":", T("log.trimDone"), nil)
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
	op := beginOp("repair", kind, T("log.repair"), map[string]any{"kind": kind})
	go func() {
		ok, summary := false, ""
		func() {
			defer func() {
				if r := recover(); r != nil {
					summary = fmt.Sprint(r)
					op.panicked(r)
				}
			}()
			ok, summary = runRepair(kind)
		}()
		if ok {
			op.info(kind, summary, nil)
		} else {
			op.fail(kind, summary, nil)
		}
		op.end(summary, nil)
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
	op := beginOp("restore", "", T("log.rpCreate"), nil)
	go func() {
		ok := false
		defer func() {
			if ok {
				op.info("", T("log.rpDone"), nil)
			} else if app.admin {
				op.fail("", T("log.rpFail"), nil)
			} else {
				op.fail("", T("rp.admin"), nil)
			}
			op.end("", nil)
		}()
		func() {
			defer func() {
				if r := recover(); r != nil {
					op.panicked(r)
				}
			}()
			app.critical.Store(true)
			ok = app.admin && restorePointTicking(func(sec int) { emit("rp-tick", map[string]int{"secs": sec}) })
		}()
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
