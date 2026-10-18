//go:build windows

package main

// The tweaks. The list follows Sparkle's tweak set (github.com/Parcoil/Sparkle)
// plus a few common ones from Chris Titus Tech's WinUtil (MIT). Sparkle is
// GPL-3.0, so none of its code is used: every tweak is implemented here
// from the documented Windows settings, with state detection and undo.

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	advKey    = `Software\Microsoft\Windows\CurrentVersion\Explorer\Advanced`
	ifeoKey   = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options`
	cdmKey    = `Software\Microsoft\Windows\CurrentVersion\ContentDeliveryManager`
	searchKey = `Software\Microsoft\Windows\CurrentVersion\Search`
	ctxMenu   = `Software\Classes\CLSID\{86ca1aa0-34aa-4e8b-a509-50c905bae2a2}`
	homeCLSID = `Software\Classes\CLSID\{f874310e-b6b7-47dc-bc84-b9e6b38f5903}`
	galCLSID  = `Software\Classes\CLSID\{e88865ea-0e1c-4e20-9aa6-edcd0212c87c}`
	odCLSID   = `Software\Classes\CLSID\{018D5C66-4533-4307-9B53-224DE2ED1FE6}`

	ultimateBase = "e9a42b02-d5df-448d-aa00-03f14749eb61"
	ultimateOurs = "8d3a97e7-f0b7-4ab5-8ebb-5cb3295b1019"
	balancedPlan = "381b4222-f694-41f0-9685-ff5bb260df2e"

	ps7Profile = "{574e775e-4f2a-5b96-ac1e-a2962a402336}"
	ps5Profile = "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}"
)

const (
	svcAuto     = windows.SERVICE_AUTO_START
	svcManual   = windows.SERVICE_DEMAND_START
	svcDisabled = windows.SERVICE_DISABLED
)

// ---------------------------------------------------------------- built-in apps

type bloatApp struct {
	Pkg  string `json:"pkg"`
	Name string `json:"name"`
	Def  bool   `json:"def"` // ticked by default
}

// Apps the "Remove built-in apps" tweak offers. Apps people commonly use
// (Weather, Phone Link, Spotify, ...) are listed but not ticked. Photos,
// Notepad, Calculator, the Store and the like are never offered.
var bloatApps = []bloatApp{
	{"Clipchamp.Clipchamp", "Clipchamp", true},
	{"Microsoft.BingNews", "Microsoft News", true},
	{"Microsoft.BingFinance", "MSN Money", true},
	{"Microsoft.BingSports", "MSN Sports", true},
	{"Microsoft.BingTravel", "MSN Travel", true},
	{"Microsoft.BingFoodAndDrink", "MSN Food & Drink", true},
	{"Microsoft.BingHealthAndFitness", "MSN Health & Fitness", true},
	{"Microsoft.BingSearch", "Bing Search", true},
	{"Microsoft.News", "Microsoft News (old)", true},
	{"Microsoft.Getstarted", "Tips", true},
	{"Microsoft.MicrosoftOfficeHub", "Microsoft 365 (Office hub)", true},
	{"Microsoft.MicrosoftSolitaireCollection", "Solitaire Collection", true},
	{"Microsoft.People", "People", true},
	{"Microsoft.PowerAutomateDesktop", "Power Automate", true},
	{"Microsoft.Todos", "Microsoft To Do", true},
	{"Microsoft.WindowsFeedbackHub", "Feedback Hub", true},
	{"Microsoft.WindowsMaps", "Maps", true},
	{"Microsoft.ZuneVideo", "Movies & TV", true},
	{"Microsoft.549981C3F5F10", "Cortana", true},
	{"Microsoft.MixedReality.Portal", "Mixed Reality Portal", true},
	{"Microsoft.3DBuilder", "3D Builder", true},
	{"Microsoft.Microsoft3DViewer", "3D Viewer", true},
	{"Microsoft.Print3D", "Print 3D", true},
	{"Microsoft.MSPaint", "Paint 3D", true},
	{"Microsoft.SkypeApp", "Skype", true},
	{"Microsoft.Messaging", "Messaging", true},
	{"Microsoft.OneConnect", "Mobile Plans", true},
	{"Microsoft.Wallet", "Wallet", true},
	{"Microsoft.Office.Sway", "Sway", true},
	{"Microsoft.MicrosoftPowerBIForWindows", "Power BI", true},
	{"Microsoft.Windows.DevHome", "Dev Home", true},
	{"MicrosoftTeams", "Microsoft Teams (personal, old)", true},
	{"MicrosoftCorporationII.MicrosoftFamily", "Microsoft Family", true},
	{"king.com.*", "Candy Crush and other King games", true},
	{"Disney.*", "Disney+", true},
	{"Facebook.*", "Facebook / Instagram", true},
	{"BytedancePte.Ltd.TikTok", "TikTok", true},
	{"AmazonVideo.PrimeVideo", "Prime Video", true},
	{"4DF9E0F8.Netflix", "Netflix", true},
	{"5A894077.McAfeeSecurity", "McAfee", true},
	{"Duolingo-LearnLanguagesforFree", "Duolingo", true},
	{"AdobeSystemsIncorporated.AdobePhotoshopExpress", "Photoshop Express", true},
	{"7EE7776C.LinkedInforWindows", "LinkedIn", true},
	{"Microsoft.BingWeather", "Weather", false},
	{"Microsoft.BingTranslator", "Translator", false},
	{"Microsoft.YourPhone", "Phone Link", false},
	{"Microsoft.OutlookForWindows", "Outlook (new)", false},
	{"Microsoft.Office.OneNote", "OneNote for Windows 10", false},
	{"Microsoft.MicrosoftJournal", "Journal", false},
	{"Microsoft.MicrosoftStickyNotes", "Sticky Notes", false},
	{"Microsoft.WindowsAlarms", "Clock", false},
	{"Microsoft.WindowsSoundRecorder", "Sound Recorder", false},
	{"Microsoft.GetHelp", "Get Help", false},
	{"SpotifyAB.SpotifyMusic", "Spotify", false},
}

var gamingApps = []string{"Microsoft.GamingApp", "Microsoft.XboxApp", "Microsoft.XboxGameOverlay",
	"Microsoft.XboxGamingOverlay", "Microsoft.XboxSpeechToTextOverlay"}

// debloatApps lists the offered apps that are installed.
func debloatApps() []bloatApp {
	p := newProbe()
	var out []bloatApp
	for _, a := range bloatApps {
		if p.hasApp(a.Pkg) {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------- helpers

func regStr(root rkey, path, name string) (string, bool) {
	return rv{root: root, path: path, name: name}.read()
}

func setRegStr(root rkey, path, name, val string) error {
	k, _, err := registry.CreateKey(root.key(), path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, val)
}

func delRegValue(root rkey, path, name string) {
	if k, err := registry.OpenKey(root.key(), path, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(name)
		k.Close()
	}
}

func runTool(exe string, args ...string) error {
	out, code, err := runQuiet(10*time.Minute, exe, args...)
	if err == nil && code == 0 {
		return nil
	}
	msg := strings.TrimSpace(out)
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	if msg == "" {
		msg = T("tw.err.code", code)
	}
	return errors.New(msg)
}

func totalRAMKB() uint32 {
	ms := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r == 0 {
		return 0
	}
	kb := ms.totalPhys / 1024
	if kb > 0xffffffff {
		kb = 0xffffffff
	}
	return uint32(kb)
}

func activeScheme() string {
	out, _, _ := runQuiet(20*time.Second, sys32("powercfg.exe"), "/getactivescheme")
	return firstGUID(out)
}

func schemeExists(guid string) bool {
	out, _, _ := runQuiet(20*time.Second, sys32("powercfg.exe"), "/list")
	return strings.Contains(strings.ToLower(out), guid)
}

func bcdCurrent() string {
	out, _, _ := runQuiet(20*time.Second, sys32("bcdedit.exe"), "/enum", "{current}")
	return out
}

func wtSettingsPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), `Packages\Microsoft.WindowsTerminal_8wekyb3d8bbwe\LocalState\settings.json`)
}

func pwsh7Installed() bool {
	return fileExists(filepath.Join(os.Getenv("ProgramFiles"), `PowerShell\7\pwsh.exe`))
}

func setWTDefault(guid string) error {
	p := wtSettingsPath()
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	out, ok := wtSetDefaultProfile(string(b), guid)
	if !ok {
		return errors.New(T("tw.err.wt"))
	}
	tmp := p + ".xki4o.tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func edgeExe() string {
	for _, pf := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if pf == "" {
			continue
		}
		if p := filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`); fileExists(p) {
			return p
		}
	}
	return ""
}

