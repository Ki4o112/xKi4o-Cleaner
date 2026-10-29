//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const batteryScript = `$ErrorActionPreference = 'SilentlyContinue'
$b = Get-CimInstance Win32_Battery | Select-Object -First 1
if (-not $b) { '{"ok":false}'; exit }
$st = Get-CimInstance -Namespace root\wmi BatteryStatus | Select-Object -First 1
$o = @{ ok = $true; charge = [int]$b.EstimatedChargeRemaining
  cycles = [int](Get-CimInstance -Namespace root\wmi BatteryCycleCount | Select-Object -First 1).CycleCount
  full = [int64](Get-CimInstance -Namespace root\wmi BatteryFullChargedCapacity | Select-Object -First 1).FullChargedCapacity
  design = [int64](Get-CimInstance -Namespace root\wmi BatteryStaticData | Select-Object -First 1).DesignedCapacity }
if ($st) { $o.remaining = [int64]$st.RemainingCapacity; $o.onAC = [bool]$st.PowerOnline; $o.charging = [bool]$st.Charging; $o.draw = [int64]$st.DischargeRate; $o.gets = [int64]$st.ChargeRate }
$o | ConvertTo-Json -Compress`

func batteryInfo() batDetail {
	out, _, err := runPS(30*time.Second, batteryScript)
	var b batDetail
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &b); jerr != nil {
		b = batDetail{OK: hasBattery()}
		if b.OK {
			b.Error = T("bat.err")
			if err != nil {
				b.Error += " (" + err.Error() + ")"
			}
		}
		return b
	}
	if b.OnAC {
		b.Draw = 0
	}
	b.finish()
	return b
}

// batteryReport: Windows' own detailed battery report (powercfg), opened
// in the browser.
func batteryReport() string {
	dst := filepath.Join(os.TempDir(), "xKi4o-battery-report.html")
	_ = os.Remove(dst)
	if _, code, err := runQuiet(60*time.Second, sys32("powercfg.exe"), "/batteryreport", "/output", dst); err != nil || code != 0 {
		return T("bat.repErr")
	}
	if !fileExistsAny(dst) {
		return T("bat.repErr")
	}
	shellOpen(dst)
	return ""
}
