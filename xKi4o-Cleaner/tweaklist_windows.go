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
			reg:   []rv{sz(hkcu, `Control Panel\Desktop`, "MenuShowDelay", "0", "400")},
			apply: func(*tctx) error { liveMenuDelay(); return nil }, revert: func(*tctx) error { liveMenuDelay(); return nil }},
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
			apply:  func(*tctx) error { return runTool(sys32("powercfg.exe"), "/hibernate", "off") },
			revert: func(*tctx) error { return runTool(sys32("powercfg.exe"), "/hibernate", "on") }},
		{id: "ultimate-power", group: "perf", risk: "caution", sys: true,
			detect: func(*probe) string {
				if g := activeScheme(); g == ultimateOurs || g == ultimateBase {
					return "on"
				}
				return "off"
			},
			apply: func(c *tctx) error {
				if cur := activeScheme(); cur != "" && cur != ultimateOurs {
					c.setExtra("scheme", cur)
				}
				if !schemeExists(ultimateOurs) {
					if err := runTool(sys32("powercfg.exe"), "-duplicatescheme", ultimateBase, ultimateOurs); err != nil {
						return errors.New(T("tw.err.ultimate"))
					}
				}
				return runTool(sys32("powercfg.exe"), "-setactive", ultimateOurs)
			},
			revert: func(c *tctx) error {
				back := c.extra("scheme")
				if back == "" || back == ultimateOurs || !schemeExists(back) {
					back = balancedPlan
				}
				if err := runTool(sys32("powercfg.exe"), "-setactive", back); err != nil {
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
			apply: func(c *tctx) error {
				s, ok := regStr(hkcu, `Software\Microsoft\DirectX\UserGpuPreferences`, "DirectXUserGlobalSettings")
				if ok {
					c.setExtra("dx", s)
				} else {
					c.setExtra("dx", "\x00")
				}
				return setRegStr(hkcu, `Software\Microsoft\DirectX\UserGpuPreferences`, "DirectXUserGlobalSettings", dxSet(s, "SwapEffectUpgradeEnable", "1"))
			},
			revert: func(c *tctx) error {
				const p = `Software\Microsoft\DirectX\UserGpuPreferences`
				if old := c.extra("dx"); old != "" {
					if old == "\x00" {
						delRegValue(hkcu, p, "DirectXUserGlobalSettings")
						return nil
					}
					return setRegStr(hkcu, p, "DirectXUserGlobalSettings", old)
				}
				s, _ := regStr(hkcu, p, "DirectXUserGlobalSettings")
				if s = dxDel(s, "SwapEffectUpgradeEnable"); s == "" {
					delRegValue(hkcu, p, "DirectXUserGlobalSettings")
					return nil
				}
				return setRegStr(hkcu, p, "DirectXUserGlobalSettings", s)
			}},
		{id: "mouse-accel-off", group: "gaming",
			reg: []rv{
				sz(hkcu, `Control Panel\Mouse`, "MouseSpeed", "0", "1"),
				sz(hkcu, `Control Panel\Mouse`, "MouseThreshold1", "0", "6"),
				sz(hkcu, `Control Panel\Mouse`, "MouseThreshold2", "0", "10"),
			},
			apply: func(*tctx) error { liveMouse(); return nil }, revert: func(*tctx) error { liveMouse(); return nil }},
		{id: "nvidia-opt", group: "gaming", sys: true,
			avail: func(*probe) string {
				if !nvAvailable() {
					return T("tw.na.nvidia")
				}
				return ""
			},
			detect: func(*probe) string { return nvState() },
			apply:  func(*tctx) error { return nvApply() },
			revert: func(*tctx) error { return nvRevert() }},

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
			apply: func(c *tctx) error {
				out, _, _ := runPS(time.Minute, "try { $v=(Get-MpPreference -ErrorAction Stop).SubmitSamplesConsent; Set-MpPreference -SubmitSamplesConsent 2 -ErrorAction Stop; [string]$v } catch {}")
				if v := strings.TrimSpace(out); v != "" {
					c.setExtra("samples", v)
				}
				return nil
			},
			revert: func(c *tctx) error {
				v := c.extra("samples")
				if _, err := strconv.Atoi(v); err != nil {
					v = "1"
				}
				_, _, _ = runPS(time.Minute, "try { Set-MpPreference -SubmitSamplesConsent "+v+" -ErrorAction Stop } catch {}")
				return nil
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
			apply: func(c *tctx) error {
				if newProbe().hasApp("Microsoft.Copilot*") {
					c.setExtra("app", "1")
				}
				return removeApps([]string{"Microsoft.Copilot*"})
			},
			revert: func(c *tctx) error {
				if c.extra("app") != "1" {
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
			apply: func(c *tctx) error {
				_, code, _ := runQuiet(15*time.Minute, sys32("dism.exe"), "/Online", "/Disable-Feature", "/FeatureName:Recall", "/NoRestart")
				if code == 0 || code == 3010 {
					c.setExtra("feature", "disabled")
				}
				return nil
			},
			revert: func(c *tctx) error {
				if c.extra("feature") == "disabled" {
					_, _, _ = runQuiet(15*time.Minute, sys32("dism.exe"), "/Online", "/Enable-Feature", "/FeatureName:Recall", "/NoRestart")
				}
				return nil
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
			reg:   []rv{sz(hkcu, `Control Panel\Accessibility\StickyKeys`, "Flags", "506", "510")},
			apply: func(*tctx) error { liveStickyKeys(); return nil }, revert: func(*tctx) error { liveStickyKeys(); return nil }},
		{id: "numlock-on", group: "explorer",
			reg: []rv{
				sz(hkcu, `Control Panel\Keyboard`, "InitialKeyboardIndicators", "2", "0"),
				sz(hku, `.DEFAULT\Control Panel\Keyboard`, "InitialKeyboardIndicators", "2", "2147483648"),
			}},

		// ---------------- System
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
			apply: func(c *tctx) error {
				if b, err := os.ReadFile(wtSettingsPath()); err == nil {
					if cur := wtDefaultProfile(string(b)); cur != "" && cur != ps7Profile {
						c.setExtra("profile", cur)
					}
				}
				if !pwsh7Installed() {
					if _, err := wingetInstall("", "--id", "Microsoft.PowerShell", "-e", "--source", "winget"); err != nil || !pwsh7Installed() {
						return errors.New(T("tw.err.ps7"))
					}
				}
				return setWTDefault(ps7Profile)
			},
			revert: func(c *tctx) error {
				g := c.extra("profile")
				if g == "" {
					g = ps5Profile
				}
				return setWTDefault(g)
			}},
		{id: "bitlocker-off", group: "system", risk: "risky", oneWay: true, sys: true, slow: true,
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
		{id: "net-opt", group: "network", risk: "caution", reboot: true, sys: true, slow: true,
			detect: func(p *probe) string {
				// netsh settings are not always visible to the PowerShell
				// cmdlets, so a change made from here counts as on.
				if getBackup("net-opt") != nil {
					return "on"
				}
				f := p.facts()
				if strings.EqualFold(f.ECN, "Enabled") && strings.EqualFold(f.TS, "Disabled") {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error {
				ns := sys32("netsh.exe")
				var failed []string
				for _, a := range [][]string{
					{"int", "tcp", "set", "heuristics", "disabled"},
					{"int", "tcp", "set", "supplemental", "template=internet", "congestionprovider=ctcp"},
					{"int", "tcp", "set", "global", "rss=enabled"},
					{"int", "tcp", "set", "global", "ecncapability=enabled"},
					{"int", "tcp", "set", "global", "timestamps=disabled"},
					{"int", "tcp", "set", "global", "fastopen=enabled"},
					{"int", "tcp", "set", "global", "fastopenfallback=enabled"},
				} {
					if _, code, _ := runQuiet(time.Minute, ns, a...); code != 0 {
						failed = append(failed, strings.Join(a[3:], " "))
					}
				}
				if len(failed) == 7 {
					return errors.New(T("tw.err.net"))
				}
				return nil
			},
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

		// ---------------- Apps
		{id: "debloat", group: "apps", risk: "caution", oneWay: true, sys: true, slow: true,
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
				return removeApps(pick)
			}},
		{id: "gaming-apps-off", group: "apps", risk: "caution", oneWay: true, sys: true, slow: true,
			detect: func(p *probe) string {
				if allAppsGone(p, gamingApps) {
					return "on"
				}
				return "off"
			},
			apply: func(*tctx) error { return removeApps(gamingApps) }},
		{id: "edge-remove", group: "apps", risk: "caution", sys: true,
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
				closeProcesses(map[string]bool{"msedge.exe": true}, 3*time.Second)
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
		{id: "onedrive-remove", group: "apps", risk: "caution", sys: true,
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
				closeProcesses(map[string]bool{"onedrive.exe": true}, 3*time.Second)
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

func needWin11(p *probe) string {
	if !p.win11() {
		return T("tw.na.win11")
	}
	return ""
}
