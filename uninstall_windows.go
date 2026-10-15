//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------------------------------------------------------------- the registry, as the scan sees it

type winReg struct{}

// regLoc turns HKCU\..., HKLM\... (64-bit view) and HKLM32\... (32-bit
// view) into a root and a path. The 32-bit view is reached by its real
// place (SOFTWARE\WOW6432Node), so the same path works for reg.exe too.
func regLoc(p string) (registry.Key, string, string, bool) {
	c := regCanon(p)
	root, rest, _ := strings.Cut(c, `\`)
	switch root {
	case "HKCU":
		return registry.CURRENT_USER, rest, c, true
	case "HKLM":
		return registry.LOCAL_MACHINE, rest, c, true
	}
	return 0, "", "", false
}

func regOpenAt(p string, access uint32) (registry.Key, error) {
	root, sub, _, ok := regLoc(p)
	if !ok {
		return 0, errors.New("bad key")
	}
	return registry.OpenKey(root, sub, access|registry.WOW64_64KEY)
}

func (winReg) Subkeys(p string) []string {
	k, err := regOpenAt(p, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	n, _ := k.ReadSubKeyNames(-1)
	return n
}

func (winReg) Exists(p string) bool {
	k, err := regOpenAt(p, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func (winReg) Values(p string) []string {
	k, err := regOpenAt(p, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	n, _ := k.ReadValueNames(-1)
	return n
}

func (winReg) String(p, name string) string {
	k, err := regOpenAt(p, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(name)
	return expandEnv(v)
}

var procRegDeleteTreeW = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegDeleteTreeW")

// regDeleteKey removes a key with everything under it.
func regDeleteKey(p string) error {
	root, sub, _, ok := regLoc(p)
	if !ok || !regKeyDeletable(p) || !strings.Contains(sub, `\`) {
		return errors.New(T("un.err.keyProtected"))
	}
	i := strings.LastIndex(sub, `\`)
	parent, name := sub[:i], sub[i+1:]
	pk, err := registry.OpenKey(root, parent, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|registry.SET_VALUE|windows.DELETE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer pk.Close()
	n, _ := windows.UTF16PtrFromString(name)
	if r, _, _ := procRegDeleteTreeW.Call(uintptr(pk), uintptr(unsafe.Pointer(n))); r != 0 && syscall.Errno(r) != windows.ERROR_FILE_NOT_FOUND {
		return syscall.Errno(r)
	}
	if err := registry.DeleteKey(pk, name); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return err
	}
	return nil
}

func regDeleteValue(p, name string) error {
	if c := strings.ToLower(regCanon(p)); !strings.HasPrefix(c, `hkcu\software\`) && !strings.HasPrefix(c, `hklm\software\`) {
		return errors.New(T("un.err.keyProtected"))
	}
	k, err := regOpenAt(p, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------- registry backups (.reg files)

func regBackupDir() string { return dataDirPath(`backups`) }

// regExportAll saves the keys in one .reg file that "reg import" (or a
// double click) puts back.
func regExportAll(keys []string, file string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	var body strings.Builder
	n := 0
	for _, k := range keys {
		_, _, full, ok := regLoc(k)
		if !ok {
			continue
		}
		tmp := file + ".part"
		if _, _, err := runQuiet(30*time.Second, sys32("reg.exe"), "export", full, tmp, "/y", "/reg:64"); err != nil {
			_ = os.Remove(tmp)
			continue
		}
		b, err := os.ReadFile(tmp)
		_ = os.Remove(tmp)
		if err != nil {
			continue
		}
		text := decodeRegFile(b)
		if i := strings.Index(text, "\n"); i >= 0 && strings.HasPrefix(strings.TrimSpace(text[:i]), "Windows Registry Editor") {
			text = text[i+1:]
		}
		body.WriteString(strings.TrimLeft(text, "\r\n"))
		body.WriteString("\r\n")
		n++
	}
	if n == 0 {
		return errors.New("nothing exported")
	}
	out := "Windows Registry Editor Version 5.00\r\n\r\n" + body.String()
	u := utf16.Encode([]rune(out))
	buf := bytes.NewBuffer([]byte{0xFF, 0xFE})
	for _, c := range u {
		buf.WriteByte(byte(c))
		buf.WriteByte(byte(c >> 8))
	}
	return os.WriteFile(file, buf.Bytes(), 0o644)
}

func decodeRegFile(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return decodeUTF16(append(b[2:], 0, 0))
	}
	return string(b)
}

type regBackup struct {
	File string `json:"file"`
	Name string `json:"name"` // the program
	At   int64  `json:"at"`   // unix ms
	Keys int    `json:"keys"`
	Size int64  `json:"size"`
}

// regBackups lists the backups made before deleting leftovers, newest first.
func regBackups() []regBackup {
	var out []regBackup
	ents, _ := os.ReadDir(regBackupDir())
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".reg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if p := strings.SplitN(name, " ", 3); len(p) == 3 && len(p[0]) == 10 && len(p[1]) == 6 { // "2006-01-02 150405 Name"
			name = p[2]
		}
		b, _ := os.ReadFile(filepath.Join(regBackupDir(), e.Name()))
		keys := strings.Count(decodeRegFile(b), "\n[")
		out = append(out, regBackup{File: e.Name(), Name: name, At: fi.ModTime().UnixMilli(), Keys: keys, Size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// regRestore puts a backup back (only files in the app's backup folder).
func regRestore(file string) string {
	if file == "" || filepath.Base(file) != file || !strings.EqualFold(filepath.Ext(file), ".reg") {
		return T("un.err.backup")
	}
	p := filepath.Join(regBackupDir(), file)
	op := beginOp("uninstall", "", T("log.unRegRestore"), map[string]any{"file": file})
	out, code, err := runQuiet(time.Minute, sys32("reg.exe"), "import", p, "/reg:64")
	if err != nil || code != 0 {
		msg := strings.TrimSpace(out)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		op.fail("", msg, nil)
		op.end("", nil)
		return T("un.err.import", msg)
	}
	op.end(T("log.unRegRestored"), nil)
	return ""
}

// ---------------------------------------------------------------- last used

var usageCache struct {
	mu sync.Mutex
	u  usage
	at time.Time
}

var userAssistFolders = map[string]func() string{
	"{6D809377-6AF0-444B-8957-A3773F02200E}": func() string { return os.Getenv("ProgramW6432") },
	"{7C5A40EF-A0FB-4BFC-874A-C0F2E0B9FA8E}": func() string { return os.Getenv("ProgramFiles(x86)") },
	"{F38BF404-1D43-42F2-9305-67DE0B28FC23}": windowsDir,
	"{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}": func() string { return filepath.Join(windowsDir(), "System32") },
	"{D65231B0-B2F1-4857-A4CE-A8E7C6EA7D27}": func() string { return filepath.Join(windowsDir(), "SysWOW64") },
}

// loadUsage reads when programs were last run: Windows' Prefetch files
// (one per program, touched at each start) and UserAssist (programs started
// from the Start menu, the taskbar or Explorer).
func loadUsage() usage {
	usageCache.mu.Lock()
	defer usageCache.mu.Unlock()
	if time.Since(usageCache.at) < time.Minute && usageCache.u.byName != nil {
		return usageCache.u
	}
	u := usage{byName: map[string]int64{}, byPath: map[string]int64{}}
	if ents, err := os.ReadDir(filepath.Join(windowsDir(), "Prefetch")); err == nil {
		for _, e := range ents {
			n := prefetchName(e.Name())
			if n == "" {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().Unix() > u.byName[n] {
				u.byName[n] = fi.ModTime().Unix()
			}
		}
	}
	const ua = `Software\Microsoft\Windows\CurrentVersion\Explorer\UserAssist`
	if k, err := registry.OpenKey(registry.CURRENT_USER, ua, registry.ENUMERATE_SUB_KEYS); err == nil {
		guids, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, g := range guids {
			ck, err := registry.OpenKey(registry.CURRENT_USER, ua+`\`+g+`\Count`, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			names, _ := ck.ReadValueNames(-1)
			for _, nm := range names {
				b, _, err := ck.GetBinaryValue(nm)
				if err != nil {
					continue
				}
				t := userAssistTime(b)
				if t <= 0 {
					continue
				}
				p := rot13(nm)
				if strings.HasPrefix(p, "{") {
					if i := strings.Index(p, "}"); i > 0 {
						if f, ok := userAssistFolders[strings.ToUpper(p[:i+1])]; ok {
							p = f() + p[i+1:]
						}
					}
				}
				key := strings.ToLower(filepath.Clean(p))
				if t > u.byPath[key] {
					u.byPath[key] = t
				}
			}
			ck.Close()
		}
	}
	usageCache.u, usageCache.at = u, time.Now()
	return u
}

// ---------------------------------------------------------------- the list

type appsView struct {
	Apps  []instApp `json:"apps"`
	Admin bool      `json:"admin"`
}

var appsCache struct {
	mu    sync.Mutex
	apps  []instApp // win32
	store []instApp
}

type uninstRoot struct {
	root  registry.Key
	name  string // HKLM, HKLM32, HKCU
	path  string
	scope string
	arch  string
}

var uninstRoots = []uninstRoot{
	{registry.LOCAL_MACHINE, "HKLM", `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, "machine", "64"},
	{registry.LOCAL_MACHINE, "HKLM32", `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, "machine", "32"},
	{registry.CURRENT_USER, "HKCU", `Software\Microsoft\Windows\CurrentVersion\Uninstall`, "user", ""},
}

// appsList reads the installed programs from the uninstall entries of
// Windows (both 64- and 32-bit, for all users and for this user).
func appsList() appsView {
	use := loadUsage()
	var apps []instApp
	seen := map[string]bool{}
	for _, r := range uninstRoots {
		base, err := regOpenAt(r.name+`\`+r.path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		subs, _ := base.ReadSubKeyNames(-1)
		base.Close()
		for _, sub := range subs {
			a, ok := readUninstallEntry(r, sub, use)
			if !ok {
				continue
			}
			k := normName(cleanAppName(a.Name)) + "|" + a.Version
			if seen[k] {
				continue
			}
			seen[k] = true
			apps = append(apps, a)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	appsCache.mu.Lock()
	appsCache.apps = apps
	appsCache.mu.Unlock()
	return appsView{Apps: apps, Admin: app.admin}
}

func readUninstallEntry(r uninstRoot, sub string, use usage) (instApp, bool) {
	keyPath := r.name + `\` + r.path + `\` + sub
	k, err := regOpenAt(keyPath, registry.QUERY_VALUE)
	if err != nil {
		return instApp{}, false
	}
	defer k.Close()
	str := func(n string) string { v, _, _ := k.GetStringValue(n); return strings.TrimSpace(v) }
	num := func(n string) uint64 { v, _, _ := k.GetIntegerValue(n); return v }
	a := instApp{Name: str("DisplayName"), Kind: "win32", Scope: r.scope, Arch: r.arch, Key: keyPath}
	if a.Name == "" || num("SystemComponent") == 1 || str("ParentKeyName") != "" {
		return a, false
	}
	switch strings.ToLower(str("ReleaseType")) {
	case "update", "hotfix", "security update", "service pack":
		return a, false
	}
	a.ID = "reg:" + r.name + ":" + sub
	a.Publisher, a.Version = str("Publisher"), str("DisplayVersion")
	a.Location = strings.Trim(expandEnv(str("InstallLocation")), `"`)
	a.Command = expandEnv(str("UninstallString"))
	a.Quiet = expandEnv(str("QuietUninstallString"))
	a.NoRemove = num("NoRemove") == 1
	if num("WindowsInstaller") == 1 || strings.Contains(strings.ToLower(a.Command), "msiexec") {
		if g := reGUIDBraced.FindString(sub); g != "" {
			a.MSI = g
		} else if g := reGUIDBraced.FindString(a.Command); g != "" {
			a.MSI = g
		}
	}
	if sz := num("EstimatedSize"); sz > 0 {
		a.Size, a.SizeFrom = int64(sz)*1024, "reg"
	}
	a.Installed = installDate(str("InstallDate"))
	if a.Installed == 0 {
		if st, err := k.Stat(); err == nil {
			a.Installed = st.ModTime().Unix()
		}
	}
	icon := cleanRegPath(expandEnv(str("DisplayIcon")))
	if icon != "" && fileExists(icon) {
		a.Icon = icon
	}
	if a.Location != "" && !dirExists(a.Location) {
		a.Location = ""
	}
	// no install location: the folder of its uninstaller (not Windows' own folders)
	if a.Location == "" && a.MSI == "" {
		if exe, _ := commandParts(a.Command); exe != "" && filepath.IsAbs(exe) {
			d := filepath.Dir(exe)
			if !underWindowsDir(d) && !strings.EqualFold(d, os.Getenv("ProgramFiles")) && !strings.EqualFold(d, os.Getenv("ProgramFiles(x86)")) && dirExists(d) {
				a.Location = d
			}
		}
	}
	a.Exes = programExes(a.Location, 80)
	if icon != "" && strings.EqualFold(filepath.Ext(icon), ".exe") {
		a.Exes = append(a.Exes, icon)
	}
	if a.Icon == "" {
		for _, e := range a.Exes {
			if !genericExe(filepath.Base(e)) {
				a.Icon = e
				break
			}
		}
	}
	a.LastUsed = use.lastUsed(a.Exes)
	a.Broken = !uninstallerPresent(a)
	return a, true
}

// uninstallerPresent: the program's own uninstaller is there.
func uninstallerPresent(a instApp) bool {
	if a.MSI != "" {
		return true
	}
	exe, _ := commandParts(a.Command)
	if exe == "" {
		return false
	}
	if !filepath.IsAbs(exe) {
		return resolveExe(exe) != ""
	}
	return fileExists(exe)
}

// appsMeasure measures the folders of programs whose size the setup did not
// write (at most ~8 s for all of them).
func appsMeasure(ids []string) map[string]int64 {
	out := map[string]int64{}
	appsCache.mu.Lock()
	all := append(append([]instApp(nil), appsCache.apps...), appsCache.store...)
	appsCache.mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	deadline := time.Now().Add(8 * time.Second)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, a := range all {
		if !want[a.ID] || a.Location == "" {
			continue
		}
		wg.Add(1)
		go func(a instApp) {
			defer wg.Done()
			defer recoverLog("app size")
			sem <- struct{}{}
			defer func() { <-sem }()
			if time.Now().After(deadline) {
				return
			}
			size, _ := osLeftFS{}.Size(a.Location)
			mu.Lock()
			out[a.ID] = size
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	return out
}

// ---------------------------------------------------------------- Store apps

const storeAppsScript = `$start=@{}; try { foreach($a in Get-StartApps){ $i=$a.AppID.IndexOf('!'); if($i -gt 0){ $f=$a.AppID.Substring(0,$i); if(-not $start.ContainsKey($f)){ $start[$f]=$a.Name } } } } catch {}
$out=@(); foreach($p in Get-AppxPackage){ if($p.IsFramework -or $p.NonRemovable -or $p.IsResourcePackage -or [string]$p.SignatureKind -eq 'System'){ continue }
 $d=$start[$p.PackageFamilyName]; if(-not $d){ continue }
 $date=$null; try { $date=(Get-Item -LiteralPath $p.InstallLocation -ErrorAction Stop).CreationTimeUtc.ToString('o') } catch {}
 $out += [pscustomobject]@{ full=[string]$p.PackageFullName; fam=[string]$p.PackageFamilyName; disp=[string]$d; ver=[string]$p.Version; pub=[string]$p.Publisher; loc=[string]$p.InstallLocation; date=$date } }
ConvertTo-Json -InputObject @($out) -Compress -Depth 2`

type psStoreApp struct {
	Full string `json:"full"`
	Fam  string `json:"fam"`
	Disp string `json:"disp"`
	Ver  string `json:"ver"`
	Pub  string `json:"pub"`
	Loc  string `json:"loc"`
	Date string `json:"date"`
}

func storeApps() appsView {
	out, _, err := runPS(2*time.Minute, storeAppsScript)
	var list []psStoreApp
	if err == nil {
		_ = json.Unmarshal([]byte(out), &list)
	}
	use := loadUsage()
	var apps []instApp
	for _, p := range list {
		a := instApp{ID: "appx:" + p.Full, Kind: "store", Name: p.Disp, Version: p.Ver, Publisher: certName(p.Pub), Location: p.Loc, Family: p.Fam, Scope: "user"}
		if t, err := time.Parse(time.RFC3339Nano, p.Date); err == nil {
			a.Installed = t.Unix()
		}
		a.Exes = programExes(p.Loc, 20)
		for _, e := range a.Exes {
			if !genericExe(filepath.Base(e)) {
				a.Icon = e
				break
			}
		}
		a.LastUsed = use.lastUsed(a.Exes)
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	appsCache.mu.Lock()
	appsCache.store = apps
	appsCache.mu.Unlock()
	return appsView{Apps: apps, Admin: app.admin}
}

// certName: "CN=Microsoft Corporation, O=..., C=US" -> Microsoft Corporation.
func certName(s string) string {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToUpper(part), "CN=") {
			return strings.Trim(part[3:], `"`)
		}
	}
	return s
}

func findApp(id string) (instApp, []instApp, bool) {
	appsCache.mu.Lock()
	defer appsCache.mu.Unlock()
	all := append(append([]instApp(nil), appsCache.apps...), appsCache.store...)
	for _, a := range all {
		if a.ID == id {
			return a, all, true
		}
	}
	return instApp{}, all, false
}

// ---------------------------------------------------------------- uninstalling (one at a time)

type uninstOpts struct {
	RestorePoint bool `json:"restorePoint"`
	Level        int  `json:"level"`  // 1 safe, 2 moderate, 3 advanced
	Forced       bool `json:"forced"` // the uninstaller is missing or broken: only look for leftovers
}

type uninstJob struct {
	mu      sync.Mutex
	running bool
	app     instApp
	ident   appIdent
	found   []leftover
	scope   lfScope
	op      *opHandle
	cont    atomic.Bool // "the uninstaller has finished": stop waiting
	phase   string
}

var uj = &uninstJob{}

func emitUninst(phase string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["phase"] = phase
	uj.mu.Lock()
	uj.phase = phase
	data["id"] = uj.app.ID
	uj.mu.Unlock()
	emit("uninst", data)
}

// uninstStart runs a program's own uninstaller, waits for it, then looks
// for what it left. The page gets "uninst" events at each step.
func uninstStart(id string, o uninstOpts) bool {
	a, all, ok := findApp(id)
	if !ok || a.NoRemove && !o.Forced {
		return false
	}
	uj.mu.Lock()
	if uj.running {
		uj.mu.Unlock()
		return false
	}
	uj.running, uj.app, uj.found = true, a, nil
	uj.mu.Unlock()
	uj.cont.Store(false)
	if o.Level < 1 || o.Level > 3 {
		o.Level = 2
	}
	// what the program is recognised by, read now: its files go next
	ident := appIdent{Name: a.Name, Publisher: a.Publisher, Location: a.Location, Exes: append([]string(nil), a.Exes...), Key: a.Key, Family: a.Family}
	if a.Location != "" {
		ident.Exes = append(ident.Exes, programExes(a.Location, 200)...)
	}
	op := beginOp("uninstall", a.Name, T(map[bool]string{true: "log.unForced", false: "log.unStart"}[o.Forced], a.Name),
		map[string]any{"version": a.Version, "publisher": a.Publisher, "location": a.Location, "command": a.Command, "level": o.Level, "restorePoint": o.RestorePoint})
	uj.mu.Lock()
	uj.ident, uj.op = ident, op
	uj.mu.Unlock()
	app.working.Store(true)
	go func() {
		defer recoverLog("uninstall")
		defer app.working.Store(false)
		if o.RestorePoint && app.admin {
			emitUninst("rp", nil)
			app.critical.Store(true)
			okRP := restorePointTicking(func(sec int) { emitUninst("rp", map[string]any{"secs": sec}) })
			app.critical.Store(false)
			if okRP {
				op.info("", T("log.rpDone"), nil)
			} else {
				op.warnf("", T("log.rpFail"), nil)
			}
		}
		if !o.Forced {
			emitUninst("run", nil)
			code, err := runUninstaller(a)
			if err != nil {
				op.fail("", T("un.err.run", err.Error()), nil)
				op.end("", nil)
				emitUninst("error", map[string]any{"error": T("un.err.run", err.Error())})
				uninstFinish()
				return
			}
			op.info("", T("log.unRan", code), map[string]any{"exit": code})
			// some uninstallers hand the work to a process they do not wait for:
			// give the entry a little time to go
			for i := 0; a.Kind != "store" && i < 20 && !uj.cont.Load() && stillInstalled(a); i++ {
				emitUninst("run", map[string]any{"secs": 0})
				time.Sleep(time.Second)
			}
			if stillInstalled(a) {
				op.warnf("", T("un.still"), nil)
				op.end("", nil)
				emitUninst("still", nil)
				uninstFinish()
				return
			}
		}
		emitUninst("scan", nil)
		sc := leftoverScope(a, all, o.Level)
		if !o.Forced {
			sc.Uninstall = "" // the setup removed its entry; if not, the scan below finds it
			if a.Kind == "win32" && (winReg{}).Exists(a.Key) {
				sc.Uninstall = a.Key
			}
		} else if a.Kind == "win32" {
			sc.Uninstall = a.Key
		}
		found := scanLeftovers(ident, sc, osLeftFS{}, winReg{})
		uj.mu.Lock()
		uj.found, uj.scope = found, sc
		uj.mu.Unlock()
		op.info("", T("log.unFound", len(found)), nil)
		emitUninst("found", map[string]any{"items": found})
	}()
	return true
}

// uninstContinue: the user says the uninstaller has finished.
func uninstContinue() bool { uj.cont.Store(true); return true }

func uninstFinish() bool {
	uj.mu.Lock()
	defer uj.mu.Unlock()
	if uj.op != nil && uj.phase == "found" {
		uj.op.end(T("log.unKept"), nil)
	}
	uj.running, uj.found, uj.op = false, nil, nil
	return true
}

// runUninstaller starts the program's own uninstaller and waits until it
// and every program it started (setups often copy themselves to Temp and
// start the copy) have closed, or until the user says it has finished.
func runUninstaller(a instApp) (int, error) {
	var cmd *exec.Cmd
	switch {
	case a.Kind == "store":
		out, code, err := runPS(5*time.Minute, "Remove-AppxPackage -Package "+psQuote(strings.TrimPrefix(a.ID, "appx:"))+" -ErrorAction Stop; 'OK'")
		if err != nil || !strings.Contains(out, "OK") {
			if strings.TrimSpace(out) == "" && err != nil {
				return code, err
			}
			return code, errors.New(strings.TrimSpace(out))
		}
		return 0, nil
	case a.MSI != "":
		cmd = exec.CommandContext(context.Background(), sys32("msiexec.exe"), msiUninstallArgs(a.MSI, false)...) //nolint:gosec // Windows Installer, with the product code from the registry
	default:
		exe, args := commandParts(a.Command)
		if exe == "" {
			return -1, errors.New(T("un.err.noCmd"))
		}
		if !filepath.IsAbs(exe) {
			if r := resolveExe(exe); r != "" {
				exe = r
			}
		}
		cmd = exec.CommandContext(context.Background(), exe) //nolint:gosec // the program's own uninstaller, as Windows would start it
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + exe + `" ` + args}
		cmd.Dir = filepath.Dir(exe)
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	root := uint32(cmd.Process.Pid)
	var rootCreated int64
	if ft, ok := processStart(root); ok {
		rootCreated = int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	}
	exited := make(chan int, 1)
	go func() {
		_ = cmd.Wait()
		exited <- cmd.ProcessState.ExitCode()
	}()
	tracked := map[uint32]int64{root: rootCreated}
	code, quiet := 0, 0
	start := time.Now()
	for {
		select {
		case c := <-exited:
			code = c
		default:
		}
		if uj.cont.Load() {
			return code, nil
		}
		procs, err := queryProcesses()
		if err != nil {
			return code, nil //nolint:nilerr // without the process list the wait ends; the uninstaller itself did run
		}
		alive := false
		// children (and their children) of what we track, started after it
		for changed := true; changed; {
			changed = false
			for _, p := range procs {
				if _, ok := tracked[p.pid]; ok {
					continue
				}
				if c, ok := tracked[p.ppid]; ok && p.created >= c {
					tracked[p.pid] = p.created
					changed = true
				}
			}
		}
		for _, p := range procs {
			if c, ok := tracked[p.pid]; ok && (c == 0 || p.created == c) {
				alive = true
				break
			}
		}
		if alive {
			quiet = 0
		} else if quiet++; quiet >= 3 {
			return code, nil
		}
		if time.Since(start) > 3*time.Hour {
			return code, nil
		}
		emitUninst("run", map[string]any{"secs": int(time.Since(start).Seconds())})
		time.Sleep(700 * time.Millisecond)
	}
}

// stillInstalled: its entry is still in the registry (the uninstaller was
// cancelled or failed). Then nothing is scanned: its files are still in use.
func stillInstalled(a instApp) bool {
	if a.Kind == "store" {
		out, _, _ := runPS(time.Minute, "if (Get-AppxPackage -PackageTypeFilter Main | Where-Object { $_.PackageFullName -eq "+psQuote(strings.TrimPrefix(a.ID, "appx:"))+" }) { 'YES' } else { 'NO' }")
		return strings.Contains(out, "YES")
	}
	return winReg{}.Exists(a.Key)
}

func leftoverScope(a instApp, all []instApp, level int) lfScope {
	pf, pf86 := os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)")
	if pf == "" {
		pf = os.Getenv("ProgramFiles")
	}
	localLow := filepath.Join(P.profile, `AppData\LocalLow`)
	public := os.Getenv("PUBLIC")
	if public == "" {
		public = filepath.Join(filepath.Dir(P.profile), "Public")
	}
	desktop := knownFolder(windows.FOLDERID_Desktop, filepath.Join(P.profile, "Desktop"))
	sc := lfScope{Level: level, RegRoots: []string{`HKCU\Software`, `HKLM\SOFTWARE`, `HKLM32\SOFTWARE`}}
	sc.Roots = []lfRoot{
		{pf, 2, "programs"}, {pf86, 2, "programs"}, {filepath.Join(P.local, "Programs"), 2, "programs"},
		{P.programData, 2, "data"},
		{P.roaming, 2, "userdata"}, {P.local, 2, "userdata"}, {localLow, 2, "userdata"},
		{filepath.Join(P.roaming, `Microsoft\Windows\Start Menu\Programs`), 2, "startmenu"},
		{filepath.Join(P.programData, `Microsoft\Windows\Start Menu\Programs`), 2, "startmenu"},
		{desktop, 1, "desktop"}, {filepath.Join(public, "Desktop"), 1, "desktop"},
		{filepath.Join(P.local, "Packages"), 1, "packages"},
	}
	sc.UserData = []string{P.roaming, P.local, localLow, P.profile}
	sc.Protected = []string{windowsDir(), P.profile, P.temp, filepath.Join(P.local, "Temp")}
	for _, f := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Documents, windows.FOLDERID_Desktop, windows.FOLDERID_Downloads, windows.FOLDERID_Pictures,
		windows.FOLDERID_Music, windows.FOLDERID_Videos, windows.FOLDERID_Favorites, windows.FOLDERID_SavedGames} {
		if p := knownFolder(f, ""); p != "" {
			sc.Protected = append(sc.Protected, p)
		}
	}
	mine := normName(cleanAppName(a.Name))
	for _, o := range all {
		if o.ID == a.ID {
			continue
		}
		if n := normName(cleanAppName(o.Name)); n != mine {
			sc.OtherNames = append(sc.OtherNames, n)
			if o.Location != "" && !strings.EqualFold(filepath.Clean(o.Location), filepath.Clean(a.Location)) {
				sc.Protected = append(sc.Protected, o.Location)
			}
		}
	}
	return sc
}

