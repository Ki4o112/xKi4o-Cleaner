//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32.NewProc("GetMonitorInfoW")
)

// gmAuto: the game being played (0: none), whether game mode was turned on
// for it, and what was decided about each program seen in front.
var gmAuto struct {
	pid     uint32
	name    string
	started bool
	sess    *gameSession
	cand    uint32               // a game in front, not yet long enough
	candAt  time.Time            // since when
	notGame map[uint32]time.Time // looked at, not a game (looked at again after 10 s: a game may start in a small window)
	known   map[string]bool      // Windows' own list of games (exe paths, lower case)
	knownAt time.Time
	mu      sync.Mutex
}

// gmCfgCache: the Game mode page's settings, set from every save of the
// settings (applySettings), so the 2-second watch never reads the file.
var gmCfgCache atomic.Value // map[string]any

// gmAutoCfg: the Game mode page's settings.
func gmAutoCfg() (auto, ram, force bool, apps []string) {
	g, _ := gmCfgCache.Load().(map[string]any)
	auto, _ = g["auto"].(bool)
	ram = true
	if v, ok := g["ram"].(bool); ok {
		ram = v
	}
	force, _ = g["force"].(bool)
	if xs, ok := g["apps"].([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				apps = append(apps, s)
			}
		}
	}
	return
}

// gameAutoLoop watches for a game every 2 seconds, while game mode is on or
// may turn on by itself.
func gameAutoLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		gameAutoStep()
	}
}

