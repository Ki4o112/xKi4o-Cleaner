//go:build windows

package main

// Startup Manager.
//
// Lists what starts with Windows: the Run keys of the registry, the Startup
// folders and Task Scheduler tasks that run at log-on or at boot (Windows'
// own \Microsoft\ tasks are left out). Turning an entry off uses the same
// mechanisms as Windows itself, so nothing is deleted and everything can be
// turned on again:
//   - Run keys and Startup folders: the StartupApproved value that Task
//     Manager's Startup tab writes (the original entry stays untouched)
//   - scheduled tasks: the task's own Enabled setting (schtasks /change)
// Before every change the previous value is saved; every change is logged.
// Nothing is ever changed without the user clicking it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved`
)

type startupEntry struct {
	Key       string   `json:"key"` // stable id: source|scope|name
	Name      string   `json:"name"`
	Source    string   `json:"source"` // registry, folder, task
	Scope     string   `json:"scope"`  // user, machine, machine32
	Location  string   `json:"location"`
	Command   string   `json:"command"`
	Exe       string   `json:"exe"`
	Args      string   `json:"args,omitempty"`
	Exists    bool     `json:"exists"`
	Enabled   bool     `json:"enabled"`
	Publisher string   `json:"publisher,omitempty"`
	Signed    string   `json:"signed,omitempty"` // Valid, NotSigned, HashMismatch, ...
	Desc      string   `json:"desc,omitempty"`
	Version   string   `json:"version,omitempty"`
	Size      int64    `json:"size,omitempty"`
	Kind      string   `json:"kind"`               // app, windows, security, driver
	Warnings  []string `json:"warnings,omitempty"` // worth a closer look (not a verdict)
	Trigger   string   `json:"trigger,omitempty"`  // task: logon, boot
	Delay     string   `json:"delay,omitempty"`
	RunLevel  string   `json:"runLevel,omitempty"`
	User      string   `json:"user,omitempty"`
	Admin     bool     `json:"admin"`             // changing it needs administrator rights
	Changed   int64    `json:"changed,omitempty"` // when Windows recorded it was turned off
	Invalid   bool     `json:"invalid,omitempty"` // the command cannot be read
	Lnk       string   `json:"lnk,omitempty"`     // the shortcut file (Startup folders)
}

type startupList struct {
	Items    []startupEntry `json:"items"`
	TasksErr string         `json:"tasksErr,omitempty"` // tasks could not be read
}

// ---------------------------------------------------------------- reading

type psStartup struct {
	Lnk []struct {
		F string `json:"f"`
		T string `json:"t"`
		A string `json:"a"`
	} `json:"lnk"`
	Tasks []struct {
		Path   string `json:"path"`
		Name   string `json:"name"`
		State  string `json:"state"`
		Exe    string `json:"exe"`
		Args   string `json:"args"`
		Author string `json:"author"`
		Desc   string `json:"desc"`
		Trig   string `json:"trig"`
		Delay  string `json:"delay"`
		Level  string `json:"level"`
		User   string `json:"user"`
	} `json:"tasks"`
	TasksOK bool `json:"tasksOk"`
	Sig     []struct {
		F   string `json:"f"`
		St  string `json:"st"`
		Sub string `json:"sub"`
	} `json:"sig"`
}

