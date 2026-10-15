//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

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
	st := cleanWith(findItem("wucache").targets(), c.cl)
	c.files = st
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

var (
	srclient               = windows.NewLazySystemDLL("srclient.dll")
	procSRSetRestorePointW = srclient.NewProc("SRSetRestorePointW")
)

// RESTOREPOINTINFOW (packed, but naturally aligned): 4 + 4 + 8 + 512 bytes.
type restorePointInfo struct {
	eventType uint32
	rpType    uint32
	seq       int64
	desc      [256]uint16
}

// srRestorePoint asks System Restore directly (much faster than starting
// PowerShell). It reports the Win32 status of the call.
func srRestorePoint() (ok bool, status uint32) {
	if procSRSetRestorePointW.Find() != nil {
		return false, 1
	}
	withCOM(func() {
		info := restorePointInfo{eventType: 100, rpType: 12} // BEGIN_SYSTEM_CHANGE, MODIFY_SETTINGS
		copy(info.desc[:255], windows.StringToUTF16("xKi4o Cleaner"))
		var st [12]byte // STATEMGRSTATUS (packed): nStatus, llSequenceNumber
		r, _, _ := procSRSetRestorePointW.Call(uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&st[0])))
		status = binary.LittleEndian.Uint32(st[:4])
		if r == 0 || status != 0 {
			return
		}
		end := restorePointInfo{eventType: 101, rpType: 12, seq: int64(binary.LittleEndian.Uint64(st[4:]))} // END_SYSTEM_CHANGE
		r, _, _ = procSRSetRestorePointW.Call(uintptr(unsafe.Pointer(&end)), uintptr(unsafe.Pointer(&st[0])))
		status = binary.LittleEndian.Uint32(st[:4])
		ok = r != 0 && status == 0
	})
	return ok, status
}

// rpMu keeps restore points one at a time (a cleaning and a tweak may ask
// at the same moment; System Restore and the frequency setting below are
// shared).
var rpMu sync.Mutex

func createRestorePoint() bool {
	rpMu.Lock()
	defer rpMu.Unlock()
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
	defer func() {
		if err == nil {
			if !own {
				_ = k.DeleteValue(val)
			}
			k.Close()
		}
	}()
	if ok, _ := srRestorePoint(); ok {
		return true
	}
	// System Restore may be off for the drive: PowerShell turns it on and
	// makes the point.
	script := fmt.Sprintf("try { Enable-ComputerRestore -Drive '%s\\' -ErrorAction Stop; "+
		"Checkpoint-Computer -Description 'xKi4o Cleaner' -RestorePointType 'MODIFY_SETTINGS' -ErrorAction Stop; exit 0 } catch { exit 1 }", systemDrive())
	_, code, _ := runPS(4*time.Minute, script)
	return code == 0
}

// restorePointTicking makes a restore point and calls tick every second
// meanwhile (so the page can show that it is still working).
func restorePointTicking(tick func(sec int)) bool {
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- false
			}
		}()
		done <- createRestorePoint()
	}()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for sec := 1; ; sec++ {
		select {
		case ok := <-done:
			return ok
		case <-t.C:
			tick(sec)
		}
	}
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