func gameAutoStep() {
	defer recoverLog("game")
	auto, ram, force, apps := gmAutoCfg()
	active := gameState().Active
	gmAuto.mu.Lock()
	defer gmAuto.mu.Unlock()
	if gmAuto.pid != 0 {
		if processAlive(gmAuto.pid) {
			if s := gmAuto.sess; s != nil {
				l := liveStats()
				gpu := l.GPU.Util
				if !l.GPU.OK {
					gpu = -1
				}
				s.add(l.CPU, gpu, l.GPU.Temp, l.MemLoad)
				if v, ok := fpsLatest(); ok && v.PID == gmAuto.pid {
					s.addFPS(v.FPS, v.Low1)
				}
			}
			return
		}
		name, started := gmAuto.name, gmAuto.started
		gmAuto.pid, gmAuto.name, gmAuto.started = 0, "", false
		var last *gameSession
		if s := gmAuto.sess; s != nil && s.n > 0 {
			s.Secs = int(time.Since(time.UnixMilli(s.Start)).Seconds())
			s.Limit = gameLimit(*s)
			saveGameSession(*s)
			last = s
		}
		gmAuto.sess = nil
		if !active {
			return // turned off by hand meanwhile
		}
		ev := map[string]any{"state": "end", "game": name, "session": last}
		if started && active {
			gameStop()
			ev["stopped"] = true
		}
		if ram {
			ev["freed"] = gameRAM(T("log.gmGameEnd", name))
		}
		emit("gm-auto", ev)
		return
	}
	if !auto && !active {
		return
	}
	pid := foregroundPID()
	if pid == 0 || pid == windows.GetCurrentProcessId() {
		return
	}
	if gmAuto.cand != 0 && pid != gmAuto.cand {
		gmAuto.cand = 0 // it left the front before it settled
	}
	if gmAuto.notGame == nil || len(gmAuto.notGame) > 500 {
		gmAuto.notGame = map[uint32]time.Time{}
	}
	if at, ok := gmAuto.notGame[pid]; ok && time.Since(at) < 10*time.Second {
		return
	}
	path := processPath(pid)
	exe := strings.ToLower(path[strings.LastIndex(path, `\`)+1:])
	isGame := gameVerdict(exe, path, windowsKnowsGame(path), fullScreen3D(pid))
	if !isGame {
		gmAuto.notGame[pid] = time.Now()
		return
	}
	delete(gmAuto.notGame, pid)
	if gmAuto.cand != pid {
		gmAuto.cand, gmAuto.candAt = pid, time.Now()
		return
	}
	if time.Since(gmAuto.candAt) < gameSettle*time.Second {
		return
	}
	gmAuto.cand = 0
	name := strings.TrimSuffix(exe, ".exe")
	gmAuto.pid, gmAuto.name = pid, name
	ev := map[string]any{"state": "start", "game": name}
	gmAuto.sess = &gameSession{Game: name, Start: time.Now().UnixMilli(), GPUMax: -1, GPUAvg: -1}
	if g, _ := gmCfgCache.Load().(map[string]any); g["prio"] == true {
		ev["prio"] = gameHighPriority(path)
	}
	if !active {
		var keep map[string]bool
		if raw, err := queryProcesses(); err == nil {
			ps := make([]gmProc, 0, len(raw))
			for _, p := range raw {
				ps = append(ps, gmProc{PID: p.pid, PPID: p.ppid, Created: p.created, Exe: strings.ToLower(p.name)})
			}
			keep = gameLaunchers(ps, pid)
		}
		res := gameStart(gmOpts{Apps: withoutExes(apps, keep), Force: force})
		gmAuto.started = res.Error == "" && res.State.Active
		ev["started"] = gmAuto.started
		if ram {
			ev["freed"] = gameRAM(T("log.gmGameStart", name))
		}
	} else if ram {
		ev["freed"] = gameRAM(T("log.gmGameStart", name))
	}
	emit("gm-auto", ev)
}

// gamePlaying: a game is in front (seen by game mode) or game mode is on.
func gamePlaying() bool {
	gmAuto.mu.Lock()
	p := gmAuto.pid != 0
	gmAuto.mu.Unlock()
	return p || gameState().Active
}

func gameRAM(why string) int64 {
	op := beginOp("memory", "", why, nil)
	res := cleanMemory(gameRAMRegions)
	op.info("", T("log.ramFreed", sizeText(res.Freed)), map[string]any{"freed": res.Freed})
	op.end("", nil)
	return res.Freed
}

// processAlive reads Windows' process list (as Task Manager does) instead of
// opening the game's process: an anti-cheat watches who opens the game.
func processAlive(pid uint32) bool {
	raw, err := queryProcesses()
	if err != nil {
		return true // unknown: the game is not ended on a failed read
	}
	for _, p := range raw {
		if p.pid == pid {
			return true
		}
	}
	return false
}

// windowsKnowsGame: the exe is in Game Bar's list of games (refreshed every 5 minutes).
func windowsKnowsGame(path string) bool {
	if path == "" {
		return false
	}
	if gmAuto.known == nil || time.Since(gmAuto.knownAt) > 5*time.Minute {
		gmAuto.known, gmAuto.knownAt = map[string]bool{}, time.Now()
		if k, err := registry.OpenKey(registry.CURRENT_USER, `System\GameConfigStore\Children`, registry.ENUMERATE_SUB_KEYS); err == nil {
			names, _ := k.ReadSubKeyNames(-1)
			k.Close()
			for _, n := range names {
				if c, err := registry.OpenKey(registry.CURRENT_USER, `System\GameConfigStore\Children\`+n, registry.QUERY_VALUE); err == nil {
					if p, _, err := c.GetStringValue("MatchedExeFullPath"); err == nil && p != "" {
						gmAuto.known[strings.ToLower(p)] = true
					}
					c.Close()
				}
			}
		}
	}
	return gmAuto.known[strings.ToLower(path)]
}

// fullScreen3D: the program's window in front covers its whole screen and it
// draws with the graphics card's 3D engine.
func fullScreen3D(pid uint32) bool {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return false
	}
	var wr rect
	_, _, _ = procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	mon, _, _ := procMonitorFromWindow.Call(h, 2) // MONITOR_DEFAULTTONEAREST
	mi := struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return false
	}
	if wr.Left > mi.Monitor.Left || wr.Top > mi.Monitor.Top || wr.Right < mi.Monitor.Right || wr.Bottom < mi.Monitor.Bottom {
		return false
	}
	// whether it draws in 3D comes from Windows' counters, not from opening
	// the game: an anti-cheat watches who opens the game's process
	u, ok := gpu3DOf(pid)
	return ok && u >= 2
}

func gameSessionsPath() string { return dataDirPath("game-sessions.json") }

func gameSessionsRead() []gameSession {
	var ss []gameSession
	if b, err := os.ReadFile(gameSessionsPath()); err == nil {
		_ = json.Unmarshal(b, &ss)
	}
	return ss
}

func saveGameSession(s gameSession) {
	b, _ := json.Marshal(keepSessions(gameSessionsRead(), s))
	_ = os.MkdirAll(filepath.Dir(gameSessionsPath()), 0o755)
	_ = os.WriteFile(gameSessionsPath(), b, 0o644)
}

// gameSessions: the last games played while game mode watched (newest first).
func gameSessions() []gameSession {
	if ss := gameSessionsRead(); ss != nil {
		return ss
	}
	return []gameSession{}
}

// gameHighPriority asks Windows to start the game with "high" priority
// (Image File Execution Options\<exe>\PerfOptions, CpuPriorityClass 3): Windows
// applies it from the game's next start, and the app never opens the game's
// process, which an anti-cheat could take for a cheat. "on": already set;
// "next": set now, for the next start; "": not possible (no admin rights).
func gameHighPriority(path string) string {
	exe := filepath.Base(path)
	if !app.admin || exe == "" || exe == "." || strings.ContainsAny(exe, `\/`) {
		return ""
	}
	gamePrioMu.Lock()
	defer gamePrioMu.Unlock()
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe+`\PerfOptions`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("CpuPriorityClass"); err == nil && v == 3 {
		return "on"
	}
	if k.SetDWordValue("CpuPriorityClass", 3) != nil {
		return ""
	}
	list := gamePrioRead()
	if !slices.Contains(list, exe) {
		list = append(list, exe)
		b, _ := json.Marshal(list)
		_ = os.WriteFile(gamePrioPath(), b, 0o644)
	}
	return "next"
}

var gamePrioMu sync.Mutex

func gamePrioPath() string { return dataDirPath("game-priority.json") }

func gamePrioRead() []string {
	var l []string
	if b, err := os.ReadFile(gamePrioPath()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

// gamePrioClear takes back every priority the app asked Windows for (the
// option was turned off); keys that hold anything else stay.
func gamePrioClear() {
	defer recoverLog("game")
	gamePrioMu.Lock()
	defer gamePrioMu.Unlock()
	list := gamePrioRead()
	if len(list) == 0 || !app.admin {
		return
	}
	for _, exe := range list {
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe+`\PerfOptions`, registry.SET_VALUE|registry.QUERY_VALUE); err == nil {
			_ = k.DeleteValue("CpuPriorityClass")
			n, _ := k.Stat()
			k.Close()
			if n != nil && n.ValueCount == 0 && n.SubKeyCount == 0 {
				_ = registry.DeleteKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe+`\PerfOptions`)
			}
		}
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe, registry.QUERY_VALUE); err == nil {
			n, _ := k.Stat()
			k.Close()
			if n != nil && n.ValueCount == 0 && n.SubKeyCount == 0 {
				_ = registry.DeleteKey(registry.LOCAL_MACHINE, ifeoKey+`\`+exe)
			}
		}
	}
	_ = os.Remove(gamePrioPath())
}
