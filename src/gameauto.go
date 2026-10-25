package main

// Game mode by itself: a game is noticed when its window comes to the front,
// whatever the game. It is a game when Windows itself knows it as one (Game
// Bar's list), when it runs from a game library (Steam, Epic, GOG, EA,
// Ubisoft, Xbox, Riot…), or when it fills the screen and draws with
// DirectX, Vulkan or OpenGL and is not a browser, video player or other
// known non-game. Free of Windows calls.

import "strings"

// gameDirs: folders that only hold games (lower case, with backslashes).
var gameDirs = []string{`\steamapps\common\`, `\epic games\`, `\gog galaxy\games\`, `\gog games\`, `\ea games\`,
	`\ubisoft game launcher\games\`, `\xboxgames\`, `\riot games\`, `\rockstar games\`, `\battlestate games\`,
	`\wargaming.net\games\`, `\amazon games\library\`, `\itch\apps\`}

// notGames: launchers, their helpers and crash reporters (they live in the
// game folders too), and programs that fill the screen with 3D but are no
// games: browsers, video players, presentations, this app.
var notGames = map[string]bool{
	"steam.exe": true, "steamwebhelper.exe": true, "steamservice.exe": true, "epicgameslauncher.exe": true, "epicwebhelper.exe": true,
	"galaxyclient.exe": true, "galaxyclient helper.exe": true, "eadesktop.exe": true, "eabackgroundservice.exe": true, "origin.exe": true,
	"upc.exe": true, "ubisoftconnect.exe": true, "uplaywebcore.exe": true, "battle.net.exe": true, "agent.exe": true,
	"riotclientservices.exe": true, "riotclientux.exe": true, "riotclientuxrender.exe": true, "launcher.exe": true,
	"rockstarlauncher.exe": true, "socialclubhelper.exe": true, "unitycrashhandler64.exe": true, "unitycrashhandler32.exe": true,
	"crashreportclient.exe": true, "crashpad_handler.exe": true, "easyanticheat.exe": true, "easyanticheat_eos_setup.exe": true,
	"chrome.exe": true, "msedge.exe": true, "firefox.exe": true, "brave.exe": true, "opera.exe": true, "vivaldi.exe": true,
	"vlc.exe": true, "mpc-hc64.exe": true, "mpc-hc.exe": true, "mpc-be64.exe": true, "potplayermini64.exe": true, "mpv.exe": true,
	"microsoft.media.player.exe": true, "wmplayer.exe": true, "powerpnt.exe": true, "obs64.exe": true, "explorer.exe": true,
	"xki4ocleaner.exe": true, "msedgewebview2.exe": true, "applicationframehost.exe": true, "searchhost.exe": true,
	"startmenuexperiencehost.exe": true, "lockapp.exe": true, "dwm.exe": true, "presentmon.exe": true,
}

// launcherWords: parts of a name that make a launcher, an updater or a
// setup, not the game (they live in the game folders too).
var launcherWords = []string{"launcher", "bootstrap", "updater", "patcher", "installer", "setup", "crashhandler", "crashreport",
	"leagueclient", "riotclient", "playgtav", "battleye", "redistributable", "vc_redist", "dxsetup", "uninst"}

// isLauncher: the exe starts or updates a game; game mode never treats it as
// the game and never closes it.
func isLauncher(exe string) bool {
	exe = strings.ToLower(exe)
	if notGames[exe] {
		return true
	}
	for _, w := range launcherWords {
		if strings.Contains(exe, w) {
			return true
		}
	}
	return false
}

// gameSettle: how long a game must stay in front before game mode acts (a
// launcher that shows a window and starts the game meanwhile is left alone).
const gameSettle = 12 // seconds

// gameRAMRegions: what is freed for a game — only the cache of files Windows
// keeps; no running program (the launcher, the game) loses its memory.
var gameRAMRegions = []string{"modified", "standby"}

// gameVerdict decides from what is known of the program in front.
func gameVerdict(exe, path string, windowsGame, fullScreen3D bool) bool {
	exe = strings.ToLower(exe)
	if isLauncher(exe) || strings.Contains(exe, "xki4ocleaner") {
		return false
	}
	if windowsGame || fullScreen3D {
		return true
	}
	p := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	for _, d := range gameDirs {
		if strings.Contains(p, d) {
			return true
		}
	}
	return false
}

// gameLaunchers: the exes of the game and of every process that started it
// (its launcher); auto game mode never closes them, or the game would go too.
func gameLaunchers(procs []gmProc, pid uint32) map[string]bool {
	by := make(map[uint32]gmProc, len(procs))
	for _, p := range procs {
		by[p.PID] = p
	}
	out := map[string]bool{}
	for i := 0; i < 16; i++ {
		p, ok := by[pid]
		if !ok {
			break
		}
		out[p.Exe] = true
		par, ok := by[p.PPID]
		if !ok || par.Created > p.Created || par.PID == p.PID { // a reused PID is no parent
			break
		}
		pid = par.PID
	}
	return out
}

// withoutExes: the programs to close, less the protected ones.
func withoutExes(apps []string, keep map[string]bool) []string {
	var out []string
	for _, a := range apps {
		if x := strings.ToLower(strings.TrimSpace(a)); !keep[x] && !isLauncher(x) {
			out = append(out, a)
		}
	}
	return out
}

// gameSession: what one game asked of the PC (sampled every 2 seconds).
type gameSession struct {
	Game    string  `json:"game"`
	Start   int64   `json:"start"` // unix ms
	Secs    int     `json:"secs"`
	CPUMax  float64 `json:"cpuMax"`
	CPUAvg  float64 `json:"cpuAvg"`
	GPUMax  int     `json:"gpuMax"` // load %, -1 unknown
	GPUAvg  int     `json:"gpuAvg"`
	TempMax int     `json:"tempMax"` // graphics card °C, 0 unknown (NVIDIA only)
	MemMax  uint32  `json:"memMax"`  // % of RAM in use
	n       int
	cpuSum  float64
	gpuSum  int
	gpuN    int
}

func (s *gameSession) add(cpu float64, gpuUtil, gpuTemp int, mem uint32) {
	s.n++
	s.cpuSum += cpu
	s.CPUMax = max(s.CPUMax, cpu)
	s.CPUAvg = s.cpuSum / float64(s.n)
	if gpuUtil >= 0 {
		s.gpuN++
		s.gpuSum += gpuUtil
		s.GPUMax = max(s.GPUMax, gpuUtil)
		s.GPUAvg = s.gpuSum / s.gpuN
	}
	s.TempMax = max(s.TempMax, gpuTemp)
	s.MemMax = max(s.MemMax, mem)
}

// keepSessions: the newest first, at most 20.
func keepSessions(old []gameSession, s gameSession) []gameSession {
	out := append([]gameSession{s}, old...)
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}