func knownFolder(id *windows.KNOWNFOLDERID, def string) string {
	if p, err := windows.KnownFolderPath(id, 0); err == nil && p != "" {
		return p
	}
	return def
}

type unResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type unRemoveResult struct {
	Items  []unResult `json:"items"`
	Backup string     `json:"backup,omitempty"` // the .reg file with the deleted keys
	Freed  int64      `json:"freed"`
}

// uninstRemove deletes the chosen leftovers of the last scan: keys and
// values after a backup (.reg), files and folders to the Recycle Bin.
func uninstRemove(ids []string) unRemoveResult {
	var res unRemoveResult
	uj.mu.Lock()
	found, sc, op, a := uj.found, uj.scope, uj.op, uj.app
	uj.mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var pick []leftover
	for _, l := range found {
		if want[l.ID] {
			pick = append(pick, l)
		}
	}
	if len(pick) == 0 {
		return res
	}
	// keys first: one backup file for all of them
	var keys []string
	for _, l := range pick {
		if l.Kind == "key" || l.Kind == "value" {
			keys = append(keys, l.Path)
		}
	}
	backupOK := true
	if len(keys) > 0 {
		name := time.Now().Format("2006-01-02 150405") + " " + safeFileName(a.Name) + ".reg"
		file := filepath.Join(regBackupDir(), name)
		if err := regExportAll(uniqueStrings(keys), file); err != nil {
			backupOK = false
			op.fail("", T("un.err.backupMake", err.Error()), nil)
		} else {
			res.Backup = name
			op.info(file, T("log.unBackup"), map[string]any{"keys": len(keys)})
		}
	}
	var recycled []delResult
	for _, l := range pick {
		r := unResult{ID: l.ID}
		switch l.Kind {
		case "key", "value":
			if !backupOK {
				r.Error = T("un.err.noBackup")
				break
			}
			var err error
			if l.Kind == "key" {
				err = regDeleteKey(l.Path)
			} else {
				err = regDeleteValue(l.Path, l.Value)
			}
			if err != nil {
				r.Error = err.Error()
			} else {
				r.OK = true
			}
		default:
			if protectedFor(l.Path, sc.Protected) || isRootOf(l.Path, sc.Roots) {
				r.Error = T("da.err.protected")
				break
			}
			if err := recycleMany([]string{l.Path}); err != nil {
				r.Error = err.Error()
			} else if _, err := os.Lstat(l.Path); err == nil {
				r.Error = T("un.err.stayed")
			} else {
				r.OK = true
				res.Freed += l.Size
			}
			recycled = append(recycled, delResult{Path: l.Path, OK: r.OK, Error: r.Error, Size: l.Size})
		}
		what := l.Path
		if l.Value != "" {
			what += ` → ` + l.Value
		}
		if r.OK {
			op.info(what, T("log.unRemoved"), nil)
		} else {
			op.warnf(what, r.Error, nil)
		}
		res.Items = append(res.Items, r)
	}
	noteRecycled("uninst", recycled)
	n := 0
	for _, r := range res.Items {
		if r.OK {
			n++
		}
	}
	op.end(T("log.unDone", n, len(res.Items)), map[string]any{"freed": res.Freed, "backup": res.Backup})
	uj.mu.Lock()
	uj.op = nil
	uj.mu.Unlock()
	go func() { _ = appsList() }()
	return res
}

func isRootOf(p string, roots []lfRoot) bool {
	for _, r := range roots {
		if strings.EqualFold(filepath.Clean(r.Path), filepath.Clean(p)) {
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if k := strings.ToLower(s); !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

func safeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60])
	}
	return strings.TrimSpace(s)
}

func uninstOpenLocation(id string) bool {
	a, _, ok := findApp(id)
	if !ok || a.Location == "" {
		return false
	}
	shellOpen(a.Location)
	return true
}
