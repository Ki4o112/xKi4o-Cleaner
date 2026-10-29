//go:build windows

package main

// While game mode is on, Windows runs at its best performance: the
// Ultimate or High performance power plan when the PC has one, else (most
// new laptops, which only have Balanced) Windows' "Best performance" power
// mode. The user's own setting comes back when game mode ends.

import (
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	highPerfPlan    = "8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c"
	ultimateStdPlan = "e9a42b02-d5df-448d-aa00-03f14749eb61"
	bestPerfOverlay = "ded574b5-45a0-4f42-8737-46345c09c238"
)

var (
	powrprof                     = windows.NewLazySystemDLL("powrprof.dll")
	procPowerGetEffectiveOverlay = powrprof.NewProc("PowerGetEffectiveOverlayScheme")
	procPowerSetActiveOverlay    = powrprof.NewProc("PowerSetActiveOverlayScheme")
)

func gmPowerOn() bool {
	g, _ := gmCfgCache.Load().(map[string]any)
	return g["power"] != false
}

func overlayGet() (string, bool) {
	if procPowerGetEffectiveOverlay.Find() != nil {
		return "", false
	}
	var g windows.GUID
	if r, _, _ := procPowerGetEffectiveOverlay.Call(uintptr(unsafe.Pointer(&g))); r != 0 {
		return "", false
	}
	return strings.ToLower(strings.Trim(g.String(), "{}")), true
}

func overlaySet(id string) bool {
	if procPowerSetActiveOverlay.Find() != nil {
		return false
	}
	g, err := windows.GUIDFromString("{" + id + "}")
	if err != nil {
		return false
	}
	r, _, _ := procPowerSetActiveOverlay.Call(uintptr(unsafe.Pointer(&g)))
	return r == 0
}

// gamePowerUp switches to the best performance and says what it changed.
func gamePowerUp(st *gmState) {
	if !gmPowerOn() {
		return
	}
	out, _, _ := runQuiet(20*time.Second, sys32("powercfg.exe"), "/list")
	list := strings.ToLower(out)
	cur := activeScheme()
	for _, p := range []string{ultimateOurs, ultimateStdPlan, highPerfPlan} {
		if strings.Contains(list, p) {
			if p != cur && runTool(sys32("powercfg.exe"), "-setactive", p) == nil {
				st.Plan, st.PrevPlan = p, cur
			}
			return
		}
	}
	if ov, ok := overlayGet(); ok && ov != bestPerfOverlay && overlaySet(bestPerfOverlay) {
		st.Overlay, st.PrevOverlay = bestPerfOverlay, ov
	}
}

// gamePowerBack gives back the user's own plan or power mode, unless they
// changed it themselves meanwhile.
func gamePowerBack(st gmState) {
	if st.Plan != "" && st.PrevPlan != "" && activeScheme() == st.Plan {
		_ = runTool(sys32("powercfg.exe"), "-setactive", st.PrevPlan)
	}
	if st.Overlay != "" && st.PrevOverlay != "" {
		if ov, ok := overlayGet(); ok && ov == st.Overlay {
			overlaySet(st.PrevOverlay)
		}
	}
}

const (
	powerSaverPlan      = "a1841308-3593-4b1d-8d8a-d6b4c5ce1c8c"
	bestEfficiencyOvl   = "961cc777-2547-4f9d-8174-7d86181b8a7a"
	balancedOverlayNone = "00000000-0000-0000-0000-000000000000"
)

type powerModeResult struct {
	OK    bool   `json:"ok"`
	Plan  string `json:"plan,omitempty"`  // the plan now
	Mode  string `json:"mode,omitempty"`  // the power mode now (laptops)
	Error string `json:"error,omitempty"` // what could not be set
}

// powerProfile puts Windows' power setting for the app's modes: games (the
// best performance there is), balanced (Windows' default) or saver (the
// least power).
func powerProfile(name string) powerModeResult {
	op := beginOp("optimize", name, T("log.profile", T("pf."+name)), nil)
	out, _, _ := runQuiet(20*time.Second, sys32("powercfg.exe"), "/list")
	list := strings.ToLower(out)
	has := func(g string) bool { return strings.Contains(list, g) }
	var plans []string
	ovl := ""
	switch name {
	case "games":
		plans, ovl = []string{ultimateOurs, ultimateStdPlan, highPerfPlan}, bestPerfOverlay
	case "balanced":
		plans, ovl = []string{balancedPlan}, balancedOverlayNone
	case "saver":
		plans, ovl = []string{powerSaverPlan}, bestEfficiencyOvl
	default:
		op.end("", nil)
		return powerModeResult{Error: T("pf.err")}
	}
	r := powerModeResult{OK: true}
	set := false
	for _, p := range plans {
		if has(p) {
			if err := runTool(sys32("powercfg.exe"), "-setactive", p); err != nil {
				r.OK, r.Error = false, err.Error()
			}
			set = true
			break
		}
	}
	if !set && name != "balanced" { // only Balanced on this PC: the power mode does it
		if err := runTool(sys32("powercfg.exe"), "-setactive", balancedPlan); err == nil {
			set = true
		}
	}
	if _, ok := overlayGet(); ok {
		// the power mode goes with the plan only on Balanced (Windows ignores it otherwise)
		if activeScheme() == balancedPlan && !overlaySet(ovl) && name != "balanced" {
			r.OK, r.Error = false, T("pf.err")
		}
	} else if !set {
		r.OK, r.Error = false, T("pf.err")
	}
	r.Plan = activeScheme()
	r.Mode, _ = overlayGet()
	if !r.OK {
		op.fail("", r.Error, nil)
	}
	op.end("", map[string]any{"plan": r.Plan, "mode": r.Mode})
	return r
}
