//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ---------------------------------------------------------------- Windows Update

func deliveryOptimizationSize() int64 {
	d := j(P.windir, `ServiceProfiles\NetworkService\AppData\Local\Microsoft\Windows\DeliveryOptimization\Cache`)
	fi, err := os.Lstat(d)
	if err != nil || !fi.IsDir() {
		return 0
	}
	s, _ := sizeOf(planned{d, fi})
	return s
}

// stopService stops a running service and reports whether it was running.
func stopService(m *mgr.Mgr, name string) bool {
	s, err := m.OpenService(name)
	if err != nil {
		return false
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil || st.State == svc.Stopped {
		return false
	}
	if _, err = s.Control(svc.Stop); err != nil {
		return true
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if st, err = s.Query(); err != nil || st.State == svc.Stopped {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	return true
}

func startService(m *mgr.Mgr, name string) {
	s, err := m.OpenService(name)
	if err != nil {
		return
	}
	defer s.Close()
	_ = s.Start()
}

func cleanWindowsUpdate(c *cleanCtx) (int64, string, error) {
	app.critical.Store(true)
	defer app.critical.Store(false)

	// The download cache is locked while the update service runs, so it is
	// stopped for a moment and started again afterwards - only if it was
	// running before.
	var restart []string
	m, err := mgr.Connect()
	if err == nil {
		for _, name := range []string{"wuauserv", "bits"} {
			if stopService(m, name) {
				restart = append(restart, name)
			}
		}
	}
	st := cleanTargets(findItem("wucache").targets(), nil)
	if m != nil {
		for i := len(restart) - 1; i >= 0; i-- {
			startService(m, restart[i])
		}
		m.Disconnect()
	}
	do, _, _ := measureFree(func() error {
		_, _, e := runPS(3*time.Minute, "Delete-DeliveryOptimizationCache -Force -ErrorAction SilentlyContinue")
		return e
	})
	return st.bytes + do, "", nil
}

func cleanComponentStore(c *cleanCtx) (int64, string, error) {
	app.critical.Store(true)
	defer app.critical.Store(false)
	args := []string{"/Online", "/Cleanup-Image", "/StartComponentCleanup"}
	if c.opts.ResetBase {
		args = append(args, "/ResetBase")
	}
	last := -1
	var code int
	freed, _, err := measureFree(func() error {
		var e error
		code, e = runStream(sys32("dism.exe"), args, func(line string, _ bool) {
			if p := percentIn(line); p >= 0 && int(p) != last {
				last = int(p)
				c.note(fmt.Sprintf("%d%%", last))
			}
		})
		return e
	})
	if code != 0 && code != 3010 {
		if err == nil {
			err = errors.New(T("err.dism", code))
		}
		return freed, "", err
	}
	return freed, "", nil
}

// ---------------------------------------------------------------- restore point

func createRestorePoint() bool {
	const key = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\SystemRestore`
	const val = "SystemRestorePointCreationFrequency"
	// Windows allows one restore point per 24 hours by default. The limit
	// is lifted only for this call, and only if the user has no own setting.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.QUERY_VALUE|registry.SET_VALUE)
	own := true
	if err == nil {
		if _, _, e := k.GetIntegerValue(val); e != nil {
			own = false
			_ = k.SetDWordValue(val, 0)
		}
	}
	script := fmt.Sprintf("try { Enable-ComputerRestore -Drive '%s\\' -ErrorAction Stop; "+
		"Checkpoint-Computer -Description 'xKi4o Cleaner' -RestorePointType 'MODIFY_SETTINGS' -ErrorAction Stop; exit 0 } catch { exit 1 }", systemDrive())
	_, code, _ := runPS(5*time.Minute, script)
	if err == nil {
		if !own {
			_ = k.DeleteValue(val)
		}
		k.Close()
	}
	return code == 0
}

// ---------------------------------------------------------------- drives

type driveRow struct {
	Letter string `json:"letter"`
	Label  string `json:"label"`
	Total  int64  `json:"total"`
	Free   int64  `json:"free"`
	Media  string `json:"media"`
}

const drivesScript = `$r=@(); foreach ($v in (Get-Volume | Where-Object { $_.DriveLetter -and $_.DriveType -eq 'Fixed' } | Sort-Object DriveLetter)) { ` +
	`$m='Unknown'; try { $p=Get-Partition -DriveLetter $v.DriveLetter -ErrorAction Stop; ` +
	`$pd=Get-PhysicalDisk -ErrorAction SilentlyContinue | Where-Object { $_.DeviceId -eq [string]$p.DiskNumber } | Select-Object -First 1; ` +
	`if ($pd) { $m=[string]$pd.MediaType } } catch {}; ` +
	`$r += [pscustomobject]@{ letter=[string]$v.DriveLetter; label=[string]$v.FileSystemLabel; total=[int64]$v.Size; free=[int64]$v.SizeRemaining; media=$m } }; ` +
	`ConvertTo-Json -InputObject @($r) -Compress`

func getDrives() []driveRow {
	out, _, err := runPS(90*time.Second, drivesScript)
	var rows []driveRow
	if err == nil && json.Unmarshal([]byte(out), &rows) == nil && len(rows) > 0 {
		return rows
	}
	// Fallback without media type.
	rows = nil
	mask, _ := windows.GetLogicalDrives()
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(rp) != windows.DRIVE_FIXED {
			continue
		}
		t, f := diskSpace(root)
		rows = append(rows, driveRow{Letter: root[:1], Total: int64(t), Free: int64(f), Media: "Unknown"})
	}
	return rows
}

// ---------------------------------------------------------------- repair

type repairStep struct {
	head string
	exe  string
	args []string
	from float64 // progress range on the bar
	to   float64
}

func repairPlan(kind string) []repairStep {
	dismStep := repairStep{T("repair.dism"), sys32("dism.exe"),
		[]string{"/Online", "/Cleanup-Image", "/RestoreHealth"}, 0, 50}
	sfcStep := repairStep{T("repair.sfc"), sys32("sfc.exe"), []string{"/scannow"}, 50, 100}
	switch kind {
	case "full":
		return []repairStep{dismStep, sfcStep}
	case "sfc":
		sfcStep.from = 0
		return []repairStep{sfcStep}
	case "chkdsk":
		return []repairStep{{T("repair.chkdsk", systemDrive()), sys32("chkdsk.exe"),
			[]string{systemDrive(), "/scan"}, 0, 100}}
	}
	return nil
}

func runRepair(kind string) (bool, string) {
	steps := repairPlan(kind)
	if len(steps) == 0 {
		return false, T("repair.unknown")
	}
	app.critical.Store(true)
	defer app.critical.Store(false)
	var summary []string
	ok := true
	for _, s := range steps {
		emit("repair-head", map[string]string{"text": s.head})
		lastPct := -1.0
		code, err := runStream(s.exe, s.args, func(line string, replace bool) {
			emit("repair-line", map[string]any{"text": line, "replace": replace})
			if p := percentIn(line); p >= 0 {
				v := s.from + (s.to-s.from)*p/100
				if math.Abs(v-lastPct) >= 0.5 {
					lastPct = v
					emit("repair-progress", map[string]float64{"pct": v})
				}
			}
		})
		name := strings.ToUpper(strings.TrimSuffix(strings.ToLower(filepathBase(s.exe)), ".exe"))
		switch {
		case err != nil && code < 0:
			ok = false
			summary = append(summary, T("repair.nostart", name))
		case name == "CHKDSK" && code != 0:
			ok = false
			summary = append(summary, T("repair.chkbad"))
		case name == "CHKDSK":
			summary = append(summary, T("repair.chkok"))
		case name == "DISM" && code != 0 && code != 3010:
			ok = false
			summary = append(summary, T("repair.dismbad", code))
		case name == "DISM":
			summary = append(summary, T("repair.dismok"))
		case name == "SFC":
			summary = append(summary, T("repair.sfcok"))
		}
	}
	return ok, strings.Join(summary, " ")
}

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}