const startupScript = `$ErrorActionPreference='SilentlyContinue'
$in = Get-Content -Raw -Encoding UTF8 -LiteralPath $args[0] | ConvertFrom-Json
$o = [ordered]@{ lnk=@(); tasks=@(); tasksOk=$false; sig=@() }
$exes = New-Object System.Collections.Generic.List[string]
foreach ($e in @($in.exe)) { if ($e) { $exes.Add([string]$e) } }
$sh = New-Object -ComObject WScript.Shell
foreach ($f in @($in.lnk)) {
  try { $l = $sh.CreateShortcut($f); $o.lnk += [pscustomobject]@{ f=$f; t=[string]$l.TargetPath; a=[string]$l.Arguments }; if ($l.TargetPath) { $exes.Add([string]$l.TargetPath) } } catch {}
}
try {
  $all = Get-ScheduledTask -ErrorAction Stop
  $o.tasksOk = $true
  foreach ($t in $all) {
    if ($t.TaskPath -like '\Microsoft\*') { continue }
    $tr = @($t.Triggers | Where-Object { $_.CimClass.CimClassName -eq 'MSFT_TaskLogonTrigger' -or $_.CimClass.CimClassName -eq 'MSFT_TaskBootTrigger' })
    if ($tr.Count -eq 0) { continue }
    $a = @($t.Actions | Where-Object { $_.CimClass.CimClassName -eq 'MSFT_TaskExecAction' }) | Select-Object -First 1
    $exe = ''; $arg = ''
    if ($a) { $exe = [Environment]::ExpandEnvironmentVariables(([string]$a.Execute).Trim().Trim('"')); $arg = [string]$a.Arguments }
    if ($exe) { $exes.Add($exe) }
    $kinds = @($tr | ForEach-Object { if ($_.CimClass.CimClassName -eq 'MSFT_TaskBootTrigger') { 'boot' } else { 'logon' } }) | Sort-Object -Unique
    $o.tasks += [pscustomobject]@{ path=[string]$t.TaskPath; name=[string]$t.TaskName; state=[string]$t.State; exe=$exe; args=$arg;
      author=[string]$t.Author; desc=[string]$t.Description; trig=($kinds -join ','); delay=[string]$tr[0].Delay;
      level=[string]$t.Principal.RunLevel; user=[string]$t.Principal.UserId }
  }
} catch {}
foreach ($e in ($exes | Sort-Object -Unique)) {
  if ($e -and (Test-Path -LiteralPath $e -PathType Leaf)) {
    $s = Get-AuthenticodeSignature -LiteralPath $e
    $sub = ''; if ($s.SignerCertificate) { $sub = [string]$s.SignerCertificate.Subject }
    $o.sig += [pscustomobject]@{ f=$e; st=[string]$s.Status; sub=$sub }
  }
}
ConvertTo-Json -InputObject $o -Depth 5 -Compress`

// signerName picks the organisation (or common name) from a certificate subject.
func signerName(sub string) string {
	var o, cn string
	for _, part := range strings.Split(sub, ", ") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch strings.TrimSpace(k) {
		case "O":
			o = v
		case "CN":
			cn = v
		}
	}
	if o != "" {
		return o
	}
	return cn
}

func expandEnv(s string) string { return expandPercent(s, os.Getenv) }

// resolveExe turns the program part of a command into a full path.
func resolveExe(exe string) string {
	exe = strings.TrimSpace(expandEnv(exe))
	if exe == "" || filepath.IsAbs(exe) {
		return exe
	}
	for _, dir := range []string{sys32(""), P.windir, filepath.Join(P.windir, "SysWOW64")} {
		p := filepath.Join(dir, exe)
		if fileExistsAny(p) {
			return p
		}
		if filepath.Ext(exe) == "" && fileExistsAny(p+".exe") {
			return p + ".exe"
		}
	}
	return exe
}

