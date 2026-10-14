//go:build windows

package main

// Tweaker engine.
//
// Every tweak describes the registry values and services it changes, and
// may add its own steps (powercfg, bcdedit, apps, ...). The engine:
//   - reads the real state from Windows, so a switch shows what is set now
//   - saves what was there before the first change (tweaks-backup.json),
//     so turning a tweak off restores exactly the old values
//   - falls back to the Windows defaults when there is no backup
//   - runs one tweak at a time

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// ---------------------------------------------------------------- registry values

type rkey uint8

const (
	hkcu rkey = iota
	hklm
	hku
)

func (r rkey) key() registry.Key {
	switch r {
	case hklm:
		return registry.LOCAL_MACHINE
	case hku:
		return registry.USERS
	}
	return registry.CURRENT_USER
}

// rv is one registry value a tweak sets.
type rv struct {
	root     rkey
	path     string
	name     string
	kind     uint32 // registry.DWORD, QWORD, SZ
	on       any    // uint32, uint64 or string
	off      any    // Windows default when there is no backup; nil = delete
	absentOn bool   // a missing value already means "on"
	keys     []string
}

func dw(root rkey, path, name string, on uint32, off any) rv {
	return rv{root: root, path: path, name: name, kind: registry.DWORD, on: on, off: off}
}

func sz(root rkey, path, name, on string, off any) rv {
	return rv{root: root, path: path, name: name, kind: registry.SZ, on: on, off: off}
}

func norm(v any) string {
	switch x := v.(type) {
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case int:
		return strconv.Itoa(x)
	case string:
		return strings.TrimSpace(x)
	}
	return fmt.Sprint(v)
}

