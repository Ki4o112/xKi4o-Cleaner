//go:build windows

package main

// The Check page's reading of the PC (the logic is in changecheck.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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

var ckRun struct {
	busy atomic.Bool
	mu   sync.Mutex
	last *ckReport
}

func ckLastPath() string { return dataDirPath("check-last.json") }

// checkLast: the report of the last check (also from an earlier run).
func checkLast() *ckReport {
	ckRun.mu.Lock()
	defer ckRun.mu.Unlock()
	if ckRun.last == nil {
		if b, err := os.ReadFile(ckLastPath()); err == nil {
			var r ckReport
			if json.Unmarshal(b, &r) == nil {
				ckRun.last = &r
			}
		}
	}
	return ckRun.last
}

// checkRun starts a check; its steps and the report come as "check" events.
func checkRun() bool {
	if !ckRun.busy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer ckRun.busy.Store(false)
		// a check cut off by a panic still ends on the page (with what it says)
		defer func() {
			if v := recover(); v != nil {
				logEvent("app", "error", "check", T("err.unexpected", v), nil)
				emit("check", map[string]any{"state": "done", "report": &ckReport{At: time.Now().Unix(), Changes: []ckRef{}, Error: fmt.Sprint(v),
					Findings: []ckFinding{{ID: "ck-failed", Sev: "warn", Args: []string{fmt.Sprint(v)}}}}})
			}
		}()
		r := changeCheck(func(step int, text string) {
			emit("check", map[string]any{"state": "step", "step": step, "total": ckSteps, "text": text})
		})
		ckRun.mu.Lock()
		ckRun.last = r
		ckRun.mu.Unlock()
		if b, err := json.Marshal(r); err == nil {
			_ = os.WriteFile(ckLastPath(), b, 0o644)
		}
		emit("check", map[string]any{"state": "done", "report": r})
	}()
	return true
}

const ckSteps = 6

func changeCheck(step func(int, string)) *ckReport {
	t0 := time.Now()
	r := &ckReport{At: t0.Unix()}
	var fs []ckFinding
	add := func(f ckFinding) { fs = append(fs, f) }
	last := t0
	inner := step
	step = func(i int, text string) {
		if i > 0 {
			r.Steps = append(r.Steps, time.Since(last).Milliseconds())
		}
		last = time.Now()
		inner(i, text)
	}

	// the slow reads (PowerShell) start at once and are waited for later
	avc := make(chan avState, 1)
	go func() { defer recoverLog("check av"); avc <- avStateRead() }()
	bpc := make(chan bootPerf, 1)
	go func() { defer recoverLog("check boot"); bpc <- bootPerfRead(false) }()
	rpc := make(chan rpList, 1)
	go func() {
		defer recoverLog("check rp")
		if app.admin {
			rpc <- restorePointList()
		} else {
			rpc <- rpList{Error: "no admin"}
		}
	}()

	// 1. the changes made from the app, and whether they still hold
	step(0, "changes")
	changes, svcOf, ckf := ckChanges()
	fs = append(fs, ckf...)
	r.Changes = changes

	// 2. the services Windows needs
	step(1, "services")
	fs = append(fs, ckServicesCheck(changes, svcOf)...)

	// 3. crashes, hangs, blue screens and failed services
	step(2, "events")
	evs, evErr := ckEvents(30)
	if evErr == "" {
		evf := eventFindings(changes, evs, time.Now().Unix())
		fs = append(fs, evf...)
		fromChanges := false // the ones that keep happening anyway do not count here
		for _, f := range evf {
			fromChanges = fromChanges || !strings.HasSuffix(f.ID, "-old")
		}
		if !fromChanges {
			add(ckFinding{ID: "ok-events", Sev: "ok"})
		}
	} else {
		add(ckFinding{ID: "ev-unread", Sev: "info", Args: []string{evErr}})
	}

	// 4. Windows itself: the desktop, protection, updates
	step(3, "windows")
	fs = append(fs, ckWindows(changes, ckWait(avc, avState{Error: "timeout"}))...)

	// 5. the network
	step(4, "network")
	fs = append(fs, ckNetwork(changes)...)

	// 6. memory, space, restore point
	step(5, "pc")
	fs = append(fs, ckPC(changes, ckWait(rpc, rpList{Error: "timeout"}))...)
	if bp := ckWait(bpc, bootPerf{Error: "timeout"}); bp.Error == "" {
		var boots [][2]int64
		for _, b := range bp.Boots {
			boots = append(boots, [2]int64{b.At, b.Total})
		}
		if f := bootFinding(changes, boots, time.Now().Unix()); f != nil {
			add(*f)
		}
	}

	r.Steps = append(r.Steps, time.Since(last).Milliseconds())
	sortCkFindings(fs)
	r.Findings = fs
	if r.Findings == nil {
		r.Findings = []ckFinding{}
	}
	if r.Changes == nil {
		r.Changes = []ckRef{}
	}
	r.Took = time.Since(t0).Milliseconds()
	return r
}