func edgeSetup() string {
	for _, pf := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if pf == "" {
			continue
		}
		m, _ := filepath.Glob(filepath.Join(pf, `Microsoft\Edge\Application\*\Installer\setup.exe`))
		if len(m) > 0 {
			sort.Strings(m)
			return m[len(m)-1]
		}
	}
	return ""
}

// oneDriveUninstall returns OneDrive's uninstall command, or "".
func oneDriveUninstall() string {
	for _, root := range []rkey{hkcu, hklm} {
		if s, ok := regStr(root, `Software\Microsoft\Windows\CurrentVersion\Uninstall\OneDriveSetup.exe`, "UninstallString"); ok && s != "" {
			return s
		}
	}
	return ""
}

func oneDriveExe() string {
	for _, p := range []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\OneDrive\OneDrive.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft OneDrive\OneDrive.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft OneDrive\OneDrive.exe`),
	} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func liveMouse() {
	get := func(n string) int32 {
		s, _ := regStr(hkcu, `Control Panel\Mouse`, n)
		v, _ := strconv.Atoi(s)
		return int32(v)
	}
	m := [3]int32{get("MouseThreshold1"), get("MouseThreshold2"), get("MouseSpeed")}
	spi(0x0004, 0, unsafe.Pointer(&m)) // SPI_SETMOUSE
}

func liveMenuDelay() {
	s, _ := regStr(hkcu, `Control Panel\Desktop`, "MenuShowDelay")
	v, err := strconv.Atoi(s)
	if err != nil {
		v = 400
	}
	spi(0x006B, uint32(v), nil) // SPI_SETMENUSHOWDELAY
}

func liveHoverTime() {
	s, _ := regStr(hkcu, `Control Panel\Mouse`, "MouseHoverTime")
	v, err := strconv.Atoi(s)
	if err != nil {
		v = 400
	}
	spi(0x0067, uint32(v), nil) // SPI_SETMOUSEHOVERTIME
}

func liveKeyboardDelay() {
	s, _ := regStr(hkcu, `Control Panel\Keyboard`, "KeyboardDelay")
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 || v > 3 {
		v = 1
	}
	spi(0x0017, uint32(v), nil) // SPI_SETKEYBOARDDELAY
}

func liveStickyKeys() {
	s, _ := regStr(hkcu, `Control Panel\Accessibility\StickyKeys`, "Flags")
	v, err := strconv.Atoi(s)
	if err != nil {
		return
	}
	sk := struct{ size, flags uint32 }{8, uint32(v)}
	spi(0x003B, 8, unsafe.Pointer(&sk)) // SPI_SETSTICKYKEYS
}

func allAppsGone(p *probe, pkgs []string) bool {
	for _, a := range pkgs {
		if p.hasApp(a) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- catalogue

var tweaks []*tweak

func buildTweaks() {
	// Fewer svchost processes (WinUtil): the split threshold becomes the
	// installed RAM. Left alone on PCs with less than 4 GB.
	var svcReg []rv
	if ramKB := totalRAMKB(); ramKB >= 4*1024*1024 {
		svcReg = []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control`, "SvcHostSplitThresholdInKB", ramKB, uint32(0x380000))}
	}
	tweaks = []*tweak{
		// ---------------- Performance
		{id: "svc-manual", group: "perf", risk: "caution", sys: true,
			svc: []svcSet{
				{name: "MapsBroker", on: svcManual, off: svcAuto, offDelayed: true},
				{name: "StorSvc", on: svcManual, off: svcAuto, offDelayed: true},
				{name: "PcaSvc", on: svcManual, off: svcAuto, offDelayed: true},
				{name: "CDPSvc", on: svcManual, off: svcAuto, offDelayed: true},
				{name: "UsoSvc", on: svcManual, off: svcAuto, offDelayed: true},
				{name: "edgeupdate", on: svcManual, off: svcAuto, offDelayed: true},
				{name: "CscService", on: svcDisabled, off: svcManual},
				{name: "AJRouter", on: svcDisabled, off: svcManual},
				{name: "AssignedAccessManagerSvc", on: svcDisabled, off: svcManual},
				{name: "RetailDemo", on: svcDisabled, off: svcManual},
			},
			reg: svcReg},
		{id: "win32-priority", group: "perf", sys: true,
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\PriorityControl`, "Win32PrioritySeparation", 36, uint32(2))}},
		{id: "menu-delay", group: "perf",
			reg:  []rv{sz(hkcu, `Control Panel\Desktop`, "MenuShowDelay", "0", "400")},
			live: liveMenuDelay},
		{id: "startup-delay-off", group: "perf",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer\Serialize`, "StartupDelayInMSec", 0, nil)}},
		{id: "animations-off", group: "perf", explorer: true,
			reg: []rv{
				sz(hkcu, `Control Panel\Desktop\WindowMetrics`, "MinAnimate", "0", "1"),
				dw(hkcu, advKey, "TaskbarAnimations", 0, uint32(1)),
			}},
		{id: "bg-apps-off", group: "perf",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\BackgroundAccessApplications`, "GlobalUserDisabled", 1, uint32(0))}},
		{id: "fast-startup-off", group: "perf", sys: true,
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\Session Manager\Power`, "HiberbootEnabled", 0, uint32(1))}},
		{id: "hibernation-off", group: "perf", risk: "caution", sys: true,
			detect: func(*probe) string {
				if v, ok := regStr(hklm, `SYSTEM\CurrentControlSet\Control\Power`, "HibernateEnabled"); ok && v == "0" {
					return "on"
				}
				return "off"
			},
			owns:   []string{"power:hibernate"},
			apply:  func(*tctx) error { return runTool(sys32("powercfg.exe"), "/hibernate", "off") },
			revert: func(*tctx) error { return runTool(sys32("powercfg.exe"), "/hibernate", "on") },
			snap: func() (map[string]string, error) {
				// "~type" is not compared afterwards: turning hibernation on
				// can create HiberFileType even where it was missing before
				m := map[string]string{"hiber": "absent", "~type": "absent"}
				if v, ok := regStr(hklm, `SYSTEM\CurrentControlSet\Control\Power`, "HibernateEnabled"); ok {
					m["hiber"] = v
				}
				if v, ok := regStr(hklm, `SYSTEM\CurrentControlSet\Control\Power`, "HiberFileType"); ok {
					m["~type"] = v
				}
				return m, nil
			},
			restore: func(m map[string]string) error {
				if m["hiber"] == "0" {
					return runTool(sys32("powercfg.exe"), "/hibernate", "off")
				}
				if err := runTool(sys32("powercfg.exe"), "/hibernate", "on"); err != nil {
					return err
				}
				switch m["~type"] { // 1 reduced, 2 full
				case "1":
					return runTool(sys32("powercfg.exe"), "/hibernate", "/type", "reduced")
				case "2":
					return runTool(sys32("powercfg.exe"), "/hibernate", "/type", "full")
				}
				return nil
			}},
		{id: "ultimate-power", group: "perf", risk: "caution", sys: true,
			detect: func(*probe) string {
				if g := activeScheme(); g == ultimateOurs || g == ultimateBase {
					return "on"
				}
				return "off"
			},
			owns: []string{"power:scheme"},
			snap: func() (map[string]string, error) {
				cur := activeScheme()
				if cur == "" {
					return nil, errors.New("powercfg /getactivescheme")
				}
				return map[string]string{"scheme": cur, "ours": strconv.FormatBool(schemeExists(ultimateOurs))}, nil
			},
			restore: func(m map[string]string) error {
				if g := m["scheme"]; g != "" && g != activeScheme() {
					if !schemeExists(g) {
						return errors.New(T("tw.err.scheme", g))
					}
					if err := runTool(sys32("powercfg.exe"), "-setactive", g); err != nil {
						return err
					}
				}
				if m["ours"] == "false" && schemeExists(ultimateOurs) && activeScheme() != ultimateOurs {
					_ = runTool(sys32("powercfg.exe"), "-delete", ultimateOurs)
				}
				return nil
			},
			apply: func(c *tctx) error {
				if !schemeExists(ultimateOurs) {
					if err := runTool(sys32("powercfg.exe"), "-duplicatescheme", ultimateBase, ultimateOurs); err != nil {
						return errors.New(T("tw.err.ultimate"))
					}
				}
				return runTool(sys32("powercfg.exe"), "-setactive", ultimateOurs)
			},
			revert: func(c *tctx) error { // Windows default (only with the user's OK)
				if err := runTool(sys32("powercfg.exe"), "-setactive", balancedPlan); err != nil {
					return err
				}
				_ = runTool(sys32("powercfg.exe"), "-delete", ultimateOurs)
				return nil
			}},
		{id: "dynamic-tick", group: "perf", risk: "caution", reboot: true, sys: true,
			detect: func(*probe) string {
				if _, on := bcdFlag(bcdCurrent(), "disabledynamictick"); on {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error {
				return runTool(sys32("bcdedit.exe"), "/set", "{current}", "disabledynamictick", "yes")
			},
			revert: func(*tctx) error {
				if present, _ := bcdFlag(bcdCurrent(), "disabledynamictick"); !present {
					return nil
				}
				return runTool(sys32("bcdedit.exe"), "/deletevalue", "{current}", "disabledynamictick")
			},
			owns: []string{"bcd:disabledynamictick"},
			snap: func() (map[string]string, error) {
				out := bcdCurrent()
				if strings.TrimSpace(out) == "" {
					return nil, errors.New("bcdedit /enum {current}")
				}
				present, on := bcdFlag(out, "disabledynamictick")
				v := "absent"
				if present {
					v = map[bool]string{true: "yes", false: "no"}[on]
				}
				return map[string]string{"bcd": v}, nil
			},
			restore: func(m map[string]string) error {
				present, on := bcdFlag(bcdCurrent(), "disabledynamictick")
				switch m["bcd"] {
				case "absent":
					if present {
						return runTool(sys32("bcdedit.exe"), "/deletevalue", "{current}", "disabledynamictick")
					}
				case "yes", "no":
					if !present || on != (m["bcd"] == "yes") {
						return runTool(sys32("bcdedit.exe"), "/set", "{current}", "disabledynamictick", m["bcd"])
					}
				}
				return nil
			}},
		{id: "core-isolation-off", group: "perf", risk: "risky", reboot: true, sys: true,
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\DeviceGuard\Scenarios\HypervisorEnforcedCodeIntegrity`, "Enabled", 0, uint32(1))}},
		{id: "defender-rtp-off", group: "perf", risk: "risky", sys: true, slow: true,
			avail: func(p *probe) string {
				f := p.facts()
				// Older Windows 10 builds do not report the running mode.
				if f.RTP == nil || (f.Mode != "" && !strings.EqualFold(f.Mode, "Normal")) {
					return T("tw.na.defender")
				}
				if f.Tamper != nil && *f.Tamper && *f.RTP {
					return T("tw.na.tamper")
				}
				return ""
			},
			detect: func(p *probe) string {
				if f := p.facts(); f.RTP != nil && !*f.RTP {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error {
				if err := psRun(2*time.Minute, "Set-MpPreference -DisableRealtimeMonitoring $true -ErrorAction Stop; Start-Sleep -Seconds 2; "+
					"if ((Get-MpComputerStatus).RealTimeProtectionEnabled) { exit 3 }"); err != nil {
					return errors.New(T("tw.err.tamper"))
				}
				return nil
			},
			revert: func(*tctx) error {
				return psRun(2*time.Minute, "Set-MpPreference -DisableRealtimeMonitoring $false -ErrorAction Stop")
			},
			owns: []string{"defender:realtime"},
			snap: func() (map[string]string, error) {
				out, _, err := runPS(time.Minute, "[string](Get-MpPreference -ErrorAction Stop).DisableRealtimeMonitoring")
				v := strings.ToLower(strings.TrimSpace(out))
				if err != nil || (v != "true" && v != "false") {
					return nil, errors.New("Get-MpPreference")
				}
				return map[string]string{"disableRtp": v}, nil
			},
			restore: func(m map[string]string) error {
				v := "$false"
				if m["disableRtp"] == "true" {
					v = "$true"
				}
				return psRun(2*time.Minute, "Set-MpPreference -DisableRealtimeMonitoring "+v+" -ErrorAction Stop")
			}},

		// ---------------- Gaming
		{id: "game-mode", group: "gaming", rec: true,
			reg: []rv{
				{root: hkcu, path: `Software\Microsoft\GameBar`, name: "AllowAutoGameMode", kind: registry.DWORD, on: uint32(1), off: uint32(0), absentOn: true},
				{root: hkcu, path: `Software\Microsoft\GameBar`, name: "AutoGameModeEnabled", kind: registry.DWORD, on: uint32(1), off: uint32(0), absentOn: true},
			}},
		{id: "gamebar-off", group: "gaming", risk: "caution", sys: true,
			reg: []rv{
				dw(hkcu, `System\GameConfigStore`, "GameDVR_Enabled", 0, uint32(1)),
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\GameDVR`, "AppCaptureEnabled", 0, nil),
				dw(hkcu, `Software\Microsoft\GameBar`, "UseNexusForGameBarEnabled", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\GameDVR`, "AllowGameDVR", 0, nil),
			}},
		{id: "hags", group: "gaming", reboot: true, sys: true,
			avail: func(p *probe) string {
				if p.build < 19041 {
					return T("tw.na.build")
				}
				return ""
			},
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\GraphicsDrivers`, "HwSchMode", 2, uint32(1))}},
		{id: "windowed-opt", group: "gaming",
			avail: func(p *probe) string {
				if !p.win11() {
					return T("tw.na.win11")
				}
				return ""
			},
			detect: func(*probe) string {
				s, _ := regStr(hkcu, `Software\Microsoft\DirectX\UserGpuPreferences`, "DirectXUserGlobalSettings")
				if dxGet(s, "SwapEffectUpgradeEnable") == "1" {
					return "on"
				}
				return "off"
			},
			owns: []string{"dx:SwapEffectUpgradeEnable"},
			// The whole DirectX settings value (it holds other settings too)
			// is saved and put back as it was.
			snap: func() (map[string]string, error) {
				s, ok := regStr(hkcu, `Software\Microsoft\DirectX\UserGpuPreferences`, "DirectXUserGlobalSettings")
				if !ok {
					return map[string]string{"dx": "\x00"}, nil
				}
				return map[string]string{"dx": s}, nil
			},
			restore: func(m map[string]string) error {
				const p = `Software\Microsoft\DirectX\UserGpuPreferences`
				if m["dx"] == "\x00" {
					delRegValue(hkcu, p, "DirectXUserGlobalSettings")
					return nil
				}
				return setRegStr(hkcu, p, "DirectXUserGlobalSettings", m["dx"])
			},
			apply: func(c *tctx) error {
				s, _ := regStr(hkcu, `Software\Microsoft\DirectX\UserGpuPreferences`, "DirectXUserGlobalSettings")
				return setRegStr(hkcu, `Software\Microsoft\DirectX\UserGpuPreferences`, "DirectXUserGlobalSettings", dxSet(s, "SwapEffectUpgradeEnable", "1"))
			},
			revert: func(c *tctx) error {
				const p = `Software\Microsoft\DirectX\UserGpuPreferences`
				s, _ := regStr(hkcu, p, "DirectXUserGlobalSettings")
				if s = dxDel(s, "SwapEffectUpgradeEnable"); s == "" {
					delRegValue(hkcu, p, "DirectXUserGlobalSettings")
					return nil
				}
				return setRegStr(hkcu, p, "DirectXUserGlobalSettings", s)
			}},
		{id: "games-priority", group: "gaming", sys: true, reboot: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Multimedia\SystemProfile\Tasks\Games`, "Priority", 6, uint32(2)),
				sz(hklm, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Multimedia\SystemProfile\Tasks\Games`, "Scheduling Category", "High", "Medium"),
				sz(hklm, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Multimedia\SystemProfile\Tasks\Games`, "SFIO Priority", "High", "Normal"),
				dw(hklm, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Multimedia\SystemProfile`, "SystemResponsiveness", 10, uint32(20)),
			}},
		{id: "mouse-accel-off", group: "gaming",
			reg: []rv{
				sz(hkcu, `Control Panel\Mouse`, "MouseSpeed", "0", "1"),
				sz(hkcu, `Control Panel\Mouse`, "MouseThreshold1", "0", "6"),
				sz(hkcu, `Control Panel\Mouse`, "MouseThreshold2", "0", "10"),
			},
			live: liveMouse},
		{id: "nvidia-opt", group: "gaming", sys: true,
			avail: func(*probe) string {
				if !nvAvailable() {
					return T("tw.na.nvidia")
				}
				return ""
			},
			detect: func(*probe) string { return nvState() },
			apply:  func(*tctx) error { return nvApply() },
			revert: func(*tctx) error { return nvRevert() },
			owns:   []string{"nvidia:base-profile"},
			snap: func() (map[string]string, error) {
				v, err := nvSnap()
				if err != nil {
					return nil, err
				}
				return map[string]string{"nv": v}, nil
			},
			restore: func(m map[string]string) error { return nvRestore(m["nv"]) }},

		// ---------------- Privacy
		{id: "telemetry-off", group: "privacy", rec: true, sys: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\DataCollection`, "AllowTelemetry", 0, nil),
				dw(hklm, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\DataCollection`, "AllowTelemetry", 0, nil),
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\AdvertisingInfo`, "Enabled", 0, nil),
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Privacy`, "TailoredExperiencesWithDiagnosticDataEnabled", 0, nil),
				dw(hkcu, `Software\Microsoft\Speech_OneCore\Settings\OnlineSpeechPrivacy`, "HasAccepted", 0, nil),
				dw(hkcu, `Software\Microsoft\Input\TIPC`, "Enabled", 0, nil),
				dw(hkcu, `Software\Microsoft\InputPersonalization`, "RestrictImplicitInkCollection", 1, nil),
				dw(hkcu, `Software\Microsoft\InputPersonalization`, "RestrictImplicitTextCollection", 1, nil),
				dw(hkcu, `Software\Microsoft\InputPersonalization\TrainedDataStore`, "HarvestContacts", 0, nil),
				dw(hkcu, `Software\Microsoft\Personalization\Settings`, "AcceptedPrivacyPolicy", 0, nil),
				dw(hkcu, advKey, "Start_TrackProgs", 0, nil),
				dw(hkcu, `Software\Microsoft\Siuf\Rules`, "NumberOfSIUFInPeriod", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\Windows Error Reporting`, "Disabled", 1, nil),
				dw(hklm, `SOFTWARE\Microsoft\Windows\Windows Error Reporting`, "Disabled", 1, nil),
				sz(hklm, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "POWERSHELL_TELEMETRY_OPTOUT", "1", nil),
			},
			svc: []svcSet{
				{name: "DiagTrack", on: svcDisabled, off: svcAuto},
				{name: "WerSvc", on: svcDisabled, off: svcManual},
			},
			setting: "Environment",
			owns:    []string{"defender:samples"},
			apply: func(c *tctx) error {
				// only where Microsoft Defender is in use
				_, _, _ = runPS(time.Minute, "try { $null=Get-MpPreference -ErrorAction Stop; Set-MpPreference -SubmitSamplesConsent 2 -ErrorAction Stop } catch {}")
				return nil
			},
			revert: func(c *tctx) error {
				_, _, _ = runPS(time.Minute, "try { Set-MpPreference -SubmitSamplesConsent 1 -ErrorAction Stop } catch {}")
				return nil
			},
			snap: func() (map[string]string, error) {
				out, _, _ := runPS(time.Minute, "try { [string][int](Get-MpPreference -ErrorAction Stop).SubmitSamplesConsent } catch { 'none' }")
				v := strings.TrimSpace(out)
				if _, err := strconv.Atoi(v); err != nil {
					v = "none" // Defender not in use: nothing to restore
				}
				return map[string]string{"samples": v}, nil
			},
			restore: func(m map[string]string) error {
				v := m["samples"]
				if _, err := strconv.Atoi(v); err != nil {
					return nil
				}
				return psRun(time.Minute, "Set-MpPreference -SubmitSamplesConsent "+v+" -ErrorAction Stop")
			}},
		{id: "activity-off", group: "privacy", rec: true, sys: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\System`, "PublishUserActivities", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\System`, "UploadUserActivities", 0, nil),
			}},
		{id: "consumer-off", group: "privacy", rec: true, sys: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\CloudContent`, "DisableWindowsConsumerFeatures", 1, nil),
				dw(hkcu, cdmKey, "SilentInstalledAppsEnabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "SystemPaneSuggestionsEnabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "PreInstalledAppsEnabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "OemPreInstalledAppsEnabled", 0, uint32(1)),
			}},
		{id: "location-off", group: "privacy", sys: true,
			reg: []rv{
				sz(hklm, `SOFTWARE\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\location`, "Value", "Deny", "Allow"),
				dw(hklm, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Sensor\Overrides\{BFA794E4-F964-4FDB-90F6-51056BFE4B44}`, "SensorPermissionState", 0, uint32(1)),
				dw(hklm, `SYSTEM\Maps`, "AutoUpdateEnabled", 0, uint32(1)),
			},
			svc: []svcSet{{name: "lfsvc", on: svcDisabled, off: svcManual}}},
		{id: "copilot-off", group: "privacy", sys: true, slow: true,
			reg: []rv{
				dw(hkcu, `Software\Policies\Microsoft\Windows\WindowsCopilot`, "TurnOffWindowsCopilot", 1, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\WindowsCopilot`, "TurnOffWindowsCopilot", 1, nil),
				dw(hkcu, advKey, "ShowCopilotButton", 0, nil),
			},
			detect: func(p *probe) string {
				t := findTweak("copilot-off")
				rs, app := t.regState(), p.hasApp("Microsoft.Copilot*")
				switch {
				case rs == "on" && !app:
					return "on"
				case rs == "off" && app:
					return "off"
				case rs == "off":
					return "off"
				}
				return "partial"
			},
			owns:  []string{"appx:Microsoft.Copilot"},
			apply: func(c *tctx) error { return removeApps([]string{"Microsoft.Copilot*"}) },
			revert: func(c *tctx) error {
				_, err := wingetInstall("ms-windows-store://pdp/?ProductId=9NHT9RB2F4HD", "--id", "9NHT9RB2F4HD", "--source", "msstore")
				return err
			},
			snap: func() (map[string]string, error) {
				return map[string]string{"~app": strconv.FormatBool(newProbe().hasApp("Microsoft.Copilot*"))}, nil
			},
			restore: func(m map[string]string) error {
				if m["~app"] != "true" || newProbe().hasApp("Microsoft.Copilot*") {
					return nil
				}
				_, err := wingetInstall("ms-windows-store://pdp/?ProductId=9NHT9RB2F4HD", "--id", "9NHT9RB2F4HD", "--source", "msstore")
				return err
			}},
		{id: "recall-off", group: "privacy", reboot: true, sys: true,
			avail: func(p *probe) string {
				if p.build < 26100 {
					return T("tw.na.recall")
				}
				return ""
			},
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\WindowsAI`, "AllowRecallEnablement", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\WindowsAI`, "DisableAIDataAnalysis", 1, nil),
				dw(hkcu, `Software\Policies\Microsoft\Windows\WindowsAI`, "DisableAIDataAnalysis", 1, nil),
			},
			owns: []string{"feature:Recall"},
			apply: func(c *tctx) error {
				if recallFeature() == "absent" {
					return nil
				}
				_, code, _ := runQuiet(15*time.Minute, sys32("dism.exe"), "/Online", "/Disable-Feature", "/FeatureName:Recall", "/NoRestart")
				if code != 0 && code != 3010 {
					return errors.New(T("tw.err.code", code))
				}
				return nil
			},
			snap: func() (map[string]string, error) { return map[string]string{"~feature": recallFeature()}, nil },
			restore: func(m map[string]string) error {
				if strings.HasPrefix(m["~feature"], "enable") && !strings.HasPrefix(recallFeature(), "enable") {
					_, code, _ := runQuiet(15*time.Minute, sys32("dism.exe"), "/Online", "/Enable-Feature", "/FeatureName:Recall", "/NoRestart")
					if code != 0 && code != 3010 {
						return errors.New(T("tw.err.code", code))
					}
				}
				return nil
			}},
		{id: "start-recs-off", group: "privacy", explorer: true, avail: needWin11,
			reg: []rv{
				dw(hkcu, advKey, "Start_IrisRecommendations", 0, uint32(1)),
				dw(hkcu, advKey, "Start_AccountNotifications", 0, uint32(1)),
			}},
		{id: "settings-ads-off", group: "privacy",
			reg: []rv{
				dw(hkcu, cdmKey, "SubscribedContent-338393Enabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "SubscribedContent-353694Enabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "SubscribedContent-353696Enabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "SoftLandingEnabled", 0, uint32(1)),
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\UserProfileEngagement`, "ScoobeSystemSettingEnabled", 0, nil),
			}},
		{id: "bing-off", group: "privacy",
			reg: []rv{
				dw(hkcu, searchKey, "BingSearchEnabled", 0, nil),
				dw(hkcu, searchKey, "CortanaConsent", 0, nil),
				dw(hkcu, `Software\Policies\Microsoft\Windows\Explorer`, "DisableSearchBoxSuggestions", 1, nil),
			}},
		{id: "lockscreen-tips-off", group: "privacy",
			reg: []rv{
				dw(hkcu, cdmKey, "RotatingLockScreenOverlayEnabled", 0, uint32(1)),
				dw(hkcu, cdmKey, "SubscribedContent-338387Enabled", 0, uint32(1)),
			}},
		{id: "wpbt-off", group: "privacy", risk: "caution", reboot: true, sys: true,
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\Session Manager`, "DisableWpbtExecution", 1, nil)}},
		{id: "rdp-warn-off", group: "privacy", risk: "risky", sys: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services\Client`, "RedirectionWarningDialogVersion", 1, nil),
				dw(hkcu, `Software\Microsoft\Terminal Server Client`, "RdpLaunchConsentAccepted", 1, nil),
			}},

		// ---------------- Appearance and taskbar
		{id: "transparency-off", group: "look", setting: "ImmersiveColorSet",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, "EnableTransparency", 0, uint32(1))}},
		{id: "dark-mode", group: "look", setting: "ImmersiveColorSet",
			reg: []rv{
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, "AppsUseLightTheme", 0, uint32(1)),
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, "SystemUsesLightTheme", 0, uint32(1)),
			}},
		{id: "classic-menu", group: "look", explorer: true,
			avail: needWin11,
			reg: []rv{{root: hkcu, path: ctxMenu + `\InprocServer32`, name: "", kind: registry.SZ, on: "", off: nil,
				keys: []string{ctxMenu + `\InprocServer32`, ctxMenu}}}},
		{id: "taskbar-left", group: "look", setting: "TraySettings", avail: needWin11,
			reg: []rv{dw(hkcu, advKey, "TaskbarAl", 0, uint32(1))}},
		{id: "taskbar-search-off", group: "look", explorer: true, setting: "TraySettings",
			reg: []rv{dw(hkcu, searchKey, "SearchboxTaskbarMode", 0, uint32(1))}},
		{id: "taskview-widgets-off", group: "look", explorer: true, sys: true, setting: "TraySettings",
			reg: []rv{
				dw(hkcu, advKey, "ShowTaskViewButton", 0, uint32(1)),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Dsh`, "AllowNewsAndInterests", 0, nil),
			}},
		{id: "clock-seconds", group: "look", explorer: true, setting: "TraySettings",
			reg: []rv{dw(hkcu, advKey, "ShowSecondsInSystemClock", 1, uint32(0))}},
		{id: "clock-24h", group: "look", setting: "intl",
			reg: []rv{
				sz(hkcu, `Control Panel\International`, "sShortTime", "HH:mm", "h:mm tt"),
				sz(hkcu, `Control Panel\International`, "sTimeFormat", "HH:mm:ss", "h:mm:ss tt"),
			},
			detect: func(*probe) string {
				s, _ := regStr(hkcu, `Control Panel\International`, "sShortTime")
				if is24h(s) {
					return "on"
				}
				return "off"
			}},
		{id: "end-task", group: "look", rec: true, explorer: true,
			avail: func(p *probe) string {
				if p.build < 22631 {
					return T("tw.na.build")
				}
				return ""
			},
			reg: []rv{dw(hkcu, advKey+`\TaskbarDeveloperSettings`, "TaskbarEndTask", 1, nil)}},

		// ---------------- Explorer and input
		{id: "recent-files-off", group: "explorer", explorer: true,
			reg: []rv{
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer`, "ShowRecent", 0, uint32(1)),
				dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer`, "ShowFrequent", 0, uint32(1)),
			}},
		{id: "shortcut-suffix-off", group: "explorer",
			reg: []rv{sz(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer\NamingTemplates`, "ShortcutNameTemplate", "%s.lnk", nil)}},
		{id: "compact-view", group: "explorer", explorer: true, avail: needWin11,
			reg: []rv{dw(hkcu, advKey, "UseCompactMode", 1, uint32(0))}},
		{id: "folder-discovery-off", group: "explorer", explorer: true,
			reg: []rv{sz(hkcu, `Software\Classes\Local Settings\Software\Microsoft\Windows\Shell\Bags\AllFolders\Shell`, "FolderType", "NotSpecified", nil)}},
		{id: "file-ext", group: "explorer", rec: true, explorer: true,
			reg: []rv{dw(hkcu, advKey, "HideFileExt", 0, uint32(1))}},
		{id: "hidden-files", group: "explorer", explorer: true,
			reg: []rv{dw(hkcu, advKey, "Hidden", 1, uint32(2))}},
		{id: "home-gallery-off", group: "explorer", explorer: true, avail: needWin11,
			reg: []rv{
				{root: hkcu, path: homeCLSID, name: "System.IsPinnedToNameSpaceTree", kind: registry.DWORD, on: uint32(0), keys: []string{homeCLSID}},
				{root: hkcu, path: galCLSID, name: "System.IsPinnedToNameSpaceTree", kind: registry.DWORD, on: uint32(0), keys: []string{galCLSID}},
				dw(hkcu, advKey, "LaunchTo", 1, nil),
			}},
		{id: "sticky-keys-off", group: "explorer", rec: true,
			reg:  []rv{sz(hkcu, `Control Panel\Accessibility\StickyKeys`, "Flags", "506", "510")},
			live: liveStickyKeys},
		{id: "numlock-on", group: "explorer",
			reg: []rv{
				sz(hkcu, `Control Panel\Keyboard`, "InitialKeyboardIndicators", "2", "0"),
				sz(hku, `.DEFAULT\Control Panel\Keyboard`, "InitialKeyboardIndicators", "2", "2147483648"),
			}},

		// ---------------- System
		{id: "auto-reboot-off", group: "system", sys: true,
			reg: []rv{dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU`, "NoAutoRebootWithLoggedOnUsers", 1, nil)}},
		{id: "detailed-bsod", group: "system", sys: true,
			reg: []rv{
				dw(hklm, `SYSTEM\CurrentControlSet\Control\CrashControl`, "DisplayParameters", 1, nil),
				dw(hklm, `SYSTEM\CurrentControlSet\Control\CrashControl`, "DisableEmoticon", 1, nil),
			}},
		{id: "verbose-status", group: "system", sys: true,
			reg: []rv{dw(hklm, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, "VerboseStatus", 1, nil)}},
		{id: "long-paths", group: "system", sys: true,
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\FileSystem`, "LongPathsEnabled", 1, uint32(0))}},
		{id: "utc-time", group: "system", risk: "caution", reboot: true, sys: true,
			reg: []rv{{root: hklm, path: `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, name: "RealTimeIsUniversal", kind: registry.QWORD, on: uint64(1), off: nil}}},
		{id: "ps7-default", group: "system",
			avail: func(*probe) string {
				if !fileExists(wtSettingsPath()) {
					return T("tw.na.wt")
				}
				return ""
			},
			detect: func(*probe) string {
				b, err := os.ReadFile(wtSettingsPath())
				if err == nil && wtDefaultProfile(string(b)) == ps7Profile {
					return "on"
				}
				return "off"
			},
			owns: []string{"wt:defaultProfile"},
			snap: func() (map[string]string, error) {
				b, err := os.ReadFile(wtSettingsPath())
				if err != nil {
					return nil, err
				}
				return map[string]string{"profile": wtDefaultProfile(string(b))}, nil
			},
			restore: func(m map[string]string) error {
				if m["profile"] == "" {
					return nil
				}
				return setWTDefault(m["profile"])
			},
			apply: func(c *tctx) error {
				if !pwsh7Installed() {
					if _, err := wingetInstall("", "--id", "Microsoft.PowerShell", "-e", "--source", "winget"); err != nil || !pwsh7Installed() {
						return errors.New(T("tw.err.ps7"))
					}
				}
				return setWTDefault(ps7Profile)
			},
			revert: func(c *tctx) error { return setWTDefault(ps5Profile) }},
		{id: "bitlocker-off", group: "system", risk: "risky", oneWay: true, sys: true, slow: true, owns: []string{"bitlocker:system-drive"},
			avail: func(p *probe) string {
				f := p.facts()
				if strings.EqualFold(f.BL, "None") && f.BLP == "" {
					return T("tw.na.bitlocker")
				}
				return ""
			},
			detect: func(p *probe) string {
				f := p.facts()
				switch strings.ToLower(f.BL) {
				case "fullydecrypted", "decryptioninprogress":
					return "on"
				case "":
					if f.BLP == "0" || strings.EqualFold(f.BLP, "Off") {
						return "on"
					}
				}
				return "off"
			},
			apply: func(*tctx) error { return runTool(sys32("manage-bde.exe"), "-off", systemDrive()) }},

		// ---------------- Network
		// Removed in 2.4: Windows does not let these TCP settings be read back
		// reliably, so neither the result nor an exact undo can be verified.
		// Kept only so a change made with 2.3 can still be turned off.
		{id: "net-throttle-off", group: "network", sys: true, reboot: true,
			reg: []rv{dw(hklm, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Multimedia\SystemProfile`, "NetworkThrottlingIndex", 0xffffffff, uint32(10))}},
		{id: "net-opt", group: "network", risk: "caution", reboot: true, sys: true, legacy: true,
			owns:   []string{"netsh:tcp-global"},
			detect: func(*probe) string { return map[bool]string{true: "on", false: "off"}[getBackup("net-opt") != nil] },
			apply:  func(*tctx) error { return errors.New(T("tw.err.legacy")) },
			revert: func(*tctx) error {
				ns := sys32("netsh.exe")
				for _, a := range [][]string{
					{"int", "tcp", "set", "heuristics", "enabled"},
					{"int", "tcp", "set", "supplemental", "template=internet", "congestionprovider=default"},
					{"int", "tcp", "set", "global", "rss=default"},
					{"int", "tcp", "set", "global", "ecncapability=default"},
					{"int", "tcp", "set", "global", "timestamps=default"},
					{"int", "tcp", "set", "global", "fastopen=default"},
					{"int", "tcp", "set", "global", "fastopenfallback=default"},
				} {
					_, _, _ = runQuiet(time.Minute, ns, a...)
				}
				return nil
			}},
		{id: "do-off", group: "network", rec: true, sys: true,
			reg: []rv{dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\DeliveryOptimization`, "DODownloadMode", 0, nil)}},
		{id: "wifi-sense-off", group: "network", sys: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Microsoft\PolicyManager\default\WiFi\AllowWiFiHotSpotReporting`, "Value", 0, uint32(1)),
				dw(hklm, `SOFTWARE\Microsoft\PolicyManager\default\WiFi\AllowAutoConnectToWiFiSenseHotspots`, "Value", 0, uint32(1)),
			}},

		// ---------------- 3.0.0 (several after ReviOS's playbook; the values are Windows' own settings)
		{id: "appcompat-off", group: "privacy", rec: true, sys: true, since: "3.0.0",
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\AppCompat`, "AITEnable", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\AppCompat`, "DisableInventory", 1, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\AppCompat`, "DisableUAR", 1, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\AppCompat`, "DisablePCA", 1, nil),
			}},
		{id: "ceip-off", group: "privacy", rec: true, sys: true, since: "3.0.0",
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\SQMClient\Windows`, "CEIPEnable", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\AppV\CEIP`, "CEIPEnable", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Internet Explorer\SQM`, "DisableCustomerImprovementProgram", 1, nil),
			}},
		{id: "typing-insights-off", group: "privacy", since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Input\Settings`, "InsightsEnabled", 0, nil)}},
		{id: "spotlight-off", group: "privacy", since: "3.0.0", explorer: true,
			reg: []rv{
				// the policy turns everything off on Enterprise and Education; the
				// other two are what Settings changes on every edition
				dw(hkcu, `Software\Policies\Microsoft\Windows\CloudContent`, "DisableWindowsSpotlightFeatures", 1, nil),
				dw(hkcu, `Software\Policies\Microsoft\Windows\CloudContent`, "DisableSpotlightCollectionOnDesktop", 1, nil),
				dw(hkcu, cdmKey, "RotatingLockScreenEnabled", 0, uint32(1)),
			}},
		{id: "shutdown-fast", group: "perf", rec: true, sys: true, since: "3.0.0",
			reg: []rv{
				sz(hkcu, `Control Panel\Desktop`, "HungAppTimeout", "2000", nil),
				sz(hkcu, `Control Panel\Desktop`, "WaitToKillAppTimeout", "2000", nil),
				sz(hklm, `SYSTEM\CurrentControlSet\Control`, "WaitToKillServiceTimeout", "2000", "5000"),
			}},
		{id: "search-low-priority", group: "perf", sys: true, reboot: true, since: "3.0.0",
			reg: []rv{
				{root: hklm, path: ifeoKey + `\SearchIndexer.exe\PerfOptions`, name: "CpuPriorityClass", kind: registry.DWORD, on: uint32(5), keys: []string{ifeoKey + `\SearchIndexer.exe\PerfOptions`, ifeoKey + `\SearchIndexer.exe`}},
				{root: hklm, path: ifeoKey + `\SearchIndexer.exe\PerfOptions`, name: "IoPriority", kind: registry.DWORD, on: uint32(1), keys: []string{ifeoKey + `\SearchIndexer.exe\PerfOptions`, ifeoKey + `\SearchIndexer.exe`}},
			}},
		// 3.4: more for speed — all registry values Windows itself has, each put back exactly
		{id: "paging-exec-off", group: "perf", sys: true, reboot: true, since: "3.4.0",
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management`, "DisablePagingExecutive", 1, uint32(0))}},
		{id: "ntfs-lastaccess-off", group: "perf", sys: true, reboot: true, since: "3.4.0",
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\FileSystem`, "NtfsDisableLastAccessUpdate", 0x80000001, uint32(0x80000002))}},
		{id: "icon-cache-large", group: "perf", sys: true, explorer: true, since: "3.4.0",
			reg: []rv{sz(hklm, `SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer`, "Max Cached Icons", "4096", nil)}},
		{id: "hover-fast", group: "perf", since: "3.4.0",
			reg:  []rv{sz(hkcu, `Control Panel\Mouse`, "MouseHoverTime", "100", "400")},
			live: liveHoverTime},
		{id: "keyboard-fast", group: "perf", since: "3.4.0",
			reg:  []rv{sz(hkcu, `Control Panel\Keyboard`, "KeyboardDelay", "0", "1")},
			live: liveKeyboardDelay},
		{id: "gpu-tdr-longer", group: "perf", risk: "caution", sys: true, reboot: true, since: "3.4.0",
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\GraphicsDrivers`, "TdrDelay", 8, nil)}},
		{id: "timer-res-global", group: "perf", sys: true, reboot: true, since: "3.4.0",
			avail: func(p *probe) string {
				if !p.win11() {
					return T("tw.na.win11")
				}
				return ""
			},
			reg: []rv{dw(hklm, `SYSTEM\CurrentControlSet\Control\Session Manager\kernel`, "GlobalTimerResolutionRequests", 1, nil)}},
		{id: "ducking-off", group: "gaming", rec: true, since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Multimedia\Audio`, "UserDuckingPreference", 3, nil)}},
		{id: "full-path-title", group: "explorer", explorer: true, since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer\CabinetState`, "FullPath", 1, uint32(0))}},
		{id: "copy-details", group: "explorer", rec: true, since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer\OperationStatusManager`, "EnthusiastMode", 1, nil)}},
		{id: "autoplay-off", group: "explorer", rec: true, since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer\AutoplayHandlers`, "DisableAutoplay", 1, uint32(0))}},
		{id: "clipboard-history", group: "explorer", since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Clipboard`, "EnableClipboardHistory", 1, uint32(0))}},
		{id: "taskbar-labels", group: "look", explorer: true, since: "3.0.0",
			avail: func(p *probe) string {
				if p.build >= 22000 && p.build < 22631 {
					return T("tw.na.build") // Windows 11 before 23H2 always combines
				}
				return ""
			},
			reg: []rv{
				dw(hkcu, advKey, "TaskbarGlomLevel", 2, uint32(0)),
				dw(hkcu, advKey, "MMTaskbarGlomLevel", 2, uint32(0)),
			}},
		{id: "logon-blur-off", group: "look", sys: true, since: "3.0.0",
			reg: []rv{dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\System`, "DisableAcrylicBackgroundOnLogon", 1, nil)}},
		{id: "edge-shortcut-off", group: "apps", sys: true, since: "3.0.0",
			reg: []rv{dw(hklm, `SOFTWARE\Policies\Microsoft\EdgeUpdate`, "CreateDesktopShortcutDefault", 0, nil)}},
		{id: "explorer-ads-off", group: "privacy", rec: true, explorer: true, since: "3.0.0",
			reg: []rv{dw(hkcu, advKey, "ShowSyncProviderNotifications", 0, uint32(1))}},
		{id: "store-openwith-off", group: "explorer", sys: true, since: "3.0.0",
			reg: []rv{dw(hklm, `SOFTWARE\Policies\Microsoft\Windows\Explorer`, "NoUseStoreOpenWith", 1, nil)}},
		{id: "multi-select-menu", group: "explorer", since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows\CurrentVersion\Explorer`, "MultipleInvokePromptMinimum", 100, nil)}},
		{id: "printer-manual", group: "system", since: "3.0.0",
			reg: []rv{dw(hkcu, `Software\Microsoft\Windows NT\CurrentVersion\Windows`, "LegacyDefaultPrinterMode", 1, uint32(0))}},

		// ---------------- Apps
		{id: "edge-bg-off", group: "apps", sys: true,
			reg: []rv{
				dw(hklm, `SOFTWARE\Policies\Microsoft\Edge`, "StartupBoostEnabled", 0, nil),
				dw(hklm, `SOFTWARE\Policies\Microsoft\Edge`, "BackgroundModeEnabled", 0, nil),
			}},
		{id: "debloat", group: "apps", risk: "caution", oneWay: true, sys: true, slow: true, checks: true, owns: []string{"appx:built-in apps"},
			detect: func(p *probe) string {
				for _, a := range bloatApps {
					if a.Def && p.hasApp(a.Pkg) {
						return "off"
					}
				}
				return "on"
			},
			apply: func(c *tctx) error {
				want, _ := c.opts["apps"].([]string)
				allowed := map[string]bool{}
				for _, a := range bloatApps {
					allowed[a.Pkg] = true
				}
				var pick []string
				for _, w := range want {
					if allowed[w] { // only packages from the list above
						pick = append(pick, w)
					}
				}
				if len(pick) == 0 {
					return errors.New(T("tw.err.noapps"))
				}
				if err := removeApps(pick); err != nil {
					return err
				}
				// check that every chosen app is really gone
				p := newProbe()
				var left []string
				for _, a := range bloatApps {
					for _, w := range pick {
						if a.Pkg == w && p.hasApp(w) {
							left = append(left, a.Name)
						}
					}
				}
				if len(left) > 0 {
					return errors.New(T("tw.err.left", strings.Join(left, ", ")))
				}
				return nil
			}},
		{id: "gaming-apps-off", group: "apps", risk: "caution", oneWay: true, sys: true, slow: true, owns: []string{"appx:Xbox apps"},
			detect: func(p *probe) string {
				if allAppsGone(p, gamingApps) {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error { return removeApps(gamingApps) }},
		{id: "edge-remove", group: "apps", risk: "caution", sys: true, owns: []string{"app:Microsoft Edge"},
			detect: func(*probe) string {
				if edgeExe() == "" {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error {
				setup := edgeSetup()
				if setup == "" {
					return nil // already gone
				}
				if !closeProcesses(map[string]bool{"msedge.exe": true}, 5*time.Second) {
					return errors.New(T("tw.err.appOpen", "Microsoft Edge"))
				}
				// Edge refuses to uninstall unless this legacy stub exists.
				stubDir := filepath.Join(P.windir, `SystemApps\Microsoft.MicrosoftEdge_8wekyb3d8bbwe`)
				if dirExists(stubDir) && !fileExists(filepath.Join(stubDir, "MicrosoftEdge.exe")) {
					_ = os.WriteFile(filepath.Join(stubDir, "MicrosoftEdge.exe"), nil, 0o644)
				}
				_, _, _ = runQuiet(10*time.Minute, setup, "--uninstall", "--system-level", "--force-uninstall")
				if edgeExe() != "" {
					return errors.New(T("tw.err.edge"))
				}
				return nil
			},
			revert: func(*tctx) error {
				_, err := wingetInstall("https://www.microsoft.com/edge/download", "--id", "Microsoft.Edge", "-e", "--source", "winget")
				return err
			}},
		{id: "onedrive-remove", group: "apps", risk: "caution", sys: true, owns: []string{"app:OneDrive"},
			reg: []rv{{root: hkcu, path: odCLSID, name: "System.IsPinnedToNameSpaceTree", kind: registry.DWORD, on: uint32(0), keys: []string{odCLSID}}},
			detect: func(*probe) string {
				if oneDriveUninstall() == "" && oneDriveExe() == "" {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error {
				if exe := oneDriveExe(); exe != "" {
					_, _, _ = runQuiet(30*time.Second, exe, "/shutdown")
				}
				if !closeProcesses(map[string]bool{"onedrive.exe": true}, 5*time.Second) {
					return errors.New(T("tw.err.appOpen", "OneDrive"))
				}
				exe, args := splitCommand(oneDriveUninstall())
				if exe == "" {
					for _, p := range []string{sys32("OneDriveSetup.exe"), filepath.Join(P.windir, `SysWOW64\OneDriveSetup.exe`)} {
						if fileExists(p) {
							exe, args = p, []string{"/uninstall"}
							break
						}
					}
				}
				if exe == "" {
					return nil
				}
				// Like WinUtil: while the uninstaller runs, the OneDrive folder
				// itself cannot be deleted, so the files in it stay put.
				folder := os.Getenv("OneDrive")
				if folder != "" && dirExists(folder) {
					_, _, _ = runQuiet(time.Minute, sys32("icacls.exe"), folder, "/deny", "*S-1-5-32-544:(D,DC)")
					defer runQuiet(time.Minute, sys32("icacls.exe"), folder, "/remove:d", "*S-1-5-32-544")
				}
				_, _, _ = runQuiet(10*time.Minute, exe, append(args, "/silent")...)
				if oneDriveUninstall() != "" {
					return errors.New(T("tw.err.onedrive"))
				}
				return nil
			},
			revert: func(*tctx) error {
				_, err := wingetInstall("https://www.microsoft.com/microsoft-365/onedrive/download", "--id", "Microsoft.OneDrive", "-e", "--source", "winget")
				return err
			}},
	}
}

// recallFeature: the state of the optional feature "Recall" in lower case
// (enabled, disabled, enablepending, ...), or "absent".
func recallFeature() string {
	out, _, err := runPS(2*time.Minute, "try { [string](Get-WindowsOptionalFeature -Online -FeatureName Recall -ErrorAction Stop).State } catch { 'absent' }")
	v := strings.ToLower(strings.TrimSpace(out))
	if err != nil || v == "" {
		return "absent"
	}
	return v
}

func needWin11(p *probe) string {
	if !p.win11() {
		return T("tw.na.win11")
	}
	return ""
}
