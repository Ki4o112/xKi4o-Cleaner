//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// winget is the Windows Package Manager (part of "App Installer"). Updates
// are found and installed by it; this app only shows them and starts it.

type wgList struct {
	Available bool       `json:"available"` // winget is installed
	Updates   []wgUpdate `json:"updates"`
	Self      []string   `json:"self,omitempty"` // left out: they update themselves (names)
	Error     string     `json:"error,omitempty"`
	Checked   int64      `json:"checked"` // unix ms
}

var wg struct {
	mu      sync.Mutex // one winget at a time
	cache   wgList
	running atomic.Bool
	stop    atomic.Bool
}

func wingetExe() string {
	if p := filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\WindowsApps\winget.exe`); fileExistsAny(p) {
		return p
	}
	if p, err := exec.LookPath("winget.exe"); err == nil {
		return p
	}
	return ""
}

// runWinget runs winget hidden; its output is UTF-8 (older builds: the console code page).
func runWinget(timeout time.Duration, args ...string) (string, int, error) {
	exe := wingetExe()
	if exe == "" {
		return "", -1, os.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hiddenCmd(ctx, exe, args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	b := []byte(out.String())
	s := string(b)
	if !utf8.Valid(b) {
		s = oemDecode(b)
	}
	return s, code, err
}

// wingetUpdates asks winget which installed programs have a newer version.
// fresh=false gives the last answer if it is under 30 minutes old.
func wingetUpdates(fresh bool) wgList {
	if !fresh {
		wg.mu.Lock()
		c := wg.cache
		wg.mu.Unlock()
		if c.Checked > 0 && time.Since(time.UnixMilli(c.Checked)) < 30*time.Minute {
			return c
		}
	}
	if wg.running.Load() {
		wg.mu.Lock()
		c := wg.cache
		wg.mu.Unlock()
		return c
	}
	wg.mu.Lock()
	defer wg.mu.Unlock()
	res := wgList{Checked: time.Now().UnixMilli(), Updates: []wgUpdate{}}
	if wingetExe() == "" {
		wg.cache = res
		return res
	}
	res.Available = true
	out, code, err := runWinget(3*time.Minute, "upgrade", "--accept-source-agreements")
	if err != nil && code == -1 {
		res.Error = err.Error()
		wg.cache = res
		return res
	}
	ups := parseWingetUpgrade(out)
	const noUpdates = 0x8A15002B // "no applicable upgrade found"
	if len(ups) == 0 && code != 0 && code != noUpdates && !strings.Contains(out, "-----") {
		res.Error = wingetMessage(out) // no table: a real failure
	}
	for _, u := range ups {
		if strings.HasSuffix(u.ID, "…") || strings.HasSuffix(u.ID, "...") {
			tmp := filepath.Join(os.TempDir(), "xki4o-winget-export.json")
			_, _, _ = runWinget(2*time.Minute, "export", "-o", tmp, "--accept-source-agreements")
			b, _ := os.ReadFile(tmp)
			_ = os.Remove(tmp)
			ups = fullIDs(ups, b)
			break
		}
	}
	for _, u := range ups {
		if wingetSelfUpdating(u.ID) {
			res.Self = append(res.Self, u.Name)
			continue
		}
		res.Updates = append(res.Updates, u)
	}
	wg.cache = res
	return res
}

// wingetGone: after its update, the program is not installed any more
// (its installer removed the old version and did not put the new one).
func wingetGone(id string) bool {
	_, code, _ := runWinget(2*time.Minute, "list", "--id", id, "--exact", "--accept-source-agreements", "--disable-interactivity")
	const noneFound = 0x8A150014 // APPINSTALLER_CLI_ERROR_NO_APPLICATIONS_FOUND
	return code != -1 && uint32(code) == noneFound
}

type wgOpts struct {
	Close   bool `json:"close"`   // close the programs that are open first (and start them again after)
	Install bool `json:"install"` // install new programs (the Install tab) instead of updating
}

type wgOpenApp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// wgProgramProcs: the running processes of the installed program a winget
// row stands for (by its name in the list of installed programs).
func wgProgramProcs(id string) (string, []rawProc) {
	wg.mu.Lock()
	var name string
	for _, u := range wg.cache.Updates {
		if u.ID == id {
			name = u.Name
		}
	}
	wg.mu.Unlock()
	if name == "" {
		return "", nil
	}
	appsCache.mu.Lock()
	apps := append([]instApp(nil), appsCache.apps...)
	appsCache.mu.Unlock()
	if len(apps) == 0 {
		apps = appsList().Apps
	}
	var a *instApp
	for i := range apps {
		if sameProgram(apps[i].Name, name) {
			a = &apps[i]
			break
		}
	}
	if a == nil || (a.Location == "" && len(a.Exes) == 0) {
		return name, nil
	}
	loc := a.Location
	if loc != "" && (len(filepath.Clean(loc)) <= 3 || strings.EqualFold(filepath.Clean(loc), os.Getenv("ProgramFiles")) || strings.EqualFold(filepath.Clean(loc), os.Getenv("ProgramFiles(x86)"))) {
		loc = "" // a whole drive or Program Files is not the program's folder
	}
	exes := map[string]bool{}
	for _, e := range a.Exes {
		if b := strings.ToLower(filepath.Base(e)); !genericExe(b) {
			exes[b] = true
		}
	}
	raw, err := queryProcesses()
	if err != nil {
		return name, nil
	}
	self := windows.GetCurrentProcessId()
	var out []rawProc
	for _, p := range raw {
		if p.pid == self || p.pid <= 4 || windowsProcs[strings.ToLower(p.name)] {
			continue
		}
		if !exes[strings.ToLower(p.name)] && loc == "" {
			continue
		}
		path := processPath(p.pid)
		if path == "" || underWindowsDir(path) {
			continue
		}
		if (loc != "" && under(path, loc)) || exes[strings.ToLower(filepath.Base(path))] {
			out = append(out, p)
		}
	}
	return name, out
}

// wingetOpen: which of these programs are open right now (their update
// would fail or ask to close them). Browsers update themselves while open.
func wingetOpen(ids []string) []wgOpenApp {
	out := []wgOpenApp{}
	for _, id := range ids {
		if !validWingetID(id) || selfUpdater(id) != "" {
			continue
		}
		if name, ps := wgProgramProcs(id); len(ps) > 0 {
			out = append(out, wgOpenApp{ID: id, Name: name, Count: len(ps)})
		}
	}
	return out
}

// closeForUpdate closes a program's processes (politely, then by force)
// and returns how to start it again.
func closeForUpdate(ps []rawProc) []gmClosed {
	pids := map[uint32]string{}
	before := map[uint32]rawProc{}
	var procs []gmProc
	want := map[string]bool{}
	for _, p := range ps {
		exe := strings.ToLower(p.name)
		pids[p.pid] = exe
		before[p.pid] = p
		want[exe] = true
		procs = append(procs, gmProc{PID: p.pid, PPID: p.ppid, Created: p.created, Exe: exe, Path: processPath(p.pid), Cmd: processCommandLine(p.pid)})
	}
	windowed := windowPIDs()
	var reopen []gmClosed
	for _, r := range gmRoots(procs, want) {
		if r.Path != "" && windowed[r.PID] {
			reopen = append(reopen, gmClosed{Exe: r.Exe, Path: r.Path, Cmd: r.Cmd})
		}
	}
	left := endPIDs(pids, before, false, 8*time.Second)
	if len(left) > 0 {
		rest := map[uint32]string{}
		for pid := range left {
			rest[pid] = pids[pid]
		}
		endPIDs(rest, before, true, 0)
	}
	return reopen
}

// runSelfUpdater runs a browser's own updater ("/ua /installsource scheduler",
// what its scheduled task runs). The browser finishes the update when it restarts.
func runSelfUpdater(kind string) (string, int, error) {
	pf86, pf, local := os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramW6432"), os.Getenv("LOCALAPPDATA")
	if pf == "" {
		pf = os.Getenv("ProgramFiles")
	}
	var cands []string
	switch kind {
	case "edge":
		cands = []string{filepath.Join(pf86, `Microsoft\EdgeUpdate\MicrosoftEdgeUpdate.exe`), filepath.Join(pf, `Microsoft\EdgeUpdate\MicrosoftEdgeUpdate.exe`)}
	case "chrome":
		cands = []string{filepath.Join(pf86, `Google\Update\GoogleUpdate.exe`), filepath.Join(pf, `Google\Update\GoogleUpdate.exe`), filepath.Join(local, `Google\Update\GoogleUpdate.exe`)}
	case "brave":
		cands = []string{filepath.Join(pf86, `BraveSoftware\Update\BraveUpdate.exe`), filepath.Join(pf, `BraveSoftware\Update\BraveUpdate.exe`), filepath.Join(local, `BraveSoftware\Update\BraveUpdate.exe`)}
	}
	for _, c := range cands {
		if fileExists(c) {
			return runQuiet(15*time.Minute, c, "/ua", "/installsource", "scheduler")
		}
	}
	return "", -1, os.ErrNotExist
}

// wingetUpgrade updates the programs one after another. The page gets a
// "winget" event for each: run, then ok or fail with winget's own message
// and, when it can be told, why and what to do.
func wingetUpgrade(ids []string, o wgOpts) bool {
	if len(ids) == 0 || wingetExe() == "" || !wg.running.CompareAndSwap(false, true) {
		return false
	}
	wg.stop.Store(false)
	var clean []string
	for _, id := range ids {
		if validWingetID(id) && (o.Install || !wingetSelfUpdating(id)) {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		wg.running.Store(false)
		return false
	}
	app.working.Store(true)
	go func() {
		defer recoverLog("winget")
		defer wg.running.Store(false)
		defer app.working.Store(false)
		op := beginOp("winget", "", T("log.wgStart", len(clean)), map[string]any{"ids": clean, "close": o.Close, "install": o.Install})
		ok := 0
		for i, id := range clean {
			if wg.stop.Load() {
				emit("winget", map[string]any{"id": id, "state": "skip"})
				continue
			}
			emit("winget", map[string]any{"id": id, "state": "run", "n": i + 1, "of": len(clean)})
			var reopen []gmClosed
			if o.Close && !o.Install && selfUpdater(id) == "" {
				if name, ps := wgProgramProcs(id); len(ps) > 0 {
					emit("winget", map[string]any{"id": id, "state": "run", "n": i + 1, "of": len(clean), "closing": true})
					reopen = closeForUpdate(ps)
					op.info(id, T("log.wgClosed", name), map[string]any{"processes": len(ps)})
				}
			}
			start := time.Now()
			tick := time.NewTicker(time.Second)
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-done:
						return
					case <-tick.C:
						emit("winget", map[string]any{"id": id, "state": "run", "n": i + 1, "of": len(clean), "secs": int(time.Since(start).Seconds())})
					}
				}
			}()
			var out string
			var code int
			var err error
			self := selfUpdater(id)
			if o.Install {
				self = ""
			}
			usedSelf := false
			if self != "" {
				out, code, err = runSelfUpdater(self)
				usedSelf = err == nil || code != -1
			}
			gone := false
			if !usedSelf {
				wg.mu.Lock()
				if o.Install {
					out, code, err = runWinget(45*time.Minute, "install", "--id", id, "--exact", "--source", wingetSource(id), "--silent",
						"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
				} else {
					out, code, err = runWinget(45*time.Minute, "upgrade", "--id", id, "--exact", "--silent",
						"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
					gone = (err == nil || code != -1) && wingetGone(id) // winget ran: the update may have removed the program
				}
				wg.mu.Unlock()
			}
			tick.Stop()
			close(done)
			for _, c := range reopen {
				if fileExists(c.Path) {
					if _, err := startAgain(c); err != nil {
						op.warnf(id, T("log.wgReopenFail", filepath.Base(c.Path)), map[string]any{"error": err.Error()})
					}
				}
			}
			msg := wingetMessage(out)
			if err != nil && code == -1 {
				msg = err.Error()
			}
			switch {
			case gone:
				msg = T("wg.gone")
				op.fail(id, msg, map[string]any{"exit": uint32(code)})
				emit("winget", map[string]any{"id": id, "state": "fail", "msg": msg, "code": uint32(code), "hint": "wg.h.gone", "site": true})
			case usedSelf && code == 0:
				ok++
				op.info(id, T("log.wgSelf"), nil)
				emit("winget", map[string]any{"id": id, "state": "ok", "msg": T("wg.selfDone"), "self": true})
			case o.Install && (uint32(code) == 0x8A15002B || uint32(code) == 0x8A150061): // already installed, nothing newer
				ok++
				op.info(id, T("wg.already"), nil)
				emit("winget", map[string]any{"id": id, "state": "ok", "msg": T("wg.already"), "self": true})
			case code == 0:
				ok++
				op.info(id, T("log.wgOK"), map[string]any{"message": msg})
				emit("winget", map[string]any{"id": id, "state": "ok", "msg": msg})
			default:
				hint := ""
				if !usedSelf {
					hint = wingetHint(uint32(code))
				}
				if usedSelf && msg == "" {
					msg = T("wg.selfFail", uint32(code))
				}
				op.fail(id, msg, map[string]any{"exit": uint32(code), "hint": hint})
				emit("winget", map[string]any{"id": id, "state": "fail", "msg": msg, "code": uint32(code), "hint": hint, "site": wingetSiteHint(hint)})
			}
		}
		op.end(T("log.wgDone", ok, len(clean)), nil)
		// what is still out of date
		wg.mu.Lock()
		wg.cache.Checked = 0
		wg.mu.Unlock()
		emit("winget", map[string]any{"state": "end", "ok": ok, "of": len(clean)})
	}()
	return true
}

// wingetSite opens the program's page (from "winget show"), for updates that
// must be done from the vendor's site.
func wingetSite(id string) bool {
	if !validWingetID(id) {
		return false
	}
	out, _, _ := runWinget(time.Minute, "show", "--id", id, "--exact", "--accept-source-agreements", "--disable-interactivity")
	u := wingetShowURL(out)
	if u == "" {
		u = "https://winget.run/pkg/" + strings.ReplaceAll(id, ".", "/")
	}
	shellOpen(u)
	return true
}

func wingetStop() bool { wg.stop.Store(true); return true }

// openAppInstaller opens App Installer (which brings winget) in the Microsoft Store.
func openAppInstaller() bool {
	shellOpen("ms-windows-store://pdp/?ProductId=9NBLGGH4NNS1")
	return true
}
