package main

// Tweak presets: one click switches on a chosen set of tweaks. A preset
// only switches tweaks on (never off), goes through the same engine as a
// single switch (restore point first, the original values kept), and every
// tweak it switched on can be turned off again on its own or with Undo all.
//
// What no preset ever contains: tweaks marked risky, tweaks that cannot be
// undone or that remove something (apps, Edge, OneDrive), the security
// switches (Defender, Core Isolation, BitLocker, WPBT), hibernation, power
// plans and Fast Startup, anything about Windows Update and its helpers,
// and the tweaks that are only a matter of taste (dark mode, the classic
// menu, the clock, the taskbar's place).

type tweakPreset struct {
	ID   string   `json:"id"`
	Icon string   `json:"icon"`
	IDs  []string `json:"ids"`
}

var presetEssential = []string{
	"telemetry-off", "activity-off", "consumer-off", "start-recs-off", "settings-ads-off", "bing-off", "lockscreen-tips-off",
	"appcompat-off", "ceip-off", "explorer-ads-off", "edge-shortcut-off",
	"file-ext", "end-task", "sticky-keys-off", "autoplay-off", "copy-details", "menu-delay", "shutdown-fast", "game-mode",
}

var presetGaming = []string{
	"game-mode", "gamebar-off", "hags", "windowed-opt", "games-priority", "mouse-accel-off", "nvidia-opt", "ducking-off",
	"win32-priority", "bg-apps-off", "startup-delay-off", "menu-delay", "search-low-priority", "net-throttle-off",
	"sticky-keys-off", "shutdown-fast", "edge-bg-off",
}

var presetPrivacy = []string{
	"telemetry-off", "activity-off", "consumer-off", "location-off", "copilot-off", "recall-off", "start-recs-off",
	"settings-ads-off", "bing-off", "lockscreen-tips-off", "appcompat-off", "ceip-off", "typing-insights-off",
	"spotlight-off", "explorer-ads-off", "wifi-sense-off", "recent-files-off", "edge-shortcut-off", "edge-bg-off",
}

var presetUltimateExtra = []string{
	"animations-off", "transparency-off", "folder-discovery-off", "home-gallery-off", "store-openwith-off",
	"multi-select-menu", "full-path-title", "logon-blur-off", "detailed-bsod", "verbose-status", "long-paths", "shortcut-suffix-off",
}

// presetNever: tweaks no preset may switch on (the tests hold every preset
// to this list).
var presetNever = map[string]bool{
	"defender-rtp-off": true, "core-isolation-off": true, "bitlocker-off": true, "wpbt-off": true, "rdp-warn-off": true,
	"hibernation-off": true, "ultimate-power": true, "fast-startup-off": true, "dynamic-tick": true,
	"auto-reboot-off": true, "svc-manual": true, "do-off": true, "utc-time": true, "net-opt": true,
	"debloat": true, "gaming-apps-off": true, "edge-remove": true, "onedrive-remove": true,
	"dark-mode": true, "classic-menu": true, "taskbar-left": true, "taskbar-search-off": true, "taskview-widgets-off": true,
	"clock-seconds": true, "clock-24h": true, "compact-view": true, "hidden-files": true, "numlock-on": true,
	"ps7-default": true, "clipboard-history": true, "taskbar-labels": true, "printer-manual": true,
}

// union keeps the order of first appearance and drops repeats.
func union(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func tweakPresets() []tweakPreset {
	return []tweakPreset{
		{ID: "essential", Icon: "shield", IDs: union(presetEssential)},
		{ID: "gaming", Icon: "gamepad", IDs: union(presetGaming)},
		{ID: "privacy", Icon: "lock", IDs: union(presetPrivacy)},
		{ID: "ultimate", Icon: "bolt", IDs: union(presetEssential, presetGaming, presetPrivacy, presetUltimateExtra)},
	}
}