// ---------------------------------------------------------------- the changes

// lastApplied: when a tweak was last turned on from the app (0 = not known).
func lastApplied(id string) int64 {
	histMu.Lock()
	defer histMu.Unlock()
	loadHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		e := hist[i]
		if e.Tweak == id && e.OK && e.Action == "apply" && !e.Undone {
			return e.At
		}
	}
	return 0
}

// debloatTimes: when each Debloat change was last made or taken back, from
// the app's log (true = made).
func debloatTimes() map[string]struct {
	At   int64
	Made bool
} {
	out := map[string]struct {
		At   int64
		Made bool
	}{}
	l := olog
	l.mu.Lock()
	defer l.mu.Unlock()
	type lists struct{ apply, undo map[string]bool }
	ops := map[int64]lists{}
	set := func(v any) map[string]bool {
		m := map[string]bool{}
		if xs, ok := v.([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok {
					m[s] = true
				}
			}
		}
		if xs, ok := v.([]string); ok {
			for _, s := range xs {
				m[s] = true
			}
		}
		return m
	}
	for _, e := range l.entries {
		if e.Phase == "begin" && e.Kind == "tweak" && e.Data != nil {
			if _, ok := e.Data["apply"]; ok {
				ops[e.ID] = lists{set(e.Data["apply"]), set(e.Data["undo"])}
			}
			continue
		}
		ls, ok := ops[e.Op]
		if !ok || e.Level != "info" || e.Subj == "" {
			continue
		}
		at := e.T / 1000
		if ls.apply[e.Subj] {
			out[e.Subj] = struct {
				At   int64
				Made bool
			}{at, true}
		} else if ls.undo[e.Subj] {
			out[e.Subj] = struct {
				At   int64
				Made bool
			}{at, false}
		}
	}
	return out
}

