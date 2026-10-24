//go:build windows

package main

import (
	"strings"
	"sync"
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
	notGame map[uint32]time.Time // looked at, not a game (looked at again after 10 s: a game may start in a small window)
	known   map[string]bool      // Windows' own list of games (exe paths, lower case)
	knownAt time.Time
	mu      sync.Mutex
}

// gmAutoCfg: the Game mode page's settings (kept in the settings file).
func gmAutoCfg() (auto, ram, force bool, apps []string) {
	g, _ := getSettings()["game"].(map[string]any)
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
			return
		}
		name, started := gmAuto.name, gmAuto.started
		gmAuto.pid, gmAuto.name, gmAuto.started = 0, "", false
		if !active {
			return // turned off by hand meanwhile
		}
		ev := map[string]any{"state": "end", "game": name}
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
	name := strings.TrimSuffix(exe, ".exe")
	gmAuto.pid, gmAuto.name = pid, name
	ev := map[string]any{"state": "start", "game": name}
	if !active {
		var keep map[string]bool
		if raw, err := queryProcesses(); err == nil {
			ps := make([]gmProc, 0, len(raw))
			for _, p := range raw {
				ps = append(ps, gmProc{PID: p.pid, PPID: p.ppid, Created: p.created, Exe: strings.ToLower(p.name)})
			}
			keep = gameLaunchers(ps, pid)
		}
		res := gameStart(gmOpts{Apps: withoutExes(apps, keep), Force: force, RAM: ram})
		gmAuto.started = res.Error == "" && res.State.Active
		ev["started"] = gmAuto.started
		ev["freed"] = res.State.Freed
	} else if ram {
		ev["freed"] = gameRAM(T("log.gmGameStart", name))
	}
	emit("gm-auto", ev)
}

func gameRAM(why string) int64 {
	op := beginOp("memory", "", why, nil)
	res := cleanMemory([]string{"workingset", "modified", "standby", "combine"})
	op.info("", T("log.ramFreed", sizeText(res.Freed)), map[string]any{"freed": res.Freed})
	op.end("", nil)
	return res.Freed
}

func processAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == uint32(windows.WAIT_TIMEOUT)
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
// has DirectX, Vulkan or OpenGL loaded.
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
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(ph)
	mods := make([]windows.Handle, 1024)
	var need uint32
	if windows.EnumProcessModulesEx(ph, &mods[0], uint32(len(mods))*uint32(unsafe.Sizeof(mods[0])), &need, windows.LIST_MODULES_ALL) != nil {
		return false
	}
	n := min(int(need/uint32(unsafe.Sizeof(mods[0]))), len(mods))
	buf := make([]uint16, 260)
	for _, m := range mods[:n] {
		if windows.GetModuleBaseName(ph, m, &buf[0], uint32(len(buf))) != nil {
			continue
		}
		switch strings.ToLower(windows.UTF16ToString(buf)) {
		case "d3d9.dll", "d3d11.dll", "d3d12.dll", "vulkan-1.dll", "opengl32.dll":
			return true
		}
	}
	return false
}
