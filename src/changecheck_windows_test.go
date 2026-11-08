//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// The whole check on this PC: it finishes in time, reads the event logs and
// the services, and its report goes to the page as JSON.
func TestChangeCheckRuns(t *testing.T) {
	if underWine() {
		t.Skip("Wine has no event logs")
	}
	buildTweaks()
	start := time.Now()
	var steps []string
	r := changeCheck(func(i int, s string) { steps = append(steps, s) })
	took := time.Since(start)
	t.Logf("took %s, %d changes, %d findings, steps (ms) %v", took, len(r.Changes), len(r.Findings), r.Steps)
	for _, f := range r.Findings {
		t.Logf("  %s %s %v x%d suspects=%v fix=%s %s", f.Sev, f.ID, f.Args, f.Count, f.Suspects, f.Fix, f.FixArg)
	}
	if len(steps) != ckSteps {
		t.Errorf("steps %v", steps)
	}
	if took > 45*time.Second {
		t.Errorf("too slow: %s", took)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, f := range r.Findings {
		has[f.ID] = true
	}
	for _, id := range []string{"ok-shell"} {
		if !has[id] {
			t.Errorf("no %s", id)
		}
	}
	if !has["ok-services"] && !has["svc-off"] && !has["svc-stopped"] {
		t.Error("the services were not checked")
	}
	if !has["ok-space"] && !has["low-space"] {
		t.Error("the space was not checked")
	}
}

// The event logs are read and parsed (the runner has some Service Control
// Manager entries at least, or none at all, but never an error).
func TestCheckEvents(t *testing.T) {
	if underWine() {
		t.Skip("Wine has no event logs")
	}
	evs, errText := ckEvents(30)
	if errText != "" {
		t.Fatalf("wevtutil: %s", errText)
	}
	kinds := map[string]int{}
	for _, e := range evs {
		kinds[e.Kind]++
	}
	t.Logf("%d events: %v", len(evs), kinds)
}

// A service Windows needs, turned off: the check tells it, and its button
// puts it back to the Windows default.
func TestCheckFindsDisabledService(t *testing.T) {
	changesThisPC(t)
	const name = "W32Time"
	m, err := mgr.Connect()
	if err != nil {
		t.Skip(err)
	}
	defer m.Disconnect()
	start0, delayed0, ok := svcStart(m, name)
	if !ok {
		t.Skip("no " + name)
	}
	defer func() { _ = setSvcStart(m, name, start0, delayed0) }()
	if err := setSvcStart(m, name, windows.SERVICE_DISABLED, false); err != nil {
		t.Fatal(err)
	}
	fs := ckServicesCheck(nil, nil)
	f := findCk(fs, "svc-off", name)
	if f == nil || f.Sev != "info" || f.Fix != "svc" || f.FixArg != name {
		t.Fatalf("got %+v", fs)
	}
	if res := checkFixService(name); res["ok"] != true {
		t.Fatalf("fix: %v", res)
	}
	if st, _, _ := svcStart(m, name); st != windows.SERVICE_DEMAND_START {
		t.Fatalf("after the fix: start %d", st)
	}
	if findCk(ckServicesCheck(nil, nil), "svc-off", name) != nil {
		t.Fatal("still told after the fix")
	}
}

// A tweak the app turned on that Windows (or something else) turned off
// again: told as changed back, with the button that applies it again.
func TestCheckFindsDrift(t *testing.T) {
	changesThisPC(t)
	buildTweaks()
	was := app.admin
	app.admin = isElevated()
	defer func() { app.admin = was }()
	if !app.admin {
		t.Skip("needs administrator rights")
	}
	const id = "menu-delay"
	res := setTweak(id, true, tweakOpts{})
	if !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	defer setTweak(id, false, tweakOpts{UseDefaults: true})
	_, _, fs := ckChanges()
	if findCk(fs, "tw-drift", id) != nil {
		t.Fatalf("drift right after applying: %+v", fs)
	}
	// something else puts the Windows value back
	tw := findTweak(id)
	if err := tw.reg[0].write(tw.reg[0].off); err != nil {
		t.Fatal(err)
	}
	changes, _, fs := ckChanges()
	f := findCk(fs, "tw-drift", id)
	if f == nil || f.Fix != "reapply" || f.FixArg != id {
		t.Fatalf("got %+v", fs)
	}
	found := false
	for _, c := range changes {
		if c.Kind == "tweak" && c.ID == id && c.At > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the change and its time: %+v", changes)
	}
}

// Debloat changes are found from the app's log: when they were made, and,
// when the registry no longer matches, as changed back.
func TestCheckDebloatFromLog(t *testing.T) {
	var f *dbFeature
	r := liveReg{}
	for i := range debloatData().Features {
		x := &debloatData().Features[i]
		if len(x.Apply) > 0 && x.Custom == "" && !regApplied(x.Apply, r) {
			f = x
			break
		}
	}
	if f == nil {
		t.Skip("every change is applied here")
	}
	op := beginOp("tweak", "", "test", map[string]any{"apply": []string{f.ID}, "undo": []string{}})
	op.info(f.ID, "done", nil)
	op.end("", nil)
	x, ok := debloatTimes()[f.ID]
	if !ok || !x.Made || time.Since(time.Unix(x.At, 0)) > time.Minute {
		t.Fatalf("got %+v %v", x, ok)
	}
	changes, _, fs := ckChanges()
	if c := findCk(fs, "db-drift", f.ID); c == nil || c.Fix != "reapply-db" {
		t.Fatalf("no drift for %s: %+v", f.ID, fs)
	}
	found := false
	for _, c := range changes {
		found = found || (c.Kind == "debloat" && c.ID == f.ID)
	}
	if !found {
		t.Fatal("not among the changes")
	}
	// taken back from the app: no longer a change
	op = beginOp("tweak", "", "test", map[string]any{"apply": []string{}, "undo": []string{f.ID}})
	op.info(f.ID, "undone", nil)
	op.end("", nil)
	if x := debloatTimes()[f.ID]; x.Made {
		t.Fatal("still made after the undo")
	}
}

// The tweaks of 4.21: each turns on, reads as on, turns off and leaves the
// PC as it was (the registry values and the service start types).
func TestTweaks421OnOff(t *testing.T) {
	changesThisPC(t)
	buildTweaks()
	was := app.admin
	app.admin = isElevated()
	defer func() { app.admin = was }()
	if !app.admin {
		t.Skip("needs administrator rights")
	}
	ids := []string{"mpo-off", "power-throttling-off", "sysmain-off", "ntfs-8dot3-off", "wu-drivers-off",
		"storage-sense", "snap-assist-off", "alttab-no-tabs", "this-pc-desktop"}
	for _, id := range ids {
		tw := findTweak(id)
		if tw == nil {
			t.Errorf("%s: no such tweak", id)
			continue
		}
		before := map[string]string{}
		for _, v := range tw.reg {
			before[v.path+`\`+v.name] = fmt.Sprint(v.read())
		}
		p := newProbe()
		if st := fullState(tw, p); st.State == "na" {
			t.Logf("%s: not on this PC (%s)", id, st.Note)
			continue
		} else if st.State == "on" {
			t.Logf("%s: already on here, skipped", id)
			continue
		}
		res := setTweak(id, true, tweakOpts{})
		if !res.OK || res.State.State != "on" {
			t.Errorf("%s on: %+v", id, res)
			continue
		}
		res = setTweak(id, false, tweakOpts{})
		if !res.OK || res.State.State != "off" {
			t.Errorf("%s off: %+v", id, res)
			continue
		}
		for _, v := range tw.reg {
			if got := fmt.Sprint(v.read()); got != before[v.path+`\`+v.name] {
				t.Errorf("%s: %s is %s after off, was %s", id, v.name, got, before[v.path+`\`+v.name])
			}
		}
		if getBackup(id) != nil {
			t.Errorf("%s: the backup is still there", id)
		}
	}
}

// A policy that stops Windows Update: told, and its button takes it away.
func TestCheckFixPolicy(t *testing.T) {
	changesThisPC(t)
	const name = "NoAutoUpdate"
	path := ckUpdatePolicies[name]
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		t.Skip(err)
	}
	old, _, oldErr := k.GetIntegerValue(name)
	defer func() {
		if oldErr == nil {
			_ = k.SetDWordValue(name, uint32(old))
		} else {
			_ = k.DeleteValue(name)
		}
		k.Close()
	}()
	if err := k.SetDWordValue(name, 1); err != nil {
		t.Fatal(err)
	}
	fs := ckWindows(nil, avState{Error: "skip"})
	if f := findCk(fs, "wu-blocked", name); f == nil || f.Fix != "wu-policy" {
		t.Fatalf("not told: %+v", fs)
	}
	if r := checkFixPolicy(name); r["ok"] != true {
		t.Fatalf("fix: %v", r)
	}
	if _, _, err := k.GetIntegerValue(name); err != registry.ErrNotExist {
		t.Fatalf("still there: %v", err)
	}
	if r := checkFixPolicy("SomethingElse"); r["ok"] == true {
		t.Fatal("an unknown policy was accepted")
	}
}

// The hosts file blocks an update server: told, and cleaned with a backup;
// the other lines stay.
func TestCheckFixHosts(t *testing.T) {
	changesThisPC(t)
	p := filepath.Join(os.Getenv("SystemRoot"), `System32\drivers\etc\hosts`)
	orig, err := os.ReadFile(p)
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = os.WriteFile(p, orig, 0o644) }()
	test := string(orig) + "\r\n0.0.0.0 fe2.update.microsoft.com\r\n0.0.0.0 tracker.example.com\r\n"
	if err := os.WriteFile(p, []byte(test), 0o644); err != nil {
		t.Skip(err)
	}
	if b := hostsBlocks(); len(b) != 1 || b[0] != "fe2.update.microsoft.com" {
		t.Fatalf("blocks %v", b)
	}
	r := checkFixHosts()
	if r["ok"] != true {
		t.Fatalf("fix: %v", r)
	}
	backup, _ := r["backup"].(string)
	defer os.Remove(backup)
	if b, err := os.ReadFile(backup); err != nil || string(b) != test {
		t.Fatalf("backup: %v", err)
	}
	now, _ := os.ReadFile(p)
	if len(hostsBlockedUpdateDomains(string(now))) != 0 || !strings.Contains(string(now), "tracker.example.com") {
		t.Fatalf("after: %q", now)
	}
}

// The task that starts ctfmon.exe (the language indicator) turned off: seen,
// and the fix turns it on again and starts it.
func TestCheckFixLang(t *testing.T) {
	changesThisPC(t)
	if out, code, err := runQuiet(15*time.Second, sys32("schtasks.exe"), "/Query", "/TN", ctfTask); err != nil || code != 0 {
		t.Skip("no MsCtfMonitor task here: " + out)
	}
	was := ctfTaskDisabled()
	defer func() {
		flag := "/Enable"
		if was {
			flag = "/Disable"
		}
		_, _, _ = runQuiet(15*time.Second, sys32("schtasks.exe"), "/Change", "/TN", ctfTask, flag)
	}()
	if out, code, err := runQuiet(15*time.Second, sys32("schtasks.exe"), "/Change", "/TN", ctfTask, "/Disable"); err != nil || code != 0 {
		t.Fatalf("disable: %s", out)
	}
	if !ctfTaskDisabled() {
		t.Fatal("the turned-off task is not seen")
	}
	if r := checkFixLang(); r["ok"] != true {
		t.Fatalf("fix: %v", r)
	}
	if ctfTaskDisabled() {
		t.Fatal("still off after the fix")
	}
	t.Logf("keyboard languages here: %d, the indicator check says %+v", keyboardLayouts(), ckInput())
}