func ckChanges() ([]ckRef, map[string][]string, []ckFinding) {
	var changes []ckRef
	var fs []ckFinding
	svcOf := map[string][]string{}
	p := newProbe()
	drift := 0
	for _, t := range tweaks {
		if t.legacy || getBackup(t.id) == nil {
			continue
		}
		st := fullState(t, p)
		if st.State == "na" || st.State == "pending" {
			continue
		}
		changes = append(changes, ckRef{Kind: "tweak", ID: t.id, At: lastApplied(t.id)})
		var names []string
		for _, s := range t.svc {
			names = append(names, s.name)
		}
		for _, v := range t.reg {
			if n := svcNameOfKey(v.path); n != "" && strings.EqualFold(v.name, "Start") {
				names = append(names, n)
			}
		}
		svcOf["tweak:"+t.id] = names
		switch {
		case st.Error != "":
			fs = append(fs, ckFinding{ID: "tw-failed", Sev: "warn", Args: []string{t.id, st.Error}, Fix: "page", FixArg: "tweaks"})
			drift++
		case st.Drift:
			fs = append(fs, ckFinding{ID: "tw-drift", Sev: "warn", Args: []string{t.id, st.State}, Fix: "reapply", FixArg: t.id})
			drift++
		case st.Reboot:
			fs = append(fs, ckFinding{ID: "tw-reboot", Sev: "info", Args: []string{t.id}})
		}
	}
	build := buildNum(osBuild())
	r := liveReg{}
	for id, x := range debloatTimes() {
		f := dbFeatureByID(id)
		if f == nil || !x.Made {
			continue
		}
		changes = append(changes, ckRef{Kind: "debloat", ID: id, At: x.At})
		var names []string
		for _, o := range f.Apply {
			if n := svcNameOfKey(o.Key); n != "" && strings.EqualFold(o.Name, "Start") {
				names = append(names, n)
			}
		}
		svcOf["debloat:"+id] = names
		if len(f.Apply) > 0 && f.fits(build) && !regApplied(f.Apply, r) {
			fs = append(fs, ckFinding{ID: "db-drift", Sev: "warn", Args: []string{id}, Fix: "reapply-db", FixArg: id})
			drift++
		}
	}
	if len(changes) > 0 && drift == 0 {
		fs = append(fs, ckFinding{ID: "ok-changes", Sev: "ok", Count: len(changes)})
	}
	return changes, svcOf, fs
}

// ---------------------------------------------------------------- services

func svcRunning(m *mgr.Mgr, name string) (bool, bool) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, false
	}
	h, err := windows.OpenService(m.Handle, p, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(h, &st) != nil {
		return false, false
	}
	return st.CurrentState == windows.SERVICE_RUNNING || st.CurrentState == windows.SERVICE_START_PENDING, true
}

func ckServicesCheck(changes []ckRef, svcOf map[string][]string) []ckFinding {
	m, err := mgrRead()
	if err != nil {
		return nil
	}
	defer m.Disconnect()
	var fs []ckFinding
	bad := 0
	for _, w := range ckServices {
		start, _, ok := svcStart(m, w.Name)
		if !ok {
			continue // not on this PC (an edition without it)
		}
		if start == windows.SERVICE_DISABLED {
			fs = append(fs, ckFinding{ID: "svc-off", Sev: ckSvcSev(w.Area), Args: []string{w.Name, w.Area},
				Suspects: ckServiceCauses(w.Name, changes, svcOf), Fix: "svc", FixArg: w.Name})
			bad++
			continue
		}
		if w.Auto && start == windows.SERVICE_AUTO_START {
			if run, ok := svcRunning(m, w.Name); ok && !run {
				// a moment later: it may be starting right now
				time.Sleep(300 * time.Millisecond)
				if run, ok := svcRunning(m, w.Name); ok && !run {
					fs = append(fs, ckFinding{ID: "svc-stopped", Sev: "warn", Args: []string{w.Name, w.Area}, Fix: "svc", FixArg: w.Name})
					bad++
				}
			}
		}
	}
	if bad == 0 {
		fs = append(fs, ckFinding{ID: "ok-services", Sev: "ok"})
	}
	return fs
}

