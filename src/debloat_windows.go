//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func regRoot(key string) (registry.Key, string, bool) {
	root, path, _ := strings.Cut(key, `\`)
	switch root {
	case "HKCU":
		return registry.CURRENT_USER, path, true
	case "HKLM":
		return registry.LOCAL_MACHINE, path, true
	case "HKCR":
		return registry.CLASSES_ROOT, path, true
	case "HKU":
		return registry.USERS, path, true
	}
	return 0, "", false
}

// liveReg: the live registry, for the state check.
type liveReg struct{}

func (liveReg) keyExists(key string) bool {
	root, path, ok := regRoot(key)
	if !ok {
		return false
	}
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func (liveReg) value(key, name string) (string, uint64, string, []byte, bool) {
	root, path, ok := regRoot(key)
	if !ok {
		return "", 0, "", nil, false
	}
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, "", nil, false
	}
	defer k.Close()
	_, typ, err := k.GetValue(name, nil)
	if err != nil {
		return "", 0, "", nil, false
	}
	switch typ {
	case registry.DWORD, registry.QWORD:
		n, _, _ := k.GetIntegerValue(name)
		return "dword", n, "", nil, true
	case registry.SZ, registry.EXPAND_SZ:
		s, _, _ := k.GetStringValue(name)
		return "string", 0, s, nil, true
	default:
		b, _, _ := k.GetBinaryValue(name)
		return "binary", 0, "", b, true
	}
}

// deleteTree removes a key with everything below it ("" when it is not there).
func deleteTree(root registry.Key, path string) error {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	subs, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, s := range subs {
		if err := deleteTree(root, path+`\`+s); err != nil {
			return err
		}
	}
	if err := registry.DeleteKey(root, path); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// applyOps carries out the operations as "reg import" does; the first error
// is returned after trying them all.
func applyOps(ops []regOp) error {
	var first error
	keep := func(err error, op regOp) {
		if err != nil && first == nil {
			first = errors.New(op.Key + `\` + op.Name + ": " + err.Error())
		}
	}
	for _, op := range ops {
		root, path, ok := regRoot(op.Key)
		if !ok {
			keep(errors.New("unknown root"), op)
			continue
		}
		switch op.Op {
		case "mkkey":
			k, _, err := registry.CreateKey(root, path, registry.QUERY_VALUE)
			if err == nil {
				k.Close()
			}
			keep(err, op)
		case "delkey":
			keep(deleteTree(root, path), op)
		case "delval":
			k, err := registry.OpenKey(root, path, registry.SET_VALUE)
			if err != nil {
				continue // no key: no value
			}
			if err := k.DeleteValue(op.Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				keep(err, op)
			}
			k.Close()
		case "set":
			k, _, err := registry.CreateKey(root, path, registry.SET_VALUE)
			if err != nil {
				keep(err, op)
				continue
			}
			switch op.Type {
			case "dword":
				err = k.SetDWordValue(op.Name, uint32(op.Num))
			case "qword":
				err = k.SetQWordValue(op.Name, op.Num)
			case "string":
				err = k.SetStringValue(op.Name, op.Str)
			case "expand":
				err = k.SetExpandStringValue(op.Name, op.Str)
			default:
				err = k.SetBinaryValue(op.Name, op.Bin)
			}
			k.Close()
			keep(err, op)
		}
	}
	return first
}

// ---- the state

type dbState struct {
	Build     int             `json:"build"`
	Admin     bool            `json:"admin"`
	Applied   map[string]bool `json:"applied"`
	Fits      map[string]bool `json:"fits"`
	Installed []string        `json:"installed"` // apps, by their first id
	Busy      bool            `json:"busy"`
	Error     string          `json:"error,omitempty"`
}

var dbRun struct {
	busy atomic.Bool
	mu   sync.Mutex
}

var widgetApps = []string{"Microsoft.StartExperiencesApp", "MicrosoftWindows.Client.WebExperience", "Microsoft.WidgetsPlatformRuntime"}

// The installed Store apps and the optional features, quickly: the features
// through WMI (InstallState 1 = on), not DISM, which takes a minute.
const dbScanScript = `$ErrorActionPreference = 'SilentlyContinue'
Get-AppxPackage -AllUsers | ForEach-Object { 'P' + [char]9 + $_.Name }
Get-CimInstance Win32_OptionalFeature -Filter "Name='Containers-DisposableClientVM' OR Name='Microsoft-Windows-Subsystem-Linux' OR Name='VirtualMachinePlatform'" | ForEach-Object { 'F' + [char]9 + $_.Name + [char]9 + $(if ($_.InstallState -eq 1) { 'Enabled' } else { 'Disabled' }) }`

func startBinPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), `Packages\Microsoft.Windows.StartMenuExperienceHost_cw5n1h2txyewy\LocalState\start2.bin`)
}

func debloatState() dbState {
	d := debloatData()
	st := dbState{Build: buildNum(osBuild()), Admin: app.admin, Applied: map[string]bool{}, Fits: map[string]bool{}, Installed: []string{}, Busy: dbRun.busy.Load()}
	out, _, err := runPS(3*time.Minute, dbScanScript)
	if err != nil && strings.TrimSpace(out) == "" {
		st.Error = T("db.err.scan")
	}
	var pkgs []string
	optional := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(l), "\t")
		switch {
		case len(f) == 2 && f[0] == "P":
			pkgs = append(pkgs, f[1])
		case len(f) == 3 && f[0] == "F":
			optional[f[1]] = strings.EqualFold(f[2], "Enabled")
		}
	}
	installed := func(id string) bool {
		switch id {
		case "Microsoft.OneDrive":
			return fileExistsAny(filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\OneDrive\OneDrive.exe`)) ||
				fileExistsAny(filepath.Join(os.Getenv("ProgramFiles"), `Microsoft OneDrive\OneDrive.exe`)) ||
				fileExistsAny(filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft OneDrive\OneDrive.exe`))
		case "Microsoft.Edge", "XPFFTQ037JWMHS":
			return fileExistsAny(filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`)) ||
				fileExistsAny(filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`))
		case "XP9CXNGPPJ97XX":
			id = "Microsoft.Copilot"
		}
		for _, p := range pkgs {
			if appMatches(id, p) {
				return true
			}
		}
		return false
	}
	for _, a := range d.Apps {
		for _, id := range a.IDs {
			if installed(id) {
				st.Installed = append(st.Installed, a.IDs[0])
				break
			}
		}
	}
	view := liveReg{}
	for i := range d.Features {
		f := &d.Features[i]
		st.Fits[f.ID] = f.fits(st.Build)
		switch f.Custom {
		case "widgets":
			on := true
			for _, w := range widgetApps {
				if installed(w) {
					on = false
				}
			}
			st.Applied[f.ID] = on
		case "sandbox":
			st.Applied[f.ID] = optional["Containers-DisposableClientVM"]
		case "wsl":
			st.Applied[f.ID] = optional["Microsoft-Windows-Subsystem-Linux"] && optional["VirtualMachinePlatform"]
		case "clearstart":
			b, err := os.ReadFile(startBinPath())
			st.Applied[f.ID] = err == nil && bytes.Equal(b, emptyStartBin)
		default:
			st.Applied[f.ID] = regApplied(f.Apply, view)
		}
	}
	return st
}

// ---- running the changes

type dbRunOpts struct {
	Apply        []string `json:"apply"`
	Undo         []string `json:"undo"`
	Apps         []string `json:"apps"` // first ids
	RestorePoint bool     `json:"restorePoint"`
}

type dbFail struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func dbEmit(step, total int, text string) {
	emit("debloat", map[string]any{"state": "run", "step": step, "total": total, "text": text})
}

var telemetryTasks = []string{
	`\Microsoft\Windows\Application Experience\Microsoft Compatibility Appraiser`,
	`\Microsoft\Windows\Application Experience\Microsoft Compatibility Appraiser Exp`,
	`\Microsoft\Windows\Application Experience\ProgramDataUpdater`,
	`\Microsoft\Windows\Application Experience\StartupAppTask`,
	`\Microsoft\Windows\Customer Experience Improvement Program\Consolidator`,
	`\Microsoft\Windows\Customer Experience Improvement Program\UsbCeip`,
	`\Microsoft\Windows\DiskDiagnostic\Microsoft-Windows-DiskDiagnosticDataCollector`,
	`\Microsoft\Windows\Autochk\Proxy`,
}

func telemetryTasksSet(enable bool) {
	flag := "/Disable"
	if enable {
		flag = "/Enable"
	}
	for _, t := range telemetryTasks {
		_, _, _ = runQuiet(30*time.Second, sys32("schtasks.exe"), "/Change", "/TN", t, flag) // a task this Windows has not: nothing to do
	}
}


// removeAppx removes Store apps (by Win11Debloat's ids) for every user and
// from the image new users get; it returns the ones that stayed.
func removeAppx(ids []string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = psQuote(id)
	}
	// the image's packages are read once (it takes DISM a while)
	script := `$ErrorActionPreference = 'Continue'
$prov = @(Get-AppxProvisionedPackage -Online -ErrorAction SilentlyContinue)
foreach ($n in @(` + strings.Join(q, ",") + `)) {
  $p = '*' + $n + '*'
  try {
    Get-AppxPackage -AllUsers -Name $p | ForEach-Object { Remove-AppxPackage -Package $_.PackageFullName -AllUsers -ErrorAction Stop }
  } catch { }
  # removing it for all users often takes the image's copy too: a "not found" here is not a failure
  $prov | Where-Object { $_.PackageName -like $p } | ForEach-Object { try { Remove-AppxProvisionedPackage -Online -AllUsers -PackageName $_.PackageName -ErrorAction Stop | Out-Null } catch { } }
  $left = @(Get-AppxPackage -AllUsers -Name $p)
  if ($left.Count) {
    $why = 'still installed'
    try { $left | ForEach-Object { Remove-AppxPackage -Package $_.PackageFullName -AllUsers -ErrorAction Stop } } catch { $why = $_.Exception.Message }
    if (@(Get-AppxPackage -AllUsers -Name $p).Count) { 'FAIL' + [char]9 + $n + [char]9 + $why } else { 'OK' + [char]9 + $n }
  } else { 'OK' + [char]9 + $n }
}`
	out, _, err := runPS(15*time.Minute, script)
	failed := map[string]string{}
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(l), "\t", 3)
		if len(f) >= 2 {
			seen[f[1]] = true
			if f[0] == "FAIL" {
				msg := T("db.err.app")
				if len(f) == 3 && strings.TrimSpace(f[2]) != "" {
					msg = strings.TrimSpace(f[2])
				}
				failed[f[1]] = msg
			}
		}
	}
	for _, id := range ids {
		if !seen[id] {
			failed[id] = T("db.err.app")
			if err != nil {
				failed[id] += " (" + err.Error() + ")"
			}
		}
	}
	return failed
}

// removeWinget: the apps Win11Debloat removes through winget (OneDrive,
// Edge, the Copilot app); any of an app's ids may work.
func removeWinget(ids []string) string {
	last := T("db.err.app")
	for _, id := range ids {
		if wingetExe() == "" {
			return T("db.err.winget")
		}
		out, code, err := runWinget(5*time.Minute, "uninstall", "--id", id, "--exact", "--silent", "--accept-source-agreements", "--disable-interactivity")
		if err == nil && code == 0 {
			return ""
		}
		if s := strings.TrimSpace(lastLine(out)); s != "" {
			last = s
		}
	}
	return last
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return ls[len(ls)-1]
}

func killExe(names ...string) {
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	for pid := range procPIDs(want) {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

func dismFeature(name string, on bool) error {
	verb := "/disable-feature"
	if on {
		verb = "/enable-feature"
	}
	args := []string{"/online", verb, "/featurename:" + name, "/norestart"}
	if on {
		args = append(args, "/all")
	}
	_, code, err := runQuiet(20*time.Minute, sys32("dism.exe"), args...)
	if err != nil {
		return err
	}
	if code != 0 && code != 3010 { // 3010: done, a restart finishes it
		return errors.New(T("db.err.dism", code))
	}
	return nil
}

func startBinBackup() string { return dataDirPath("start2-backup.bin") }

func clearStart(undo bool) error {
	p := startBinPath()
	if undo {
		b, err := os.ReadFile(startBinBackup())
		if err != nil {
			return errors.New(T("db.err.noBackup"))
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	} else {
		if b, err := os.ReadFile(p); err == nil && !bytes.Equal(b, emptyStartBin) {
			_ = os.MkdirAll(filepath.Dir(startBinBackup()), 0o755)
			if err := os.WriteFile(startBinBackup(), b, 0o644); err != nil {
				return err
			}
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, emptyStartBin, 0o644); err != nil {
			return err
		}
	}
	killExe("StartMenuExperienceHost.exe") // Windows starts it again with the new file
	return nil
}

// dbChange applies (or with undo, takes back) one change.
func dbChange(f *dbFeature, undo bool, appIndex map[string]dbApp) error {
	switch f.Custom {
	case "widgets":
		if undo {
			return errors.New(T("db.err.oneway"))
		}
		killExe("Widgets.exe", "WidgetService.exe")
		if failed := removeAppx(widgetApps); len(failed) > 0 {
			for _, e := range failed {
				return errors.New(e)
			}
		}
		return nil
	case "sandbox":
		return dismFeature("Containers-DisposableClientVM", !undo)
	case "wsl":
		if undo {
			if err := dismFeature("Microsoft-Windows-Subsystem-Linux", false); err != nil {
				return err
			}
			return dismFeature("VirtualMachinePlatform", false)
		}
		if err := dismFeature("VirtualMachinePlatform", true); err != nil {
			return err
		}
		return dismFeature("Microsoft-Windows-Subsystem-Linux", true)
	case "clearstart":
		return clearStart(undo)
	}
	ops := f.Apply
	if undo {
		ops = f.Undo
		if len(ops) == 0 {
			return errors.New(T("db.err.oneway"))
		}
	}
	err := applyOps(ops)
	if f.Tasks {
		telemetryTasksSet(undo)
	}
	if !undo && len(f.Apps) > 0 {
		var appx []string
		for _, id := range f.Apps {
			if a, ok := appIndex[id]; ok && a.Method == "WinGet" {
				_ = removeWinget(a.IDs) // the app may not be there: the setting still counts
			} else {
				appx = append(appx, id)
			}
		}
		removeAppx(appx)
	}
	return err
}

// debloatRun carries out the chosen changes and app removals in the
// background; the page gets "debloat" events and a last one with the result.
func debloatRun(o dbRunOpts) bool {
	if !app.admin || !dbRun.busy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer dbRun.busy.Store(false)
		defer recoverLog("debloat")
		d := debloatData()
		appIndex := map[string]dbApp{}
		for _, a := range d.Apps {
			for _, id := range a.IDs {
				appIndex[id] = a
			}
		}
		total := len(o.Apply) + len(o.Undo) + len(o.Apps)
		if o.RestorePoint {
			total++
		}
		op := beginOp("tweak", "", T("log.dbRun", len(o.Apply)+len(o.Undo), len(o.Apps)), map[string]any{"apply": o.Apply, "undo": o.Undo, "apps": o.Apps})
		var fails []dbFail
		step, explorer, reboot := 0, false, false
		if o.RestorePoint {
			dbEmit(step, total, T("db.s.rp"))
			if !restorePointTicking(func(int) {}) {
				op.warnf("", T("db.err.rp"), nil)
			}
			step++
		}
		for _, list := range []struct {
			ids  []string
			undo bool
		}{{o.Undo, true}, {o.Apply, false}} {
			for _, id := range list.ids {
				f := dbFeatureByID(id)
				if f == nil {
					continue
				}
				dbEmit(step, total, f.BG[0])
				if err := dbChange(f, list.undo, appIndex); err != nil {
					fails = append(fails, dbFail{ID: id, Error: err.Error()})
					op.fail(id, err.Error(), nil)
				} else {
					op.info(id, T(map[bool]string{true: "log.dbUndone", false: "log.dbDone"}[list.undo]), nil)
					if f.Custom == "" {
						explorer = true
					}
					if f.Reboot {
						reboot = true
					}
				}
				step++
			}
		}
		var appx []string
		for _, id := range o.Apps {
			a, ok := appIndex[id]
			if !ok {
				continue
			}
			if a.Method == "WinGet" {
				dbEmit(step, total, T("db.s.app", a.Name))
				if e := removeWinget(a.IDs); e != "" {
					fails = append(fails, dbFail{ID: id, Error: e})
					op.fail(a.Name, e, nil)
				}
				step++
				continue
			}
			appx = append(appx, id)
		}
		if len(appx) > 0 {
			dbEmit(step, total, T("db.s.apps", len(appx)))
			for id, e := range removeAppx(appx) {
				fails = append(fails, dbFail{ID: id, Error: e})
				op.fail(id, e, nil)
			}
		}
		if explorer {
			dbEmit(total, total, T("db.s.explorer"))
			restartExplorer()
		}
		op.end(T("log.dbSum", total-len(fails), total), nil)
		emit("debloat", map[string]any{"state": "done", "total": total, "failed": fails, "reboot": reboot})
	}()
	return true
}

// debloatInfo: what the page shows (the texts, without the registry operations).
func debloatInfo() map[string]any {
	d := debloatData()
	type fv struct {
		ID     string    `json:"id"`
		Cat    string    `json:"cat"`
		Label  string    `json:"label"`
		Tip    string    `json:"tip"`
		BG     [2]string `json:"bg"`
		Prio   int       `json:"prio"`
		Reboot bool      `json:"reboot,omitempty"`
		Oneway bool      `json:"oneway,omitempty"`
		Undo   bool      `json:"undo"`
	}
	fs := make([]fv, 0, len(d.Features))
	for _, f := range d.Features {
		fs = append(fs, fv{ID: f.ID, Cat: f.Cat, Label: f.Label, Tip: f.Tip, BG: f.BG, Prio: f.Prio, Reboot: f.Reboot, Oneway: f.Oneway,
			Undo: len(f.Undo) > 0 || f.Custom == "sandbox" || f.Custom == "wsl" || f.Custom == "clearstart"})
	}
	return map[string]any{"source": d.Source, "categories": d.Categories, "features": fs, "groups": d.Groups, "apps": d.Apps, "presets": d.Presets, "defaults": d.Defaults}
}