// read returns the current value as text and whether it exists.
func (v rv) read() (string, bool) {
	k, err := registry.OpenKey(v.root.key(), v.path, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	_, typ, err := k.GetValue(v.name, nil)
	if err != nil {
		return "", false
	}
	switch typ {
	case registry.DWORD, registry.QWORD:
		n, _, err := k.GetIntegerValue(v.name)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(n, 10), true
	case registry.SZ, registry.EXPAND_SZ:
		s, _, err := k.GetStringValue(v.name)
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(s), true
	}
	return "", true
}

func (v rv) isOn() bool {
	cur, ok := v.read()
	if !ok {
		return v.absentOn
	}
	return cur == norm(v.on)
}

func (v rv) write(val any) error {
	if val == nil {
		k, err := registry.OpenKey(v.root.key(), v.path, registry.SET_VALUE)
		if err != nil {
			return nil // nothing to delete
		}
		defer k.Close()
		if err := k.DeleteValue(v.name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	k, _, err := registry.CreateKey(v.root.key(), v.path, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	switch x := val.(type) {
	case uint32:
		if v.kind == registry.QWORD {
			return k.SetQWordValue(v.name, uint64(x))
		}
		if v.kind == registry.SZ {
			return k.SetStringValue(v.name, strconv.FormatUint(uint64(x), 10))
		}
		return k.SetDWordValue(v.name, x)
	case uint64:
		return k.SetQWordValue(v.name, x)
	case string:
		if v.kind == registry.EXPAND_SZ {
			return k.SetExpandStringValue(v.name, x)
		}
		return k.SetStringValue(v.name, x)
	}
	return fmt.Errorf("unsupported value %T", val)
}

// dropEmptyKeys removes keys (deepest first) that hold nothing any more.
func dropEmptyKeys(root rkey, paths []string) {
	for _, p := range paths {
		k, err := registry.OpenKey(root.key(), p, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		info, err := k.Stat()
		k.Close()
		if err != nil || info.SubKeyCount > 0 || info.ValueCount > 0 {
			continue
		}
		_ = registry.DeleteKey(root.key(), p)
	}
}

// missingKeys lists the parts of path that do not exist yet, outermost first.
func missingKeys(root rkey, path string) []string {
	var miss []string
	p := path
	for p != "" {
		k, err := registry.OpenKey(root.key(), p, registry.QUERY_VALUE)
		if err == nil {
			k.Close()
			break
		}
		miss = append([]string{p}, miss...)
		i := strings.LastIndex(p, `\`)
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return miss
}

// ---------------------------------------------------------------- backup

type regSnap struct {
	Root    rkey     `json:"root"`
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Exists  bool     `json:"exists"`
	Type    uint32   `json:"type,omitempty"`
	Int     uint64   `json:"int,omitempty"`
	Str     string   `json:"str,omitempty"`
	Strs    []string `json:"strs,omitempty"`
	Bin     []byte   `json:"bin,omitempty"`
	Created []string `json:"created,omitempty"`
}

type svcSnap struct {
	Name    string `json:"name"`
	Start   uint32 `json:"start"`
	Delayed bool   `json:"delayed"`
}

type tweakBackup struct {
	Reg   []regSnap         `json:"reg,omitempty"`
	Svc   []svcSnap         `json:"svc,omitempty"`
	Extra map[string]string `json:"extra,omitempty"`
	At    int64             `json:"at"`
}

var (
	backupMu sync.Mutex
	backups  map[string]*tweakBackup
)

func backupPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", "tweaks-backup.json")
}

func loadBackups() {
	if backups != nil {
		return
	}
	backups = map[string]*tweakBackup{}
	if b, err := os.ReadFile(backupPath()); err == nil {
		_ = json.Unmarshal(b, &backups)
		if backups == nil {
			backups = map[string]*tweakBackup{}
		}
	}
}

func saveBackups() error {
	b, err := json.MarshalIndent(backups, "", " ")
	if err != nil {
		return err
	}
	p := backupPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func getBackup(id string) *tweakBackup {
	backupMu.Lock()
	defer backupMu.Unlock()
	loadBackups()
	return backups[id]
}

func putBackup(id string, b *tweakBackup) {
	backupMu.Lock()
	defer backupMu.Unlock()
	loadBackups()
	if b == nil {
		delete(backups, id)
	} else {
		backups[id] = b
	}
	_ = saveBackups()
}

func snapReg(v rv) regSnap {
	s := regSnap{Root: v.root, Path: v.path, Name: v.name, Created: missingKeys(v.root, v.path)}
	k, err := registry.OpenKey(v.root.key(), v.path, registry.QUERY_VALUE)
	if err != nil {
		return s
	}
	defer k.Close()
	_, typ, err := k.GetValue(v.name, nil)
	if err != nil {
		return s
	}
	s.Exists, s.Type = true, typ
	switch typ {
	case registry.DWORD, registry.QWORD:
		s.Int, _, _ = k.GetIntegerValue(v.name)
	case registry.SZ, registry.EXPAND_SZ:
		s.Str, _, _ = k.GetStringValue(v.name)
	case registry.MULTI_SZ:
		s.Strs, _, _ = k.GetStringsValue(v.name)
	default:
		buf := make([]byte, 64*1024)
		if n, _, err := k.GetValue(v.name, buf); err == nil {
			s.Bin = append([]byte(nil), buf[:n]...)
		}
	}
	return s
}

func restoreReg(s regSnap) error {
	if !s.Exists {
		k, err := registry.OpenKey(s.Root.key(), s.Path, registry.SET_VALUE)
		if err == nil {
			_ = k.DeleteValue(s.Name)
			k.Close()
		}
		for i := len(s.Created) - 1; i >= 0; i-- {
			dropEmptyKeys(s.Root, []string{s.Created[i]})
		}
		return nil
	}
	k, _, err := registry.CreateKey(s.Root.key(), s.Path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	switch s.Type {
	case registry.DWORD:
		return k.SetDWordValue(s.Name, uint32(s.Int))
	case registry.QWORD:
		return k.SetQWordValue(s.Name, s.Int)
	case registry.SZ:
		return k.SetStringValue(s.Name, s.Str)
	case registry.EXPAND_SZ:
		return k.SetExpandStringValue(s.Name, s.Str)
	case registry.MULTI_SZ:
		return k.SetStringsValue(s.Name, s.Strs)
	}
	return k.SetBinaryValue(s.Name, s.Bin)
}

// ---------------------------------------------------------------- services

type svcSet struct {
	name       string
	on         uint32 // windows.SERVICE_AUTO_START, SERVICE_DEMAND_START, SERVICE_DISABLED
	off        uint32 // Windows default
	offDelayed bool
}

func openSvc(m *mgr.Mgr, name string) (*mgr.Service, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, p, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: h}, nil
}

func svcStart(m *mgr.Mgr, name string) (start uint32, delayed bool, ok bool) {
	s, err := openSvc(m, name)
	if err != nil {
		return 0, false, false
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return 0, false, false
	}
	return c.StartType, c.DelayedAutoStart, true
}

// setSvcStart changes only the start type (nothing else in the service
// configuration is rewritten).
func setSvcStart(m *mgr.Mgr, name string, start uint32, delayed bool) error {
	s, err := openSvc(m, name)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := windows.ChangeServiceConfig(s.Handle, windows.SERVICE_NO_CHANGE, start, windows.SERVICE_NO_CHANGE,
		nil, nil, nil, nil, nil, nil, nil); err != nil {
		return err
	}
	if start == windows.SERVICE_AUTO_START {
		info := windows.SERVICE_DELAYED_AUTO_START_INFO{}
		if delayed {
			info.IsDelayedAutoStartUp = 1
		}
		_ = windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO, (*byte)(unsafe.Pointer(&info)))
	}
	return nil
}

// ---------------------------------------------------------------- model

type tweak struct {
	id       string
	group    string
	rec      bool   // recommended
	risk     string // "", "caution", "risky"
	reboot   bool   // takes effect after a restart
	explorer bool   // Explorer shows it after a restart of Explorer
	oneWay   bool   // cannot be undone from the app
	sys      bool   // system-wide change: a restore point is made first
	slow     bool   // the state needs the PowerShell probe
	reg      []rv
	svc      []svcSet
	setting  string // WM_SETTINGCHANGE area announced after a change
	avail    func(p *probe) string
	detect   func(p *probe) string
	apply    func(c *tctx) error
	revert   func(c *tctx) error
}

type tctx struct {
	t     *tweak
	b     *tweakBackup // backup of the state before the first change
	fresh bool         // the backup was taken just now
	opts  map[string]any
}

func (c *tctx) extra(k string) string {
	if c.b == nil || c.b.Extra == nil {
		return ""
	}
	return c.b.Extra[k]
}

func (c *tctx) setExtra(k, v string) {
	if c.b != nil && c.fresh {
		if c.b.Extra == nil {
			c.b.Extra = map[string]string{}
		}
		c.b.Extra[k] = v
	}
}

type tweakMeta struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Rec      bool   `json:"rec,omitempty"`
	Risk     string `json:"risk,omitempty"`
	Reboot   bool   `json:"reboot,omitempty"`
	Explorer bool   `json:"explorer,omitempty"`
	OneWay   bool   `json:"oneWay,omitempty"`
	Sys      bool   `json:"sys,omitempty"`
}

type tweakState struct {
	ID    string `json:"id"`
	State string `json:"state"` // on, off, partial, na, pending
	Note  string `json:"note,omitempty"`
}

// regState derives the state from the registry values and services.
func (t *tweak) regState() string {
	total, on := 0, 0
	for _, v := range t.reg {
		total++
		if v.isOn() {
			on++
		}
	}
	if len(t.svc) > 0 {
		if m, err := mgr.Connect(); err == nil {
			for _, s := range t.svc {
				st, _, ok := svcStart(m, s.name)
				if !ok {
					continue
				}
				total++
				if st == s.on {
					on++
				}
			}
			m.Disconnect()
		}
	}
	switch {
	case total == 0:
		return "off"
	case on == total:
		return "on"
	case on == 0:
		return "off"
	}
	return "partial"
}

func (t *tweak) state(p *probe) tweakState {
	st := tweakState{ID: t.id}
	defer func() {
		if r := recover(); r != nil {
			st.State, st.Note = "na", fmt.Sprint(r)
		}
	}()
	if t.avail != nil {
		if why := t.avail(p); why != "" {
			st.State, st.Note = "na", why
			return st
		}
	}
	if t.detect != nil {
		st.State = t.detect(p)
	} else {
		st.State = t.regState()
	}
	return st
}

// ---------------------------------------------------------------- probe

// probe gathers, once, the facts that need PowerShell.
type probe struct {
	once  sync.Once
	ps    psFacts
	build uint32
}

type psFacts struct {
	Appx   []string `json:"appx"`
	RTP    *bool    `json:"rtp"`
	Mode   string   `json:"mode"`
	Tamper *bool    `json:"tamper"`
	BL     string   `json:"bl"`
	BLP    string   `json:"blp"`
	CC     string   `json:"cc"`
	ECN    string   `json:"ecn"`
	TS     string   `json:"ts"`
	ok     bool
	appx   map[string]bool
}

const probeScript = `$o=[ordered]@{}; ` +
	`try { $o.appx=@(Get-AppxPackage -ErrorAction Stop | ForEach-Object { $_.Name }) } catch {}; ` +
	`try { $s=Get-MpComputerStatus -ErrorAction Stop; $o.rtp=[bool]$s.RealTimeProtectionEnabled; $o.mode=[string]$s.AMRunningMode; $o.tamper=[bool]$s.IsTamperProtected } catch {}; ` +
	`try { $b=Get-BitLockerVolume -MountPoint $env:SystemDrive -ErrorAction Stop; $o.bl=[string]$b.VolumeStatus; $o.blp=[string]$b.ProtectionStatus } catch { ` +
	`try { $v=Get-CimInstance -Namespace root/cimv2/Security/MicrosoftVolumeEncryption -ClassName Win32_EncryptableVolume -Filter ("DriveLetter='"+$env:SystemDrive+"'") -ErrorAction Stop; if ($v) { $o.blp=[string]$v.ProtectionStatus } else { $o.bl='None' } } catch { $o.bl='None' } }; ` +
	`try { $t=Get-NetTCPSetting -SettingName Internet -ErrorAction Stop; $o.cc=[string]$t.CongestionProvider; $o.ecn=[string]$t.EcnCapability; $o.ts=[string]$t.Timestamps } catch {}; ` +
	`ConvertTo-Json -InputObject $o -Compress -Depth 3`

func newProbe() *probe {
	return &probe{build: windows.RtlGetVersion().BuildNumber}
}

func (p *probe) facts() *psFacts {
	p.once.Do(func() {
		out, _, err := runPS(2*time.Minute, probeScript)
		if err == nil && json.Unmarshal([]byte(out), &p.ps) == nil {
			p.ps.ok = true
		}
		p.ps.appx = map[string]bool{}
		for _, a := range p.ps.Appx {
			p.ps.appx[strings.ToLower(a)] = true
		}
	})
	return &p.ps
}

// hasApp reports whether an app package matching the pattern is installed.
func (p *probe) hasApp(pattern string) bool {
	f := p.facts()
	if !strings.HasSuffix(pattern, "*") {
		return f.appx[strings.ToLower(pattern)]
	}
	for a := range f.appx {
		if pkgMatch(pattern, a) {
			return true
		}
	}
	return false
}

func (p *probe) win11() bool { return p.build >= 22000 }

// ---------------------------------------------------------------- operations

var (
	tweakMu     sync.Mutex
	tweakRPDone atomic.Bool // a restore point was made this session
	tweakScan   atomic.Bool
)

func findTweak(id string) *tweak {
	for _, t := range tweaks {
		if t.id == id {
			return t
		}
	}
	return nil
}

// tweakList returns the catalogue (texts live in the page).
func tweakList() []tweakMeta {
	out := make([]tweakMeta, 0, len(tweaks))
	for _, t := range tweaks {
		out = append(out, tweakMeta{ID: t.id, Group: t.group, Rec: t.rec, Risk: t.risk,
			Reboot: t.reboot, Explorer: t.explorer, OneWay: t.oneWay, Sys: t.sys})
	}
	return out
}

// tweakStates returns the quick states at once; the ones that need
// PowerShell come later as "tweak-state" events, then "tweaks-ready".
func tweakStates() []tweakState {
	p := newProbe()
	out := make([]tweakState, 0, len(tweaks))
	var slow []*tweak
	for _, t := range tweaks {
		if t.slow {
			out = append(out, tweakState{ID: t.id, State: "pending"})
			slow = append(slow, t)
			continue
		}
		out = append(out, t.state(p))
	}
	if len(slow) > 0 && tweakScan.CompareAndSwap(false, true) {
		go func() {
			defer tweakScan.Store(false)
			for _, t := range slow {
				emit("tweak-state", t.state(p))
			}
			emit("tweaks-ready", true)
		}()
	} else if len(slow) > 0 {
		go func() {
			for tweakScan.Load() {
				time.Sleep(200 * time.Millisecond)
			}
			for _, t := range slow {
				emit("tweak-state", t.state(p))
			}
			emit("tweaks-ready", true)
		}()
	}
	return out
}

type tweakResult struct {
	OK       bool       `json:"ok"`
	State    tweakState `json:"state"`
	Error    string     `json:"error,omitempty"`
	Note     string     `json:"note,omitempty"`
	Explorer bool       `json:"explorer,omitempty"`
	Reboot   bool       `json:"reboot,omitempty"`
}

type tweakOpts struct {
	RestorePoint bool     `json:"restorePoint"`
	Apps         []string `json:"apps"`
}

func setTweak(id string, on bool, o tweakOpts) (res tweakResult) {
	t := findTweak(id)
	if t == nil {
		res.Error = T("tw.err.unknown")
		return
	}
	tweakMu.Lock()
	defer tweakMu.Unlock()
	app.working.Store(true)
	defer app.working.Store(false)
	defer func() {
		if r := recover(); r != nil {
			res.OK, res.Error = false, T("err.unexpected", r)
		}
		res.State = t.state(newProbe())
	}()
	if !app.admin {
		res.Error = T("blocked.admin")
		return
	}
	if on && t.sys && o.RestorePoint && !tweakRPDone.Load() {
		emit("tweak-rp", map[string]string{"state": "running"})
		app.critical.Store(true)
		ok := createRestorePoint()
		app.critical.Store(false)
		emit("tweak-rp", map[string]string{"state": map[bool]string{true: "done", false: "failed"}[ok]})
		if !ok {
			res.Error = T("tw.err.rp")
			return
		}
		tweakRPDone.Store(true)
	}
	opts := map[string]any{"apps": o.Apps}
	var err error
	if on {
		err = applyTweak(t, opts)
	} else {
		err = revertTweak(t, opts)
	}
	if err != nil {
		res.Error = err.Error()
		return
	}
	res.OK, res.Explorer, res.Reboot = true, t.explorer, t.reboot
	return
}

func applyTweak(t *tweak, opts map[string]any) error {
	b := getBackup(t.id)
	c := &tctx{t: t, b: b, opts: opts}
	if b == nil {
		c.b, c.fresh = &tweakBackup{At: time.Now().Unix()}, true
		for _, v := range t.reg {
			c.b.Reg = append(c.b.Reg, snapReg(v))
		}
		if len(t.svc) > 0 {
			if m, err := mgr.Connect(); err == nil {
				for _, s := range t.svc {
					if st, d, ok := svcStart(m, s.name); ok {
						c.b.Svc = append(c.b.Svc, svcSnap{Name: s.name, Start: st, Delayed: d})
					}
				}
				m.Disconnect()
			}
		}
	}
	var errs []string
	for _, v := range t.reg {
		if err := v.write(v.on); err != nil {
			errs = append(errs, v.name+": "+err.Error())
		}
	}
	if len(t.svc) > 0 {
		if m, err := mgr.Connect(); err == nil {
			for _, s := range t.svc {
				if _, _, ok := svcStart(m, s.name); !ok {
					continue // not on this PC
				}
				if err := setSvcStart(m, s.name, s.on, false); err != nil {
					errs = append(errs, s.name+": "+err.Error())
				}
			}
			m.Disconnect()
		}
	}
	var hookErr error
	if t.apply != nil {
		hookErr = t.apply(c)
	}
	if c.fresh && !t.oneWay {
		putBackup(t.id, c.b)
	}
	announce(t.setting)
	if hookErr != nil {
		return hookErr
	}
	if len(errs) > 0 {
		return errors.New(T("tw.err.partial", strings.Join(errs, "; ")))
	}
	return nil
}

func revertTweak(t *tweak, opts map[string]any) error {
	if t.oneWay {
		return errors.New(T("tw.err.oneway"))
	}
	b := getBackup(t.id)
	c := &tctx{t: t, b: b, opts: opts}
	var errs []string
	if b != nil {
		for i := len(b.Reg) - 1; i >= 0; i-- {
			if err := restoreReg(b.Reg[i]); err != nil {
				errs = append(errs, b.Reg[i].Name+": "+err.Error())
			}
		}
		if len(b.Svc) > 0 {
			if m, err := mgr.Connect(); err == nil {
				for _, s := range b.Svc {
					if err := setSvcStart(m, s.Name, s.Start, s.Delayed); err != nil {
						errs = append(errs, s.Name+": "+err.Error())
					}
				}
				m.Disconnect()
			}
		}
	} else {
		for _, v := range t.reg {
			if err := v.write(v.off); err != nil {
				errs = append(errs, v.name+": "+err.Error())
			}
			if v.off == nil && len(v.keys) > 0 {
				dropEmptyKeys(v.root, v.keys)
			}
		}
		if len(t.svc) > 0 {
			if m, err := mgr.Connect(); err == nil {
				for _, s := range t.svc {
					if _, _, ok := svcStart(m, s.name); !ok {
						continue
					}
					if err := setSvcStart(m, s.name, s.off, s.offDelayed); err != nil {
						errs = append(errs, s.name+": "+err.Error())
					}
				}
				m.Disconnect()
			}
		}
	}
	var hookErr error
	if t.revert != nil {
		hookErr = t.revert(c)
	}
	announce(t.setting)
	if hookErr != nil {
		return hookErr
	}
	putBackup(t.id, nil)
	if len(errs) > 0 {
		return errors.New(T("tw.err.partial", strings.Join(errs, "; ")))
	}
	return nil
}

// ---------------------------------------------------------------- Windows helpers

var (
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
	procPostMessageW        = user32.NewProc("PostMessageW")
)

// announce tells running programs that a setting changed, the way the
// Settings app does, so most changes show without signing out.
func announce(area string) {
	if area == "" {
		return
	}
	for _, a := range strings.Split(area, ",") {
		p, _ := windows.UTF16PtrFromString(a)
		var res uintptr
		_, _, _ = procSendMessageTimeoutW.Call(0xffff, 0x001A, 0, uintptr(unsafe.Pointer(p)),
			0x0002, 2000, uintptr(unsafe.Pointer(&res))) // HWND_BROADCAST, WM_SETTINGCHANGE, SMTO_ABORTIFHUNG
	}
}

// spi calls SystemParametersInfo to apply a setting at once.
func spi(action, uiParam uint32, pv unsafe.Pointer) {
	_, _, _ = procSystemParametersInfoW.Call(uintptr(action), uintptr(uiParam), uintptr(pv), 0x02) // SPIF_SENDCHANGE
}

// restartExplorer restarts the taskbar and desktop so taskbar and menu
// tweaks show. Windows starts the shell again by itself; if it does not,
// it is started from here.
func restartExplorer() bool {
	pids := procPIDs(map[string]bool{"explorer.exe": true})
	for pid := range pids {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
	for i := 0; i < 30; i++ {
		time.Sleep(200 * time.Millisecond)
		if len(procPIDs(map[string]bool{"explorer.exe": true})) > 0 {
			return true
		}
	}
	// Start it with normal (not administrator) rights.
	_, _, err := runQuiet(10*time.Second, sys32("runas.exe"), "/trustlevel:0x20000", filepath.Join(P.windir, "explorer.exe"))
	time.Sleep(1500 * time.Millisecond)
	if err != nil || len(procPIDs(map[string]bool{"explorer.exe": true})) == 0 {
		shellOpen(filepath.Join(P.windir, "explorer.exe"))
	}
	return true
}

// psRun runs a PowerShell script and turns a failure into an error that
// carries the script's last output line.
func psRun(timeout time.Duration, script string) error {
	out, code, err := runPS(timeout, script)
	if err == nil && code == 0 {
		return nil
	}
	msg := strings.TrimSpace(out)
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = T("tw.err.code", code)
	}
	return errors.New(msg)
}

// removeApps removes app packages (patterns may end in *) for every user.
func removeApps(patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	q := make([]string, len(patterns))
	for i, p := range patterns {
		q[i] = psQuote(p)
	}
	script := "$ErrorActionPreference='SilentlyContinue'; foreach ($n in @(" + strings.Join(q, ",") + ")) { " +
		"Get-AppxPackage -AllUsers -Name $n | Remove-AppxPackage -AllUsers; " +
		"Get-AppxPackage -Name $n | Remove-AppxPackage }; exit 0"
	return psRun(10*time.Minute, script)
}

func wingetPath() string {
	if p := filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\WindowsApps\winget.exe`); fileExists(p) {
		return p
	}
	return ""
}

// wingetInstall installs a package; if winget is missing it opens the
// fallback page (the Microsoft Store or a download page) instead.
func wingetInstall(fallback string, args ...string) (string, error) {
	w := wingetPath()
	if w == "" {
		if fallback != "" {
			shellOpen(fallback)
			return T("tw.note.store"), nil
		}
		return "", errors.New(T("tw.err.winget"))
	}
	full := append([]string{"install"}, args...)
	full = append(full, "--silent", "--accept-source-agreements", "--accept-package-agreements")
	out, code, err := runQuiet(20*time.Minute, w, full...)
	if err == nil && code == 0 {
		return "", nil
	}
	// "already installed" exits with a non-zero code too
	if strings.Contains(strings.ToLower(out), "already installed") || code == -1978335189 {
		return "", nil
	}
	if fallback != "" {
		shellOpen(fallback)
		return T("tw.note.store"), nil
	}
	return "", errors.New(T("tw.err.code", code))
}