// checkFixService puts a service Windows needs back to its Windows default
// start type and starts it when it runs all the time.
func checkFixService(name string) map[string]any {
	w := ckWanted(name)
	if w == nil {
		return map[string]any{"ok": false, "error": "unknown service"}
	}
	op := beginOp("tweak", w.Name, T("log.ckSvc", w.Name), nil)
	m, err := mgr.Connect()
	if err != nil {
		op.fail(w.Name, err.Error(), nil)
		op.end("", nil)
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer m.Disconnect()
	if start, _, ok := svcStart(m, w.Name); ok && start == windows.SERVICE_DISABLED {
		if err := setSvcStart(m, w.Name, w.Def, w.Delayed); err != nil {
			op.fail(w.Name, err.Error(), nil)
			op.end("", nil)
			return map[string]any{"ok": false, "error": err.Error()}
		}
	}
	if w.Auto || w.Def == windows.SERVICE_AUTO_START {
		if s, err := m.OpenService(w.Name); err == nil {
			_ = s.Start()
			s.Close()
		}
	}
	op.end("", nil)
	return map[string]any{"ok": true}
}

// ---------------------------------------------------------------- events

// ckEvents reads the last days of the System and Application logs.
func ckEvents(days int) ([]ckEvent, string) {
	ms := strconv.Itoa(days * 86400000)
	q := []struct{ log, ids string }{
		{"System", "EventID=41 or EventID=1001 or EventID=4101 or EventID=7000 or EventID=7023 or EventID=7024 or EventID=7031 or EventID=7034"},
		{"Application", "EventID=1000 or EventID=1002"},
	}
	var evs []ckEvent
	errText := ""
	for _, x := range q {
		query := "*[System[(" + x.ids + ") and TimeCreated[timediff(@SystemTime) <= " + ms + "]]]"
		out, code, err := runQuiet(45*time.Second, sys32("wevtutil.exe"), "qe", x.log, "/q:"+query, "/f:xml", "/rd:true", "/c:3000")
		if err != nil || code != 0 {
			errText = strings.TrimSpace(out)
			continue
		}
		evs = append(evs, ckParseEvents(out)...)
	}
	return evs, errText
}

// ---------------------------------------------------------------- Windows

func ckApplied(changes []ckRef, id string) bool {
	for _, c := range changes {
		if c.Kind == "tweak" && c.ID == id {
			return true
		}
	}
	return false
}

func ckTweakRefs(changes []ckRef, ids ...string) []ckRef {
	var out []ckRef
	for _, c := range changes {
		for _, id := range ids {
			if c.Kind == "tweak" && c.ID == id {
				out = append(out, c)
			}
		}
	}
	return out
}

func regDword(root registry.Key, path, name string) (uint64, bool) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return v, err == nil
}