func approvedState(root registry.Key, sub, name string) (enabled bool, changed int64, raw []byte, exists bool) {
	k, err := registry.OpenKey(root, approvedKey+`\`+sub, registry.QUERY_VALUE)
	if err != nil {
		return true, 0, nil, false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(name)
	if err != nil {
		return true, 0, nil, false
	}
	en, _ := approvedEnabled(b)
	if !en && len(b) >= 12 {
		ft := windows.Filetime{LowDateTime: uint32(b[4]) | uint32(b[5])<<8 | uint32(b[6])<<16 | uint32(b[7])<<24,
			HighDateTime: uint32(b[8]) | uint32(b[9])<<8 | uint32(b[10])<<16 | uint32(b[11])<<24}
		if ft.HighDateTime != 0 {
			changed = ft.Nanoseconds() / 1e9
		}
	}
	return en, changed, b, true
}

type regSource struct {
	root   registry.Key
	access uint32
	scope  string
	sub    string // StartupApproved subkey
	label  string
}

func runSources() []regSource {
	return []regSource{
		{registry.CURRENT_USER, 0, "user", "Run", `HKCU\` + runKey},
		{registry.LOCAL_MACHINE, registry.WOW64_64KEY, "machine", "Run", `HKLM\` + runKey},
		{registry.LOCAL_MACHINE, registry.WOW64_32KEY, "machine32", "Run32", `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run`},
	}
}

func startupFolders() []struct{ dir, scope string } {
	return []struct{ dir, scope string }{
		{filepath.Join(P.roaming, `Microsoft\Windows\Start Menu\Programs\Startup`), "user"},
		{filepath.Join(P.programData, `Microsoft\Windows\Start Menu\Programs\StartUp`), "machine"},
	}
}

var startupMu sync.Mutex

// listStartup reads every startup entry. It changes nothing.
func listStartup() (res startupList) {
	startupMu.Lock()
	defer startupMu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			res.TasksErr = fmt.Sprint(r)
		}
	}()
	var items []startupEntry
	// registry
	for _, src := range runSources() {
		k, err := registry.OpenKey(src.root, runKey, registry.QUERY_VALUE|src.access)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(-1)
		for _, n := range names {
			cmd, _, err := k.GetStringValue(n)
			e := startupEntry{Key: "reg|" + src.scope + "|" + n, Name: n, Source: "registry", Scope: src.scope,
				Location: src.label, Command: cmd, Admin: src.scope != "user"}
			if n == "" {
				e.Name = "(default)"
			}
			if err != nil {
				e.Invalid = true
			}
			e.Exe, e.Args = commandParts(cmd)
			e.Enabled, e.Changed, _, _ = approvedState(src.root, src.sub, n)
			items = append(items, e)
		}
		k.Close()
	}
	// Startup folders
	var lnks []string
	for _, f := range startupFolders() {
		for _, p := range readDir(f.dir) {
			if p.info.IsDir() || strings.EqualFold(p.info.Name(), "desktop.ini") {
				continue
			}
			root := registry.CURRENT_USER
			if f.scope == "machine" {
				root = registry.LOCAL_MACHINE
			}
			e := startupEntry{Key: "folder|" + f.scope + "|" + p.info.Name(), Name: strings.TrimSuffix(p.info.Name(), filepath.Ext(p.info.Name())),
				Source: "folder", Scope: f.scope, Location: f.dir, Command: p.path, Exe: p.path, Lnk: p.path, Admin: f.scope != "user"}
			e.Enabled, e.Changed, _, _ = approvedState(root, "StartupFolder", p.info.Name())
			if strings.EqualFold(filepath.Ext(p.path), ".lnk") {
				lnks = append(lnks, p.path)
			}
			items = append(items, e)
		}
	}
	// PowerShell: shortcut targets, tasks, signatures
	var exes []string
	for i := range items {
		if items[i].Source == "registry" {
			items[i].Exe = resolveExe(items[i].Exe)
			exes = append(exes, items[i].Exe)
		}
	}
	ps := runStartupPS(lnks, exes)
	if !ps.TasksOK {
		res.TasksErr = T("su.tasksErr")
	}
	targets := map[string][2]string{}
	for _, l := range ps.Lnk {
		targets[normPath(l.F)] = [2]string{l.T, l.A}
	}
	for i := range items {
		if items[i].Source == "folder" {
			if t, ok := targets[normPath(items[i].Lnk)]; ok && t[0] != "" {
				items[i].Exe, items[i].Args = resolveExe(t[0]), t[1]
				items[i].Command = strings.TrimSpace(`"` + t[0] + `" ` + t[1])
			}
		}
	}
	for _, t := range ps.Tasks {
		full := t.Path + t.Name
		e := startupEntry{Key: "task|" + full, Name: t.Name, Source: "task", Scope: "machine", Location: `Task Scheduler` + t.Path,
			Exe: resolveExe(t.Exe), Args: t.Args, Command: strings.TrimSpace(`"` + t.Exe + `" ` + t.Args), Enabled: !strings.EqualFold(t.State, "Disabled"),
			Trigger: t.Trig, Delay: t.Delay, RunLevel: t.Level, User: t.User, Desc: t.Desc, Admin: true}
		if t.Exe == "" {
			e.Invalid = true
		}
		if e.Publisher == "" && t.Author != "" && !strings.HasPrefix(t.Author, "$(") {
			e.Publisher = t.Author
		}
		items = append(items, e)
	}
	sigs := map[string][2]string{}
	for _, s := range ps.Sig {
		sigs[normPath(s.F)] = [2]string{s.St, s.Sub}
	}
	for i := range items {
		describeStartup(&items[i], sigs)
	}
	sort.SliceStable(items, func(a, b int) bool { return strings.ToLower(items[a].Name) < strings.ToLower(items[b].Name) })
	res.Items = items
	return res
}

func runStartupPS(lnks, exes []string) psStartup {
	var out psStartup
	in, _ := json.Marshal(map[string][]string{"lnk": lnks, "exe": exes})
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner")
	_ = os.MkdirAll(dir, 0o755)
	f := filepath.Join(dir, "startup-query.json")
	if os.WriteFile(f, in, 0o644) != nil {
		return out
	}
	defer os.Remove(f)
	script := "& { " + startupScript + " } " + psQuote(f)
	res, _, err := runPS(3*time.Minute, script)
	if err == nil {
		_ = json.Unmarshal([]byte(res), &out)
	}
	return out
}

var hardwareVendors = []string{"realtek", "nvidia", "intel", "advanced micro devices", "amd", "synaptics", "elan", "dolby", "conexant",
	"waves", "logitech", "razer", "corsair", "steelseries", "asustek", "micro-star", "msi", "gigabyte", "lenovo", "hp inc", "hewlett",
	"dell", "acer", "alps", "cirrus", "qualcomm", "broadcom", "marvell", "creative", "nahimic", "a-volute", "maxx"}

var securityVendors = []string{"microsoft windows defender", "kaspersky", "eset", "bitdefender", "avast", "avg", "norton", "symantec",
	"gen digital", "mcafee", "malwarebytes", "sophos", "avira", "f-secure", "trend micro", "panda", "comodo", "webroot", "g data", "emsisoft", "360"}

// describeStartup fills publisher, file facts, the kind and the warnings.
func describeStartup(e *startupEntry, sigs map[string][2]string) {
	if e.Exe == "" {
		e.Invalid = true
	}
	if fi, err := os.Stat(e.Exe); err == nil && !fi.IsDir() {
		e.Exists, e.Size = true, fi.Size()
	}
	hidden := false
	if e.Exists {
		v := fileVersionStrings(e.Exe, "CompanyName", "FileDescription", "FileVersion", "ProductVersion")
		if e.Desc == "" {
			e.Desc = v["FileDescription"]
		}
		e.Version = v["ProductVersion"]
		if e.Version == "" {
			e.Version = v["FileVersion"]
		}
		if s, ok := sigs[normPath(e.Exe)]; ok {
			e.Signed = s[0]
			if s[0] == "Valid" {
				e.Publisher = signerName(s[1])
			}
		}
		if e.Publisher == "" {
			e.Publisher = v["CompanyName"]
		}
		if p, err := windows.UTF16PtrFromString(e.Exe); err == nil {
			if a, err := windows.GetFileAttributes(p); err == nil && a&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
				hidden = true
			}
		}
	}
	pub := strings.ToLower(e.Publisher)
	base := strings.ToLower(filepath.Base(e.Exe))
	win := strings.ToLower(filepath.Clean(P.windir)) + `\`
	switch {
	case securityProcs[base] || base == "securityhealthsystray.exe" || matchesAny(pub, securityVendors):
		e.Kind = "security"
	case strings.HasPrefix(strings.ToLower(e.Exe), win) && e.Signed == "Valid" && strings.Contains(pub, "microsoft"):
		e.Kind = "windows"
	case e.Signed == "Valid" && matchesAny(pub, hardwareVendors):
		e.Kind = "driver"
	default:
		e.Kind = "app"
	}
	e.Warnings = startupWarnings(startupFacts{Exe: e.Exe, Args: e.Args, Exists: e.Exists, Signed: e.Signed, Hidden: hidden,
		WinDir: P.windir, Temp: os.Getenv("TEMP"), Profile: P.profile})
}

func matchesAny(s string, list []string) bool {
	if s == "" {
		return false
	}
	for _, v := range list {
		if strings.HasPrefix(s, v) || strings.Contains(s, " "+v) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- changes

type startupBackup struct {
	Key     string `json:"key"`
	Exists  bool   `json:"exists"`            // StartupApproved value existed
	Data    []byte `json:"data,omitempty"`    // its bytes
	Enabled bool   `json:"enabled"`           // the entry was on
	Command string `json:"command,omitempty"` // for reference: what the entry runs
}

type startupLog struct {
	ID       int64          `json:"id"`
	At       int64          `json:"at"`
	Key      string         `json:"key"`
	Name     string         `json:"name"`
	Source   string         `json:"source"`
	Action   string         `json:"action"` // enable, disable, restore
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Backup   *startupBackup `json:"backup,omitempty"`
	Restored bool           `json:"restored,omitempty"`
}

type startupResult struct {
	OK    bool          `json:"ok"`
	Error string        `json:"error,omitempty"`
	Entry *startupEntry `json:"entry,omitempty"`
}

func startupLogPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", "startup-log.json")
}

func readStartupLog() []*startupLog {
	var l []*startupLog
	if b, err := os.ReadFile(startupLogPath()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

func writeStartupLog(l []*startupLog) {
	if len(l) > 500 {
		l = l[len(l)-500:]
	}
	b, _ := json.MarshalIndent(l, "", " ")
	p := startupLogPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p+".tmp", b, 0o644)
	_ = os.Rename(p+".tmp", p)
}

func appendStartupLog(e *startupLog) {
	l := readStartupLog()
	now := time.Now()
	e.ID, e.At = now.UnixMicro(), now.Unix() // exact as a JavaScript number
	if n := len(l); n > 0 && l[n-1].ID >= e.ID {
		e.ID = l[n-1].ID + 1
	}
	writeStartupLog(append(l, e))
}

// startupLogList returns the log, newest first.
func startupLogList() []*startupLog {
	l := readStartupLog()
	for i, j := 0, len(l)-1; i < j; i, j = i+1, j-1 {
		l[i], l[j] = l[j], l[i]
	}
	return l
}

func parseKey(key string) (source, scope, name string, ok bool) {
	if strings.HasPrefix(key, "task|") && len(key) > 5 {
		return "task", "machine", key[5:], true
	}
	parts := strings.SplitN(key, "|", 3)
	switch {
	case len(parts) == 3 && (parts[0] == "reg" || parts[0] == "folder"):
		return parts[0], parts[1], parts[2], true
	case len(parts) == 2 && parts[0] == "task":
		return "task", "machine", parts[1], true
	}
	return "", "", "", false
}

func approvedLoc(source, scope string) (registry.Key, string, bool) {
	switch {
	case source == "reg" && scope == "user":
		return registry.CURRENT_USER, "Run", true
	case source == "reg" && scope == "machine":
		return registry.LOCAL_MACHINE, "Run", true
	case source == "reg" && scope == "machine32":
		return registry.LOCAL_MACHINE, "Run32", true
	case source == "folder" && scope == "user":
		return registry.CURRENT_USER, "StartupFolder", true
	case source == "folder" && scope == "machine":
		return registry.LOCAL_MACHINE, "StartupFolder", true
	}
	return 0, "", false
}

// findStartup reads one entry again (fresh state).
func findStartup(key string) *startupEntry {
	for _, e := range listStartupQuick() {
		if e.Key == key {
			c := e
			return &c
		}
	}
	return nil
}

// listStartupQuick is the registry and folder part only (no PowerShell),
// used to check single entries right after a change.
func listStartupQuick() []startupEntry {
	var items []startupEntry
	for _, src := range runSources() {
		k, err := registry.OpenKey(src.root, runKey, registry.QUERY_VALUE|src.access)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(-1)
		for _, n := range names {
			cmd, _, _ := k.GetStringValue(n)
			e := startupEntry{Key: "reg|" + src.scope + "|" + n, Name: n, Source: "registry", Scope: src.scope, Command: cmd}
			e.Enabled, e.Changed, _, _ = approvedState(src.root, src.sub, n)
			items = append(items, e)
		}
		k.Close()
	}
	for _, f := range startupFolders() {
		for _, p := range readDir(f.dir) {
			root := registry.CURRENT_USER
			if f.scope == "machine" {
				root = registry.LOCAL_MACHINE
			}
			e := startupEntry{Key: "folder|" + f.scope + "|" + p.info.Name(), Name: p.info.Name(), Source: "folder", Scope: f.scope, Command: p.path}
			e.Enabled, e.Changed, _, _ = approvedState(root, "StartupFolder", p.info.Name())
			items = append(items, e)
		}
	}
	return items
}

// taskEnabled reads a task's Enabled setting from its XML (the same in
// every Windows language).
func taskEnabled(full string) (bool, error) {
	out, code, err := runQuiet(time.Minute, sys32("schtasks.exe"), "/Query", "/TN", full, "/XML")
	if err != nil || code != 0 {
		return false, errors.New(T("su.err.task"))
	}
	i := strings.Index(out, "<Settings>")
	j := strings.Index(out, "</Settings>")
	if i < 0 || j < i {
		return true, nil
	}
	return !strings.Contains(out[i:j], "<Enabled>false</Enabled>"), nil
}

// setStartup turns one entry on or off.
func setStartup(key string, enable bool) startupResult {
	startupMu.Lock()
	defer startupMu.Unlock()
	src, scope, name, ok := parseKey(key)
	if !ok {
		return startupResult{Error: T("su.err.unknown")}
	}
	log := &startupLog{Key: key, Name: name, Source: src, Action: map[bool]string{true: "enable", false: "disable"}[enable]}
	res := doSetStartup(src, scope, name, enable, log)
	log.OK, log.Error = res.OK, res.Error
	appendStartupLog(log)
	logStartupOp(log.Action, name, key, res)
	return res
}

// logStartupOp writes a startup change to the operation log.
func logStartupOp(action, name, key string, res startupResult) {
	op := beginOp("startup", name, T("log.su."+action), map[string]any{"key": key})
	if res.OK {
		op.info(name, T("log.suDone"), nil)
	} else {
		op.fail(name, res.Error, nil)
	}
	op.end("", nil)
}

func doSetStartup(src, scope, name string, enable bool, log *startupLog) startupResult {
	if src == "task" {
		if !app.admin {
			return startupResult{Error: T("blocked.admin")}
		}
		was, err := taskEnabled(name)
		if err != nil {
			return startupResult{Error: err.Error()}
		}
		log.Backup = &startupBackup{Key: "task|" + name, Enabled: was}
		flag := map[bool]string{true: "/ENABLE", false: "/DISABLE"}[enable]
		if out, code, err := runQuiet(time.Minute, sys32("schtasks.exe"), "/Change", "/TN", name, flag); err != nil || code != 0 {
			msg := strings.TrimSpace(out)
			if msg == "" {
				msg = T("tw.err.code", code)
			}
			return startupResult{Error: msg}
		}
		now, err := taskEnabled(name)
		if err != nil || now != enable {
			return startupResult{Error: T("su.err.verify")}
		}
		return startupResult{OK: true}
	}
	root, sub, ok := approvedLoc(src, scope)
	if !ok {
		return startupResult{Error: T("su.err.unknown")}
	}
	if root == registry.LOCAL_MACHINE && !app.admin {
		return startupResult{Error: T("blocked.admin")}
	}
	valName := name
	was, _, raw, exists := approvedState(root, sub, valName)
	log.Backup = &startupBackup{Key: src + "|" + scope + "|" + name, Exists: exists, Data: raw, Enabled: was}
	if e := findStartup(src + "|" + scope + "|" + name); e != nil {
		log.Backup.Command = e.Command
	} else {
		return startupResult{Error: T("su.err.gone")}
	}
	k, _, err := registry.CreateKey(root, approvedKey+`\`+sub, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return startupResult{Error: err.Error()}
	}
	err = k.SetBinaryValue(valName, approvedBytes(enable, time.Now()))
	k.Close()
	if err != nil {
		return startupResult{Error: err.Error()}
	}
	if now, _, _, _ := approvedState(root, sub, valName); now != enable {
		return startupResult{Error: T("su.err.verify")}
	}
	e := findStartup(src + "|" + scope + "|" + name)
	return startupResult{OK: true, Entry: e}
}

// restoreStartup puts back the value saved before a logged change.
func restoreStartup(id int64) startupResult {
	startupMu.Lock()
	defer startupMu.Unlock()
	l := readStartupLog()
	var entry *startupLog
	for _, e := range l {
		if e.ID == id {
			entry = e
		}
	}
	if entry == nil || entry.Backup == nil || !entry.OK || entry.Restored {
		return startupResult{Error: T("su.err.norestore")}
	}
	b := entry.Backup
	src, scope, name, _ := parseKey(b.Key)
	log := &startupLog{Key: b.Key, Name: name, Source: src, Action: "restore"}
	res := startupResult{Error: T("su.err.unknown")}
	if src == "task" {
		res = doSetStartup(src, scope, name, b.Enabled, log)
	} else if root, sub, ok := approvedLoc(src, scope); ok {
		if root == registry.LOCAL_MACHINE && !app.admin {
			res = startupResult{Error: T("blocked.admin")}
		} else if k, _, err := registry.CreateKey(root, approvedKey+`\`+sub, registry.SET_VALUE|registry.QUERY_VALUE); err != nil {
			res = startupResult{Error: err.Error()}
		} else {
			cur, _, raw, exists := approvedState(root, sub, name)
			log.Backup = &startupBackup{Key: b.Key, Exists: exists, Data: raw, Enabled: cur}
			if b.Exists {
				err = k.SetBinaryValue(name, b.Data)
			} else {
				err = k.DeleteValue(name)
				if errors.Is(err, registry.ErrNotExist) {
					err = nil
				}
			}
			k.Close()
			if err != nil {
				res = startupResult{Error: err.Error()}
			} else if now, _, _, _ := approvedState(root, sub, name); now != b.Enabled {
				res = startupResult{Error: T("su.err.verify")}
			} else {
				res = startupResult{OK: true}
			}
		}
	}
	log.OK, log.Error = res.OK, res.Error
	if res.OK {
		entry.Restored = true
		writeStartupLog(l)
	}
	appendStartupLog(log)
	logStartupOp("restore", name, b.Key, res)
	return res
}
