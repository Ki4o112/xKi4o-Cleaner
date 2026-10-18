//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------------------------------------------------------------- paths

type paths struct {
	local, roaming, localLow, temp, windir, programData, sysDrive, profile string
}

var P paths

func initPaths() {
	P = paths{
		local:       os.Getenv("LOCALAPPDATA"),
		roaming:     os.Getenv("APPDATA"),
		temp:        os.Getenv("TEMP"),
		windir:      os.Getenv("SystemRoot"),
		programData: os.Getenv("ProgramData"),
		sysDrive:    systemDrive(),
		profile:     os.Getenv("USERPROFILE"),
	}
	if P.windir == "" {
		P.windir = os.Getenv("WINDIR")
	}
	for _, p := range []*string{&P.local, &P.roaming, &P.temp, &P.profile} {
		if *p != "" {
			*p = longPath(*p)
		}
	}
	if P.profile != "" {
		P.localLow = filepath.Join(P.profile, "AppData", "LocalLow")
	}
	// Folders that may never be a target themselves.
	protect(P.local, P.roaming, P.localLow, P.windir, P.programData, P.profile,
		filepath.Join(P.windir, "System32"), filepath.Join(P.windir, "SysWOW64"),
		filepath.Join(P.windir, "WinSxS"), filepath.Join(P.sysDrive+`\`, "Users"),
		os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"),
		filepath.Join(P.profile, "Documents"), filepath.Join(P.profile, "Downloads"),
		filepath.Join(P.profile, "Desktop"), filepath.Join(P.profile, "OneDrive"))
}

func j(parts ...string) string { return filepath.Join(parts...) }

// longPath expands 8.3 short names (C:\Users\ANDREA~1\...) so that two
// spellings of one folder are recognised as the same folder.
func longPath(p string) string {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetLongPathName(u, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n > uint32(len(buf)) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

// ---------------------------------------------------------------- model

type report struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Size      int64  `json:"size"`
	Count     int64  `json:"count"`
	Running   bool   `json:"running"`
	Blocked   string `json:"blocked,omitempty"`
}

type cleanCtx struct {
	opts  CleanOpts
	stop  func() bool
	note  func(string)
	cl    *cleaner // stop, dry run and the user's exclusions for file targets
	files stats    // file results of a custom clean that also deletes files
}

type item struct {
	id      string
	deep    bool                               // a restore point is made first
	admin   bool                               // needs administrator rights
	busy    func(procs map[string]bool) string // why it must wait right now ("" = fine)
	restart bool                               // a restart finishes the job
	procs   []string
	detect  func() bool
	targets func() []target
	size    func() (int64, int64) // custom size / count; size -1 = unknown
	clean   func(c *cleanCtx) (int64, string, error)
}

func (it *item) available() bool {
	if it.detect != nil {
		return it.detect()
	}
	if it.targets != nil {
		return len(it.targets()) > 0
	}
	return true
}

func (it *item) runningIn(procs map[string]bool) bool {
	for _, p := range it.procs {
		if procs[p] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- catalog

var chromiumProfileCaches = []string{"Cache", "Code Cache", "GPUCache", "Media Cache", "DawnCache",
	"DawnGraphiteCache", "DawnWebGPUCache", "GraphiteDawnCache", "ShaderCache", "GrShaderCache"}
var chromiumBaseCaches = []string{"ShaderCache", "GrShaderCache", "GraphiteDawnCache",
	"component_crx_cache", "extensions_crx_cache", `Crashpad\reports`}
var electronCaches = []string{"Cache", "Code Cache", "GPUCache", "DawnCache", "DawnGraphiteCache",
	"DawnWebGPUCache", "GraphiteDawnCache", "CachedData", "CachedExtensionVSIXs", `Crashpad\reports`, "logs"}

// chromiumTargets: cache folders only (BleachBit "cache" rules). History,
// cookies, logins, sessions and site storage are separate files.
func chromiumTargets(bases ...string) []target {
	var ts []target
	for _, base := range bases {
		if !dirExists(base) {
			continue
		}
		for _, sub := range readDir(base) {
			if isLink(sub.info) || !sub.info.IsDir() {
				continue
			}
			for _, n := range chromiumProfileCaches {
				ts = append(ts, contentsIfExists(j(sub.path, n))...)
			}
		}
		for _, n := range chromiumBaseCaches {
			ts = append(ts, contentsIfExists(j(base, n))...)
		}
	}
	return ts
}

func electronTargets(bases ...string) []target {
	var ts []target
	for _, base := range bases {
		for _, n := range electronCaches {
			ts = append(ts, contentsIfExists(j(base, n))...)
		}
	}
	return ts
}

func anyDir(ps ...string) bool {
	for _, p := range ps {
		if dirExists(p) {
			return true
		}
	}
	return false
}

func steamDir() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("SteamPath")
	if err != nil || v == "" {
		return ""
	}
	return filepath.Clean(strings.ReplaceAll(v, "/", `\`))
}

// newTeamsDir is the web view folder of the new Microsoft Teams.
func newTeamsDir() string {
	return j(P.local, `Packages\MSTeams_8wekyb3d8bbwe\LocalCache\Microsoft\MSTeams\EBWebView`)
}

func discordBases() []string {
	return []string{j(P.roaming, "discord"), j(P.roaming, "discordptb"), j(P.roaming, "discordcanary")}
}

func operaBases() []string {
	return []string{j(P.local, "Opera Software"), j(P.roaming, "Opera Software")}
}

var catalog []*item

// steamLibraries returns the Steam folder and every extra library folder.
func steamLibraries() []string {
	s := steamDir()
	if s == "" {
		return nil
	}
	b, _ := os.ReadFile(j(s, `steamapps\libraryfolders.vdf`))
	return mergeLibraries(s, string(b))
}

func ubisoftDir() string {
	for _, pf := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if pf == "" {
			continue
		}
		if d := j(pf, `Ubisoft\Ubisoft Game Launcher`); dirExists(d) {
			return d
		}
	}
	return ""
}

func buildCatalog() {
	chromium := func(id string, proc string, base func() string) *item {
		return &item{id: id, procs: []string{proc},
			detect:  func() bool { return dirExists(base()) },
			targets: func() []target { return chromiumTargets(base()) }}
	}
	electron := func(id string, procs []string, bases func() []string, extra func() []target) *item {
		return &item{id: id, procs: procs,
			detect: func() bool { return anyDir(bases()...) },
			targets: func() []target {
				ts := electronTargets(bases()...)
				if extra != nil {
					ts = append(ts, extra()...)
				}
				return ts
			}}
	}

	catalog = []*item{
		// ---------------- System
		{id: "temp", targets: func() []target {
			seen := map[string]bool{}
			var ts []target
			for _, p := range []string{P.temp, j(P.local, "Temp"), j(P.windir, "Temp")} {
				if p == "" {
					continue
				}
				key := normPath(longPath(p)) // TEMP is often a short 8.3 path
				if seen[key] || !dirExists(p) {
					continue
				}
				seen[key] = true
				ts = append(ts, target{Path: p, Kind: kOlder})
			}
			return ts
		}},
		{id: "inetcache", targets: func() []target {
			return contentsIfExists(j(P.local, `Microsoft\Windows\INetCache`))
		}},
		{id: "storeapps", targets: func() []target {
			var ts []target
			for _, pkg := range subDirs(j(P.local, "Packages"), "*") {
				ts = append(ts, contentsIfExists(j(pkg, "TempState"))...)
			}
			return ts
		}},
		{id: "msstore", procs: []string{"winstore.app.exe"},
			detect: func() bool { return dirExists(j(P.local, `Packages\Microsoft.WindowsStore_8wekyb3d8bbwe`)) },
			targets: func() []target {
				d := j(P.local, `Packages\Microsoft.WindowsStore_8wekyb3d8bbwe`)
				return contentsIfExists(j(d, "LocalCache"), j(d, `LocalState\Cache`), j(d, `AC\INetCache`))
			}},
		{id: "dumps", detect: func() bool { return true }, targets: func() []target {
			ts := contentsIfExists(
				j(P.windir, "Minidump"),
				j(P.local, "CrashDumps"),
				j(P.local, `Microsoft\Windows\WER`),
				j(P.programData, `Microsoft\Windows\WER\ReportArchive`),
				j(P.programData, `Microsoft\Windows\WER\ReportQueue`),
				j(P.programData, `Microsoft\Windows\WER\Temp`))
			if fileExists(j(P.windir, "MEMORY.DMP")) {
				ts = append(ts, target{Path: j(P.windir, "MEMORY.DMP"), Kind: kWhole})
			}
			if dirExists(j(P.windir, "LiveKernelReports")) {
				ts = append(ts, target{Path: j(P.windir, "LiveKernelReports"), Kind: kGlob, Pattern: "*.dmp", Recurse: true})
			}
			return ts
		}},
		{id: "thumbs", targets: func() []target {
			d := j(P.local, `Microsoft\Windows\Explorer`)
			if !dirExists(d) {
				return nil
			}
			return []target{{Path: d, Kind: kGlob, Pattern: "thumbcache_*.db"}}
		}},
		{id: "recycle",
			size: func() (int64, int64) { return recycleBinInfo() },
			clean: func(c *cleanCtx) (int64, string, error) {
				before, _ := recycleBinInfo()
				if !emptyRecycleBin() {
					after, _ := recycleBinInfo()
					if after >= before {
						return 0, "", errors.New(T("err.recycle"))
					}
					return before - after, "", nil
				}
				return before, "", nil
			}},
		{id: "dns",
			size: func() (int64, int64) { return 0, 0 },
			clean: func(c *cleanCtx) (int64, string, error) {
				_, _, _ = runQuiet(30*time.Second, sys32("ipconfig.exe"), "/flushdns")
				return 0, "", nil
			}},

		// ---------------- Browsers
		chromium("chrome", "chrome.exe", func() string { return j(P.local, `Google\Chrome\User Data`) }),
		chromium("edge", "msedge.exe", func() string { return j(P.local, `Microsoft\Edge\User Data`) }),
		chromium("brave", "brave.exe", func() string { return j(P.local, `BraveSoftware\Brave-Browser\User Data`) }),
		chromium("vivaldi", "vivaldi.exe", func() string { return j(P.local, `Vivaldi\User Data`) }),
		{id: "opera", procs: []string{"opera.exe"},
			detect:  func() bool { return anyDir(operaBases()...) },
			targets: func() []target { return chromiumTargets(operaBases()...) }},
		{id: "firefox", procs: []string{"firefox.exe"},
			detect: func() bool {
				return anyDir(j(P.local, `Mozilla\Firefox\Profiles`), j(P.roaming, `Mozilla\Firefox\Profiles`))
			},
			targets: func() []target {
				var ts []target
				for _, p := range subDirs(j(P.local, `Mozilla\Firefox\Profiles`), "*") {
					ts = append(ts, contentsIfExists(j(p, "cache2"), j(p, "startupCache"), j(p, "jumpListCache"), j(p, "OfflineCache"))...)
				}
				for _, p := range subDirs(j(P.roaming, `Mozilla\Firefox\Profiles`), "*") {
					ts = append(ts, contentsIfExists(j(p, "shader-cache"))...)
				}
				return ts
			}},

		// ---------------- Apps
		electron("discord", []string{"discord.exe", "discordptb.exe", "discordcanary.exe"}, discordBases, nil),
		electron("vscode", []string{"code.exe"}, func() []string { return []string{j(P.roaming, "Code")} }, nil),
		electron("slack", []string{"slack.exe"}, func() []string { return []string{j(P.roaming, "Slack")} }, nil),
		{id: "teams", procs: []string{"ms-teams.exe", "teams.exe"},
			detect: func() bool { return anyDir(newTeamsDir(), j(P.roaming, `Microsoft\Teams`)) },
			targets: func() []target {
				return append(chromiumTargets(newTeamsDir()), electronTargets(j(P.roaming, `Microsoft\Teams`))...)
			}},
		electron("notion", []string{"notion.exe"}, func() []string { return []string{j(P.roaming, "Notion")} },
			func() []target {
				var ts []target
				for _, p := range subDirs(j(P.roaming, `Notion\Partitions`), "*") {
					ts = append(ts, electronTargets(p)...)
				}
				return ts
			}),
		{id: "telegram", procs: []string{"telegram.exe"},
			detect: func() bool { return dirExists(j(P.roaming, `Telegram Desktop\tdata`)) },
			targets: func() []target {
				base := j(P.roaming, "Telegram Desktop")
				var ts []target
				// tdata\user_data*\cache and media_cache — what Telegram's own
				// "Clear cache" removes. Chats and the login stay.
				for _, ud := range subDirs(j(base, "tdata"), "user_data*") {
					for _, c := range subDirs(ud, "*cache") {
						ts = append(ts, target{Path: c, Kind: kContents})
					}
				}
				ts = append(ts, contentsIfExists(j(base, `tupdates\temp`))...)
				if dirExists(j(base, `tdata\dumps`)) {
					ts = append(ts, target{Path: j(base, `tdata\dumps`), Kind: kGlob, Pattern: "*.dmp"})
				}
				return ts
			}},
		{id: "steam", procs: []string{"steam.exe"},
			detect: func() bool { return steamDir() != "" || dirExists(j(P.local, `Steam\htmlcache`)) },
			targets: func() []target {
				ts := contentsIfExists(j(P.local, `Steam\htmlcache`))
				if s := steamDir(); s != "" {
					ts = append(ts, contentsIfExists(j(s, `appcache\httpcache`), j(s, "dumps"), j(s, "logs"))...)
				}
				return ts
			}},
		{id: "epic", procs: []string{"epicgameslauncher.exe"},
			detect: func() bool { return dirExists(j(P.local, `EpicGamesLauncher\Saved`)) },
			targets: func() []target {
				var ts []target
				for _, d := range subDirs(j(P.local, `EpicGamesLauncher\Saved`), "webcache*") {
					ts = append(ts, target{Path: d, Kind: kContents})
				}
				return append(ts, contentsIfExists(j(P.local, `EpicGamesLauncher\Saved\Logs`),
					j(P.local, `EpicGamesLauncher\Saved\Crashes`), j(P.local, `EpicGames\eosoverlay\BrowserCache`))...)
			}},
		{id: "ubisoft", procs: []string{"upc.exe", "ubisoftconnect.exe"},
			detect: func() bool { return ubisoftDir() != "" },
			targets: func() []target {
				d := ubisoftDir()
				if d == "" {
					return nil
				}
				var ts []target
				for _, h := range subDirs(j(d, "cache"), "http*") {
					ts = append(ts, target{Path: h, Kind: kContents})
				}
				return append(ts, contentsIfExists(j(d, "logs"))...)
			}},
		{id: "battlenet", procs: []string{"battle.net.exe"},
			detect: func() bool { return dirExists(j(P.local, "Battle.net")) },
			targets: func() []target {
				base := j(P.local, "Battle.net")
				var ts []target
				for _, d := range subDirs(base, "*cache") {
					ts = append(ts, target{Path: d, Kind: kContents})
				}
				ts = append(ts, chromiumTargets(j(base, "BrowserCaches"))...)
				return append(ts, contentsIfExists(j(base, "Logs"), j(base, "Errors"),
					j(P.local, `Blizzard Entertainment\Battle.net\Cache`))...)
			}},
		{id: "eaapp", procs: []string{"eadesktop.exe"},
			detect: func() bool { return anyDir(j(P.local, `Electronic Arts\EA Desktop`), j(P.local, "EADesktop")) },
			targets: func() []target {
				var ts []target
				for _, d := range append(subDirs(j(P.local, `Electronic Arts\EA Desktop`), "*cache"),
					subDirs(j(P.local, `EADesktop\QtWebEngine\Default`), "*cache")...) {
					ts = append(ts, target{Path: d, Kind: kContents})
				}
				return append(ts, contentsIfExists(j(P.local, `Electronic Arts\EA Desktop\Logs`), j(P.local, `EADesktop\cache`))...)
			}},
		{id: "gog", procs: []string{"galaxyclient.exe"},
			detect: func() bool { return dirExists(j(P.programData, `GOG.com\Galaxy`)) },
			targets: func() []target {
				g := j(P.programData, `GOG.com\Galaxy`)
				ts := contentsIfExists(j(g, "logs"), j(g, "crashdumps"))
				for _, d := range subDirs(j(g, `webcache\common`), "*cache*") {
					ts = append(ts, target{Path: d, Kind: kContents})
				}
				return ts
			}},
		{id: "riot", procs: []string{"riotclientservices.exe", "riotclientux.exe", "riotclientuxrender.exe"},
			detect: func() bool { return dirExists(j(P.local, `Riot Games\Riot Client`)) },
			targets: func() []target {
				return contentsIfExists(j(P.local, `Riot Games\Riot Client\Logs`), j(P.local, `Riot Games\Riot Client\Crashes`))
			}},
		{id: "obs", procs: []string{"obs64.exe", "obs32.exe"},
			detect: func() bool { return dirExists(j(P.roaming, "obs-studio")) },
			targets: func() []target {
				o := j(P.roaming, "obs-studio")
				return contentsIfExists(j(o, "logs"), j(o, "crashes"), j(o, "profiler_data"))
			}},
		{id: "devcache", targets: func() []target {
			return contentsIfExists(
				j(P.local, `pip\cache`),
				j(P.local, `npm-cache\_cacache`),
				j(P.local, `Yarn\Cache`),
				j(P.local, "go-build"),
				j(P.local, `NuGet\v3-cache`),
				j(P.local, `NuGet\plugins-cache`))
		}},

		// ---------------- GPU
		{id: "nvdrivers", deep: true, admin: true, restart: true,
			detect: func() bool {
				return dirExists(j(P.programData, "NVIDIA Corporation")) || len(findOldNvidiaDrivers(false)) > 0
			},
			size: func() (int64, int64) {
				old := findOldNvidiaDrivers(true)
				var s int64
				for _, d := range old {
					s += d.size
				}
				return s, int64(len(old))
			},
			clean: cleanOldNvidiaDrivers},
		{id: "nvinstallers", targets: func() []target {
			ts := contentsIfExists(
				j(P.sysDrive+`\`, `NVIDIA\DisplayDriver`),
				j(P.programData, `NVIDIA Corporation\Downloader`),
				j(P.programData, `NVIDIA Corporation\NV_Cache`))
			if d := j(P.programData, "NVIDIA"); dirExists(d) {
				ts = append(ts, target{Path: d, Kind: kGlob, Pattern: "*.log"}) // driver installer logs
			}
			ota := j(P.programData, `NVIDIA Corporation\NVIDIA App\UpdateFramework\ota-artifacts`)
			if dirExists(ota) {
				ts = append(ts, target{Path: ota, Kind: kKeepNewest})
			}
			return ts
		}},
		{id: "nvapp", procs: []string{"nvidia app.exe"},
			detect: func() bool { return dirExists(j(P.local, `NVIDIA Corporation\NVIDIA App`)) },
			targets: func() []target {
				a := j(P.local, `NVIDIA Corporation\NVIDIA App`)
				var ts []target
				for _, d := range append(subDirs(j(a, "CefCache"), "*cache*"), subDirs(j(a, `CefCache\Default`), "*cache*")...) {
					ts = append(ts, target{Path: d, Kind: kContents})
				}
				if dirExists(a) {
					ts = append(ts, target{Path: a, Kind: kGlob, Pattern: "*.log*"})
				}
				if d := j(P.programData, `NVIDIA Corporation\NVIDIA App`); dirExists(d) {
					ts = append(ts, target{Path: d, Kind: kGlob, Pattern: "*.log", Recurse: true})
				}
				return ts
			}},
		{id: "amdinstallers",
			detect: func() bool { return anyDir(j(P.sysDrive+`\`, "AMD"), j(P.sysDrive+`\`, `Intel\Logs`)) },
			targets: func() []target {
				// The AMD installer unpacks into C:\AMD and leaves it there;
				// Intel's driver installers leave their logs in C:\Intel\Logs.
				var ts []target
				for _, d := range subDirs(j(P.sysDrive+`\`, "AMD"), "*") {
					ts = append(ts, target{Path: d, Kind: kWhole})
				}
				if d := j(P.sysDrive+`\`, `Intel\Logs`); dirExists(d) {
					ts = append(ts, target{Path: d, Kind: kGlob, Pattern: "*.log"})
				}
				return ts
			}},
		{id: "shaders", targets: func() []target {
			var ts []target
			for _, d := range subDirs(j(P.local, "NVIDIA"), "*cache") {
				ts = append(ts, target{Path: d, Kind: kContents})
			}
			ts = append(ts, contentsIfExists(
				j(P.localLow, `NVIDIA\PerDriverVersion\DXCache`),
				j(P.localLow, `NVIDIA\DXCache`),
				j(P.roaming, `NVIDIA\ComputeCache`),
				j(P.local, `NVIDIA Corporation\NV_Cache`),
				j(P.local, "D3DSCache"))...)
			for _, d := range subDirs(j(P.local, "AMD"), "*cache") {
				ts = append(ts, target{Path: d, Kind: kContents})
			}
			ts = append(ts, contentsIfExists(j(P.localLow, `Intel\ShaderCache`), j(P.programData, `Intel\ShaderCache`))...)
			// Steam's per-game shader pre-cache, only while Steam is closed.
			if !runningProcs()["steam.exe"] {
				for _, lib := range steamLibraries() {
					ts = append(ts, contentsIfExists(j(lib, `steamapps\shadercache`))...)
				}
			}
			return ts
		}},

		// ---------------- Windows Update
		{id: "wucache", deep: true, admin: true, busy: wuBusy,
			detect: func() bool { return true },
			targets: func() []target {
				ts := contentsIfExists(j(P.windir, `SoftwareDistribution\Download`))
				for _, old := range []string{"SoftwareDistribution.old", "SoftwareDistribution.bak"} {
					if dirExists(j(P.windir, old)) {
						ts = append(ts, target{Path: j(P.windir, old), Kind: kWhole})
					}
				}
				return ts
			},
			clean: cleanWindowsUpdate},
		{id: "msiorphans", deep: true, admin: true, busy: installerBusy,
			detect:  func() bool { return dirExists(j(P.windir, "Installer")) },
			targets: msiTargets},
		{id: "winlogs", admin: true, detect: func() bool { return true }, targets: func() []target {
			// Plain-text and CAB logs only (conservative subset of the
			// Winapp2 / BleachBit "Windows logs" rules). Logs in use stay.
			var ts []target
			add := func(dir, pattern string, recurse bool) {
				if dirExists(dir) {
					ts = append(ts, target{Path: dir, Kind: kGlob, Pattern: pattern, Recurse: recurse})
				}
			}
			add(j(P.windir, "Logs"), "*.log", true)
			add(j(P.windir, `Logs\CBS`), "*.cab", false)
			add(j(P.windir, "debug"), "*.log", true)
			add(j(P.windir, `security\logs`), "*.log", false)
			add(j(P.windir, `security\logs`), "*.old", false)
			add(j(P.windir, "INF"), "*.log", false)
			// USOShared\Logs\System belongs to SYSTEM alone: administrators
			// cannot delete it (a real PC reported 78 "denied"), so it is left
			// out instead of being counted and then refused every time.
			ts = append(ts, contentsExcept(j(P.programData, `USOShared\Logs`), "System")...)
			return ts
		}},
		{id: "diskcleanup", deep: true, admin: true, restart: true,
			detect: func() bool { return fileExists(sys32("cleanmgr.exe")) },
			size:   func() (int64, int64) { return -1, 0 },
			clean: func(c *cleanCtx) (int64, string, error) {
				return measureFree(func() error { return runCleanmgr(100, cleanmgrAllowList) })
			}},
		{id: "dism", deep: true, admin: true, busy: wuBusy, restart: true,
			size:  func() (int64, int64) { return -1, 0 },
			clean: cleanComponentStore},
		{id: "windowsold", deep: true, admin: true,
			detect: func() bool { return dirExists(j(P.sysDrive+`\`, "Windows.old")) },
			size: func() (int64, int64) {
				fi, err := os.Lstat(j(P.sysDrive+`\`, "Windows.old"))
				if err != nil {
					return 0, 0
				}
				s, n := sizeOf(planned{j(P.sysDrive+`\`, "Windows.old"), fi})
				return s, int64(n)
			},
			clean: func(c *cleanCtx) (int64, string, error) {
				return measureFree(func() error {
					return runCleanmgr(101, map[string]bool{"previous installations": true})
				})
			}},

		// ---------------- Privacy
		{id: "recent", detect: func() bool { return dirExists(j(P.roaming, `Microsoft\Windows\Recent`)) },
			targets: func() []target {
				// Only the shortcut list itself; pinned jump lists stay.
				return []target{{Path: j(P.roaming, `Microsoft\Windows\Recent`), Kind: kGlob, Pattern: "*.lnk"}}
			}},
		{id: "clipboard",
			size: func() (int64, int64) { return 0, 0 },
			clean: func(c *cleanCtx) (int64, string, error) {
				if !clearClipboard() {
					return 0, "", errors.New(T("err.clipboard"))
				}
				return 0, "", nil
			}},
	}
}

type previewResult struct {
	ID      string         `json:"id"`
	Entries []previewEntry `json:"entries"`
	Total   int64          `json:"total"`
	Files   int            `json:"files"`
	More    int            `json:"more"`
	Tool    bool           `json:"tool"` // a Windows tool does the work: no file list
}

// previewItem lists what a category would remove right now, largest first.
// Nothing is changed.
func previewItem(id string) (r previewResult) {
	r.ID = id
	defer func() { _ = recover() }()
	it := findItem(id)
	if it == nil || !it.available() {
		return r
	}
	if it.id == "nvdrivers" {
		r.Tool = true
		for _, d := range findOldNvidiaDrivers(true) {
			r.Entries = append(r.Entries, previewEntry{Path: d.inf + "  ·  " + d.version, Size: d.size})
			r.Total += d.size
		}
		return r
	}
	if it.targets == nil || (it.clean != nil && it.id != "wucache") {
		r.Tool = true
		return r
	}
	r.Entries, r.Total, r.Files, r.More = previewTargets(it.targets(), 400)
	return r
}

func findItem(id string) *item {
	for _, it := range catalog {
		if it.id == id {
			return it
		}
	}
	return nil
}

// ---------------------------------------------------------------- analyse

func analyzeItem(it *item, procs map[string]bool, admin bool) report {
	r := report{ID: it.id}
	if !it.available() {
		return r
	}
	r.Available = true
	r.Running = it.runningIn(procs)
	switch {
	case it.admin && !admin:
		r.Blocked = T("blocked.admin")
	case it.busy != nil:
		r.Blocked = it.busy(procs)
	}
	if it.size != nil {
		r.Size, r.Count = it.size()
	} else if it.targets != nil {
		s, n := sizeTargets(it.targets())
		r.Size, r.Count = s, int64(n)
	}
	if it.id == "wucache" && r.Size >= 0 {
		r.Size += deliveryOptimizationSize()
	}
	return r
}

// analyzeAll scans every item, four at a time, and reports each result.
func analyzeAll(admin bool, emitItem func(report)) bool {
	procs := runningProcs()
	pending := updatesPending(procs)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, it := range catalog {
		wg.Add(1)
		sem <- struct{}{}
		go func(it *item) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if recover() != nil {
					emitItem(report{ID: it.id})
				}
			}()
			emitItem(analyzeItem(it, procs, admin))
		}(it)
	}
	wg.Wait()
	return pending
}

// ---------------------------------------------------------------- helpers

// measureFree runs f and reports how much free space it produced.
func measureFree(f func() error) (int64, string, error) {
	before := freeBytes()
	err := f()
	freed := freeBytes() - before
	if freed < 0 {
		freed = 0
	}
	return freed, "", err
}

var cleanmgrAllowList = map[string]bool{
	"active setup temp folders":             true,
	"branchcache":                           true,
	"content indexer cleaner":               true,
	"diagnostic data viewer database files": true,
	"downloaded program files":              true,
	"feedback hub archive log files":        true,
	"old chkdsk files":                      true,
	"retaildemo offline content":            true,
	"setup log files":                       true,
	"temporary setup files":                 true,
	"upgrade discarded files":               true,
	"windows defender":                      true,
	"windows upgrade log files":             true,
}

// runCleanmgr runs Windows Disk Cleanup with only the given categories on.
func runCleanmgr(profile int, allow map[string]bool) error {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\VolumeCaches`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return err
	}
	names, _ := root.ReadSubKeyNames(-1)
	root.Close()
	value := fmt.Sprintf("StateFlags%04d", profile)
	for _, n := range names {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\VolumeCaches\`+n, registry.SET_VALUE)
		if err != nil {
			continue
		}
		flag := uint32(0)
		if allow[strings.ToLower(n)] {
			flag = 2
		}
		_ = k.SetDWordValue(value, flag)
		k.Close()
	}
	_, _, err = runQuiet(45*time.Minute, sys32("cleanmgr.exe"), "/sagerun:"+strconv.Itoa(profile))
	return err
}

// ---------------------------------------------------------------- NVIDIA drivers

type oldDriver struct {
	inf     string
	version string
	size    int64
}

var (
	reClassDisplay = regexp.MustCompile(`(?im)^\s*Class\s*=\s*"?Display\b`)
	reProviderNV   = regexp.MustCompile(`(?im)^\s*Provider\s*=.*NVIDIA`)
	reDriverVer    = regexp.MustCompile(`(?im)^\s*DriverVer\s*=\s*([^\r\n;]+)`)
)

func readInfText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 512*1024 {
		b = b[:512*1024]
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u := make([]uint16, (len(b)-2)/2)
		for i := range u {
			u[i] = uint16(b[2+2*i]) | uint16(b[3+2*i])<<8
		}
		return string(utf16.Decode(u))
	}
	return string(b)
}

// displayInfsInUse returns the INF names used by any display adapter.
func displayInfsInUse() map[string]bool {
	inUse := map[string]bool{}
	const class = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, class, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	names, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, n := range names {
		sk, err := registry.OpenKey(registry.LOCAL_MACHINE, class+`\`+n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if v, _, err := sk.GetStringValue("InfPath"); err == nil && v != "" {
			inUse[strings.ToLower(v)] = true
		}
		sk.Close()
	}
	return inUse
}

func driverStoreSize(inf string) int64 {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\DriverDatabase\DriverInfFiles\`+inf, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	pkgs, _, err := k.GetStringsValue("")
	if err != nil {
		return 0
	}
	var total int64
	for _, p := range pkgs {
		if p == "" || strings.ContainsAny(p, `\/`) {
			continue
		}
		dir := j(P.windir, `System32\DriverStore\FileRepository`, p)
		if fi, err := os.Lstat(dir); err == nil {
			s, _ := sizeOf(planned{dir, fi})
			total += s
		}
	}
	return total
}

// findOldNvidiaDrivers lists NVIDIA display driver packages that no
// display adapter uses. If the in-use list cannot be read, nothing is
// reported, so nothing can be removed.
func findOldNvidiaDrivers(withSize bool) []oldDriver {
	inUse := displayInfsInUse()
	if len(inUse) == 0 {
		return nil
	}
	files, _ := filepath.Glob(j(P.windir, "INF", "oem*.inf"))
	var out []oldDriver
	for _, f := range files {
		name := strings.ToLower(filepath.Base(f))
		if inUse[name] {
			continue
		}
		txt := readInfText(f)
		if !reClassDisplay.MatchString(txt) || !reProviderNV.MatchString(txt) {
			continue
		}
		d := oldDriver{inf: name}
		if m := reDriverVer.FindStringSubmatch(txt); m != nil {
			d.version = strings.TrimSpace(m[1])
		}
		if withSize {
			d.size = driverStoreSize(name)
		}
		out = append(out, d)
	}
	return out
}

func cleanOldNvidiaDrivers(c *cleanCtx) (int64, string, error) {
	old := findOldNvidiaDrivers(true)
	var freed int64
	removed := 0
	for _, d := range old {
		if c.stop() {
			break
		}
		c.note(d.version)
		// No /force: Windows refuses to remove a driver that is in use.
		_, code, _ := runQuiet(5*time.Minute, sys32("pnputil.exe"), "/delete-driver", d.inf)
		if code == 0 {
			freed += d.size
			removed++
		}
	}
	if len(old) > 0 && removed == 0 {
		return 0, "", errors.New(T("err.drivers"))
	}
	return freed, "", nil
}