func regKeyExists(root registry.Key, path string) bool {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// the changes (Debloat ones included) whose registry operations set a value
func ckSetters(changes []ckRef, key, name string) []ckRef {
	var out []ckRef
	for _, c := range changes {
		if c.Kind != "debloat" {
			continue
		}
		f := dbFeatureByID(c.ID)
		if f == nil {
			continue
		}
		for _, o := range f.Apply {
			if strings.EqualFold(strings.TrimPrefix(o.Key, `HKEY_LOCAL_MACHINE\`), key) && strings.EqualFold(o.Name, name) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// ckWait: what a slow read gave, or the fallback after 40 seconds.
func ckWait[T any](c chan T, fallback T) T {
	select {
	case v := <-c:
		return v
	case <-time.After(40 * time.Second):
		return fallback
	}
}

func ckWindows(changes []ckRef, av avState) []ckFinding {
	var fs []ckFinding
	// the desktop: Explorer runs
	shellOK := false
	if raw, err := queryProcesses(); err == nil {
		for _, p := range raw {
			if strings.EqualFold(p.name, "explorer.exe") {
				shellOK = true
				break
			}
		}
		if shellOK {
			fs = append(fs, ckFinding{ID: "ok-shell", Sev: "ok"})
		} else {
			fs = append(fs, ckFinding{ID: "shell-down", Sev: "bad", Fix: "explorer"})
		}
	}
	// protection
	switch {
	case av.Error != "" && !av.On && len(av.Other) == 0:
		// Defender could not be asked (removed, or a policy): nothing to say
	case !av.On && len(av.Other) == 0:
		fs = append(fs, ckFinding{ID: "av-off", Sev: "bad", Suspects: ckTweakRefs(changes, "defender-rtp-off"), Fix: "page", FixArg: "av"})
	case av.On && !av.RealT && len(av.Other) == 0:
		fs = append(fs, ckFinding{ID: "av-rt-off", Sev: "warn", Suspects: ckTweakRefs(changes, "defender-rtp-off"), Fix: "page", FixArg: "av"})
	case av.On && av.SigAt > 0 && time.Since(time.UnixMilli(av.SigAt)) > 7*24*time.Hour:
		days := int(time.Since(time.UnixMilli(av.SigAt)).Hours() / 24)
		fs = append(fs, ckFinding{ID: "av-old", Sev: "warn", Args: []string{strconv.Itoa(days)}, Suspects: ckTweakRefs(changes, "svc-manual"), Fix: "page", FixArg: "wupd"})
	default:
		fs = append(fs, ckFinding{ID: "ok-av", Sev: "ok"})
	}
	// updates: no policy that stops them
	wuOK := true
	for _, name := range []string{"NoAutoUpdate", "DisableWindowsUpdateAccess", "DoNotConnectToWindowsUpdateInternetLocations"} {
		path := ckUpdatePolicies[name]
		if v, ok := regDword(registry.LOCAL_MACHINE, path, name); ok && v == 1 {
			fs = append(fs, ckFinding{ID: "wu-blocked", Sev: "warn", Args: []string{name}, Suspects: ckSetters(changes, path, name), Fix: "wu-policy", FixArg: name})
			wuOK = false
		}
	}
	if blocked := hostsBlocks(); len(blocked) > 0 {
		fs = append(fs, ckFinding{ID: "hosts-block", Sev: "warn", Args: blocked, Fix: "hosts"})
		wuOK = false
	}
	if wuOK {
		fs = append(fs, ckFinding{ID: "ok-updates", Sev: "ok"})
	}
	// Insider builds need the diagnostic data that telemetry-off turns off
	if ckApplied(changes, "telemetry-off") {
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\WindowsSelfHost\Applicability`, registry.QUERY_VALUE); err == nil {
			if b, _, err := k.GetStringValue("BranchName"); err == nil && b != "" {
				fs = append(fs, ckFinding{ID: "insider-telemetry", Sev: "warn", Suspects: ckTweakRefs(changes, "telemetry-off")})
			}
			k.Close()
		}
	}
	// Game Pass games need the Xbox app and Gaming Services
	if ckApplied(changes, "gaming-apps-off") {
		for _, d := range []string{`C:\XboxGames`, filepath.Join(os.Getenv("SystemDrive")+`\`, "XboxGames")} {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				fs = append(fs, ckFinding{ID: "xbox-games", Sev: "warn", Suspects: ckTweakRefs(changes, "gaming-apps-off")})
				break
			}
		}
	}
	// printing: the spooler off while printers are installed
	if m, err := mgrRead(); err == nil {
		if start, _, ok := svcStart(m, "Spooler"); ok && start == windows.SERVICE_DISABLED && realPrinters() > 0 {
			fs = append(fs, ckFinding{ID: "print-off", Sev: "warn", Suspects: ckTweakRefs(changes, "printer-manual"), Fix: "svc", FixArg: "Spooler"})
		}
		m.Disconnect()
	}
	// the language indicator by the clock (with two keyboard languages or more)
	if f := ckInput(); f != nil {
		fs = append(fs, *f)
	}
	// a restart Windows waits for
	if regKeyExists(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) ||
		regKeyExists(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		fs = append(fs, ckFinding{ID: "reboot-pending", Sev: "info", Fix: "reboot"})
	}
	return fs
}

// realPrinters: printers other than the ones Windows itself puts there.
func realPrinters() int {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Print\Printers`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return 0
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	n := 0
	for _, p := range names {
		l := strings.ToLower(p)
		if strings.HasPrefix(l, "microsoft ") || strings.HasPrefix(l, "onenote") || l == "fax" || strings.Contains(l, "pdf") {
			continue
		}
		n++
	}
	return n
}

// hostsBlocks: Microsoft update and Store addresses the hosts file sends nowhere.
func hostsBlocks() []string {
	b, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), `System32\drivers\etc\hosts`))
	if err != nil {
		return nil
	}
	return hostsBlockedUpdateDomains(string(b))
}

// ---------------------------------------------------------------- network

func ckNetwork(changes []ckRef) []ckFinding {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, dnsErr := net.DefaultResolver.LookupHost(ctx, "www.microsoft.com")
	if dnsErr == nil {
		return []ckFinding{{ID: "ok-net", Sev: "ok"}}
	}
	// is there a connection at all? (a public DNS server's port)
	d := net.Dialer{Timeout: 3 * time.Second}
	if c, err := d.Dial("tcp", "1.1.1.1:53"); err == nil {
		c.Close()
		return []ckFinding{{ID: "net-dns", Sev: "warn", Suspects: ckTweakRefs(changes, "net-throttle-off", "net-opt", "wifi-sense-off"), Fix: "page", FixArg: "network"}}
	}
	return []ckFinding{{ID: "net-offline", Sev: "info"}}
}

// ---------------------------------------------------------------- the PC

var procGlobalMemStatus = kernel32.NewProc("GlobalMemoryStatusEx")

func ckPC(changes []ckRef, rp rpList) []ckFinding {
	var fs []ckFinding
	// the page file
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management`, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetStringsValue("PagingFiles")
		k.Close()
		if err == nil && pagingOff(v) {
			ms := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
			_, _, _ = procGlobalMemStatus.Call(uintptr(unsafe.Pointer(&ms)))
			gb := int(ms.totalPhys >> 30)
			sev := "warn"
			if gb >= 32 {
				sev = "info"
			}
			fs = append(fs, ckFinding{ID: "pagefile-off", Sev: sev, Args: []string{strconv.Itoa(gb)}, Fix: "page", FixArg: "optimize"})
		} else {
			fs = append(fs, ckFinding{ID: "ok-pagefile", Sev: "ok"})
		}
	}
	// space on the system drive
	sys := os.Getenv("SystemDrive") + `\`
	var free, total, totalFree uint64
	if p, err := windows.UTF16PtrFromString(sys); err == nil && windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) == nil && total > 0 {
		if free < 10<<30 || free*10 < total {
			fs = append(fs, ckFinding{ID: "low-space", Sev: "warn", Args: []string{sizeText(int64(free))}, Fix: "page", FixArg: "clean"})
		} else {
			fs = append(fs, ckFinding{ID: "ok-space", Sev: "ok", Args: []string{sizeText(int64(free))}})
		}
	}
	// a way back: a restore point from the last 30 days (only with changes)
	if len(changes) > 0 && app.admin {
		if rp.Error == "" {
			recent := false
			for _, p := range rp.Points {
				if t, err := time.Parse(time.RFC3339, p.Time); err == nil && time.Since(t) < 30*24*time.Hour {
					recent = true
					break
				}
			}
			if recent {
				fs = append(fs, ckFinding{ID: "ok-rp", Sev: "ok"})
			} else {
				fs = append(fs, ckFinding{ID: "no-rp", Sev: "info", Fix: "rp"})
			}
		}
	}
	return fs
}

// checkFixPolicy takes away one of the policies that stop Windows Update.
func checkFixPolicy(name string) map[string]any {
	path, ok := ckUpdatePolicies[name]
	if !ok {
		return map[string]any{"ok": false, "error": "unknown policy"}
	}
	op := beginOp("tweak", name, T("log.ckPolicy", name), nil)
	defer op.end("", nil)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		op.fail(name, err.Error(), nil)
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
		op.fail(name, err.Error(), nil)
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

// checkFixHosts takes the lines that block the update and Store servers out
// of the hosts file; the file as it was is kept next to it first.
func checkFixHosts() map[string]any {
	p := filepath.Join(os.Getenv("SystemRoot"), `System32\drivers\etc\hosts`)
	op := beginOp("tweak", "hosts", T("log.ckHosts"), nil)
	defer op.end("", nil)
	b, err := os.ReadFile(p)
	if err != nil {
		op.fail("hosts", err.Error(), nil)
		return map[string]any{"ok": false, "error": err.Error()}
	}
	nb, changed := hostsWithoutBlocks(string(b))
	if !changed {
		return map[string]any{"ok": true}
	}
	backup := p + ".xki4o-" + time.Now().Format("20060102-150405")
	if err := os.WriteFile(backup, b, 0o644); err != nil {
		op.fail("hosts", err.Error(), nil)
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if err := os.WriteFile(p, []byte(nb), 0o644); err != nil {
		op.fail("hosts", err.Error(), nil)
		return map[string]any{"ok": false, "error": err.Error()}
	}
	op.info("hosts", T("log.ckHostsBackup", backup), nil)
	_, _, _ = runQuiet(15*time.Second, sys32("ipconfig.exe"), "/flushdns")
	return map[string]any{"ok": true, "backup": backup}
}

const ctfTask = `\Microsoft\Windows\TextServicesFramework\MsCtfMonitor`

// keyboardLayouts: how many keyboard languages this user has.
func keyboardLayouts() int {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Keyboard Layout\Preload`, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	return len(names)
}

// ctfTaskDisabled: the task that starts ctfmon.exe at sign-in is turned off.
func ctfTaskDisabled() bool {
	out, code, err := runQuiet(15*time.Second, sys32("schtasks.exe"), "/Query", "/TN", ctfTask, "/FO", "CSV", "/NH")
	return err == nil && code == 0 && ctfTaskOff(out)
}

// ckInput: the language indicator is gone (ctfmon.exe not running, its task
// turned off, or the language bar hidden) while there are 2 languages or more.
func ckInput() *ckFinding {
	if keyboardLayouts() < 2 {
		return nil
	}
	running := false
	if raw, err := queryProcesses(); err == nil {
		for _, p := range raw {
			if strings.EqualFold(p.name, "ctfmon.exe") {
				running = true
				break
			}
		}
	}
	hidden := false
	if v, ok := regDwordCU(`Software\Microsoft\CTF\LangBar`, "ShowStatus"); ok && v == 3 {
		hidden = true
	}
	task := ctfTaskDisabled()
	if running && !hidden && !task {
		return &ckFinding{ID: "ok-lang", Sev: "ok"}
	}
	why := "notrunning"
	switch {
	case task:
		why = "task"
	case hidden:
		why = "hidden"
	}
	return &ckFinding{ID: "lang-gone", Sev: "warn", Args: []string{why}, Fix: "lang"}
}

func regDwordCU(path, name string) (uint64, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return v, err == nil
}

// checkFixLang brings the language indicator back: the language bar back in
// the taskbar, the task that starts ctfmon.exe turned on, and started now (as
// the signed-in user, not with the app's administrator rights).
func checkFixLang() map[string]any {
	op := beginOp("tweak", "ctfmon", T("log.ckLang"), nil)
	defer op.end("", nil)
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\CTF\LangBar`, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if v, _, err := k.GetIntegerValue("ShowStatus"); err == nil && v == 3 {
			_ = k.SetDWordValue("ShowStatus", 4) // docked in the taskbar
		}
		k.Close()
	}
	if ctfTaskDisabled() {
		if out, code, err := runQuiet(20*time.Second, sys32("schtasks.exe"), "/Change", "/TN", ctfTask, "/Enable"); err != nil || code != 0 {
			op.fail("ctfmon", strings.TrimSpace(out), nil)
			return map[string]any{"ok": false, "error": strings.TrimSpace(out)}
		}
	}
	if out, code, err := runQuiet(20*time.Second, sys32("schtasks.exe"), "/Run", "/TN", ctfTask); err != nil || code != 0 {
		op.fail("ctfmon", strings.TrimSpace(out), nil)
		return map[string]any{"ok": false, "error": strings.TrimSpace(out)}
	}
	return map[string]any{"ok": true}
}
