//go:build windows

package main

import (
	"time"

	"golang.org/x/sys/windows/registry"
)

const (
	wuUXKey     = `SOFTWARE\Microsoft\WindowsUpdate\UX\Settings`
	wuPolicyKey = `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
)

type wuCtl struct {
	PausedUntil  int64  `json:"pausedUntil,omitempty"` // unix ms
	SecurityOnly bool   `json:"securityOnly"`
	Home         bool   `json:"home"` // Windows Home: the deferral policy is not followed there
	Error        string `json:"error,omitempty"`
}

func wuCtlState() wuCtl {
	var st wuCtl
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, wuUXKey, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("PauseUpdatesExpiryTime"); err == nil {
			if t := wuPausedUntil(v, time.Now()); !t.IsZero() {
				st.PausedUntil = t.UnixMilli()
			}
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, wuPolicyKey, registry.QUERY_VALUE); err == nil {
		v, _, _ := k.GetIntegerValue("DeferFeatureUpdates")
		st.SecurityOnly = v == 1
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		ed, _, _ := k.GetStringValue("EditionID")
		st.Home = ed == "Core" || ed == "CoreSingleLanguage" || ed == "CoreCountrySpecific"
		k.Close()
	}
	return st
}

// wuPause pauses updates for weeks (1-5); 0 resumes them.
func wuPause(weeks int) wuCtl {
	if !app.admin {
		return wuCtl{Error: T("blocked.admin")}
	}
	op := beginOp("update", "", T("log.wuPause", weeks), nil)
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, wuUXKey, registry.SET_VALUE)
	if err == nil {
		for name, v := range wuPauseValues(time.Now(), max(1, weeks)) {
			if weeks == 0 {
				_ = k.DeleteValue(name)
			} else if err = k.SetStringValue(name, v); err != nil {
				break
			}
		}
		k.Close()
	}
	if err != nil {
		op.fail("", err.Error(), nil)
	}
	op.end("", nil)
	st := wuCtlState()
	if err != nil {
		st.Error = err.Error()
	}
	return st
}

// wuSecurityOnly defers feature updates by 365 days (on), or removes that (off).
func wuSecurityOnly(on bool) wuCtl {
	if !app.admin {
		return wuCtl{Error: T("blocked.admin")}
	}
	op := beginOp("update", "", T("log.wuSecOnly", on), nil)
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, wuPolicyKey, registry.SET_VALUE)
	if err == nil {
		if on {
			if err = k.SetDWordValue("DeferFeatureUpdates", 1); err == nil {
				err = k.SetDWordValue("DeferFeatureUpdatesPeriodInDays", 365)
			}
		} else {
			_ = k.DeleteValue("DeferFeatureUpdates")
			_ = k.DeleteValue("DeferFeatureUpdatesPeriodInDays")
		}
		k.Close()
	}
	if err != nil {
		op.fail("", err.Error(), nil)
	}
	op.end("", nil)
	st := wuCtlState()
	if err != nil {
		st.Error = err.Error()
	}
	return st
}
