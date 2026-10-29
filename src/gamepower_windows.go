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
