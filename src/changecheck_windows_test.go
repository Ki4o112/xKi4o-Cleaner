//go:build windows

package main

import (
	"encoding/json"
	"testing"
	"time"

	"golang.org/x/sys/windows"
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
