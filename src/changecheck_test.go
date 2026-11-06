package main

import (
	"reflect"
	"testing"
)

const day = int64(86400)

func findCk(fs []ckFinding, id, subj string) *ckFinding {
	for i := range fs {
		if fs[i].ID == id && (subj == "" || (len(fs[i].Args) > 0 && fs[i].Args[0] == subj)) {
			return &fs[i]
		}
	}
	return nil
}

// Explorer began to crash after the changes: told, with the changes that
// touch the desktop as the suspects, the latest first.
func TestEventsBeganAfterChanges(t *testing.T) {
	now := int64(100 * day)
	changes := []ckRef{
		{Kind: "tweak", ID: "hags", At: now - 10*day},
		{Kind: "tweak", ID: "classic-menu", At: now - 9*day},
		{Kind: "debloat", ID: "DisableBing", At: now - 8*day},
		{Kind: "tweak", ID: "file-ext", At: now - 2*day}, // after the first crash: not a suspect
	}
	var evs []ckEvent
	for _, d := range []int64{7, 6, 5, 1} {
		evs = append(evs, ckEvent{At: now - d*day, Kind: "crash", Subject: "explorer.exe"})
	}
	fs := eventFindings(changes, evs, now)
	f := findCk(fs, "ev-crash", "explorer.exe")
	if f == nil {
		t.Fatalf("not found: %+v", fs)
	}
	if f.Sev != "bad" || f.Count != 4 || f.Before != 0 {
		t.Fatalf("got %+v", f)
	}
	var ids []string
	for _, s := range f.Suspects {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, []string{"DisableBing", "classic-menu"}) {
		t.Fatalf("suspects %v", ids)
	}
}

// As often before the changes as after: not their fault, and only told when
// it keeps happening.
func TestEventsAsBefore(t *testing.T) {
	now := int64(100 * day)
	changes := []ckRef{{Kind: "tweak", ID: "hags", At: now - 10*day}}
	var evs []ckEvent
	for _, d := range []int64{18, 15, 12, 8, 5, 2} {
		evs = append(evs, ckEvent{At: now - d*day, Kind: "hang", Subject: "searchhost.exe"})
	}
	fs := eventFindings(changes, evs, now)
	if f := findCk(fs, "ev-hang", ""); f != nil {
		t.Fatalf("blamed on the changes: %+v", f)
	}
	// two in the last 7 days: under the limit
	if len(fs) != 0 {
		t.Fatalf("got %+v", fs)
	}
}

// One blue screen after the changes is enough to say; the suspects are the
// changes known to touch the system.
func TestBlueScreenAfterChanges(t *testing.T) {
	now := int64(50 * day)
	changes := []ckRef{
		{Kind: "tweak", ID: "dark-mode", At: now - 3*day},
		{Kind: "tweak", ID: "core-isolation-off", At: now - 3*day},
	}
	fs := eventFindings(changes, []ckEvent{{At: now - day, Kind: "bsod"}}, now)
	f := findCk(fs, "ev-bsod", "")
	if f == nil || f.Sev != "bad" || len(f.Suspects) != 1 || f.Suspects[0].ID != "core-isolation-off" {
		t.Fatalf("got %+v", fs)
	}
}

// No change known to touch it: the changes of the week before are the
// suspects. A program needs 3 crashes.
func TestAppCrashesSuspectsByTime(t *testing.T) {
	now := int64(50 * day)
	changes := []ckRef{
		{Kind: "tweak", ID: "dark-mode", At: now - 4*day},
		{Kind: "tweak", ID: "file-ext", At: now - 20*day}, // more than a week before
	}
	evs := []ckEvent{{At: now - 3*day, Kind: "crash", Subject: "game.exe"}, {At: now - 2*day, Kind: "crash", Subject: "game.exe"}}
	if fs := eventFindings(changes, evs, now); len(fs) != 0 {
		t.Fatalf("two crashes of a program: %+v", fs)
	}
	evs = append(evs, ckEvent{At: now - day, Kind: "crash", Subject: "game.exe"})
	fs := eventFindings(changes, evs, now)
	f := findCk(fs, "ev-crash", "game.exe")
	if f == nil || f.Sev != "warn" {
		t.Fatalf("got %+v", fs)
	}
	// game.exe is an "app": the known gaming tweaks are not among the
	// changes, so the ones of the week before count
	if len(f.Suspects) != 1 || f.Suspects[0].ID != "dark-mode" {
		t.Fatalf("suspects %+v", f.Suspects)
	}
}

// Without changes from the app, troubles that keep happening are still told
// (as not caused by them).
func TestEventsWithoutChanges(t *testing.T) {
	now := int64(50 * day)
	evs := []ckEvent{{At: now - day, Kind: "power"}}
	fs := eventFindings(nil, evs, now)
	if f := findCk(fs, "ev-power-old", ""); f == nil || f.Sev != "info" {
		t.Fatalf("got %+v", fs)
	}
}

func TestServiceCauses(t *testing.T) {
	changes := []ckRef{{Kind: "tweak", ID: "svc-manual"}, {Kind: "debloat", ID: "X"}, {Kind: "tweak", ID: "dark-mode"}}
	svcOf := map[string][]string{"tweak:svc-manual": {"UsoSvc", "MapsBroker"}, "debloat:X": {"usosvc"}}
	got := ckServiceCauses("UsoSvc", changes, svcOf)
	if len(got) != 2 || got[0].ID != "svc-manual" || got[1].ID != "X" {
		t.Fatalf("got %+v", got)
	}
	if ckWanted("spooler") == nil || ckWanted("nope") != nil || ckWanted("wuauserv").Def != 3 {
		t.Fatal("ckWanted")
	}
}

func TestSortFindings(t *testing.T) {
	fs := []ckFinding{{ID: "b", Sev: "ok"}, {ID: "z", Sev: "info"}, {ID: "a", Sev: "bad"}, {ID: "c", Sev: "warn"}, {ID: "a", Sev: "ok"}}
	sortCkFindings(fs)
	var got []string
	for _, f := range fs {
		got = append(got, f.Sev+":"+f.ID)
	}
	if !reflect.DeepEqual(got, []string{"bad:a", "warn:c", "info:z", "ok:a", "ok:b"}) {
		t.Fatalf("got %v", got)
	}
}

func TestHostsBlocks(t *testing.T) {
	hosts := "# comment\n127.0.0.1 localhost\n0.0.0.0 fe2.update.microsoft.com # blocked\n0.0.0.0 vortex.data.microsoft.com\n" +
		"10.0.0.1 download.windowsupdate.com\n::  login.live.com\n0.0.0.0 notupdate.microsoft.com.evil\n"
	got := hostsBlockedUpdateDomains(hosts)
	if !reflect.DeepEqual(got, []string{"fe2.update.microsoft.com", "login.live.com"}) {
		t.Fatalf("got %v", got)
	}
}

func TestPagingOff(t *testing.T) {
	if !pagingOff(nil) || !pagingOff([]string{""}) || pagingOff([]string{`?:\pagefile.sys`}) || pagingOff([]string{"C:\\pagefile.sys 0 0"}) {
		t.Fatal("pagingOff")
	}
}

func TestSvcNameOfKey(t *testing.T) {
	for k, want := range map[string]string{
		`HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\DiagTrack`:        "DiagTrack",
		`SYSTEM\CurrentControlSet\Services\dmwappushservice\Parameters`:         "dmwappushservice",
		`HKEY_LOCAL_MACHINE\SOFTWARE\Policies\Microsoft\Windows\DataCollection`: "",
	} {
		if got := svcNameOfKey(k); got != want {
			t.Errorf("%s: %q", k, got)
		}
	}
}

// wevtutil's XML: the events that matter, with their program or service.
func TestParseEvents(t *testing.T) {
	out := `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Application Error'/><EventID>1000</EventID><TimeCreated SystemTime='2026-11-01T10:00:00.1234567Z'/></System><EventData><Data>Explorer.EXE</Data><Data>10.0</Data></EventData></Event>
<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Application Hang'/><EventID Qualifiers='0'>1002</EventID><TimeCreated SystemTime='2026-11-01T11:00:00Z'/></System><EventData><Data>StartMenuExperienceHost.exe</Data></EventData></Event>
<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-Kernel-Power' Guid='{x}'/><EventID>41</EventID><TimeCreated SystemTime='2026-11-02T08:00:00Z'/></System><EventData><Data Name='BugcheckCode'>0</Data></EventData></Event>
<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Service Control Manager' Guid='{y}' EventSourceName='Service Control Manager'/><EventID Qualifiers='49152'>7031</EventID><TimeCreated SystemTime='2026-11-02T09:00:00Z'/></System><EventData><Data Name='param1'>Windows Audio</Data></EventData></Event>
<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Display'/><EventID>4101</EventID><TimeCreated SystemTime='2026-11-02T10:00:00Z'/></System><EventData><Data>nvlddmkm</Data></EventData></Event>
<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Something Else'/><EventID>1000</EventID><TimeCreated SystemTime='2026-11-02T10:00:00Z'/></System></Event>
<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Application Error'/><EventID>1000</EventID><TimeCreated SystemTime='2026-11-02T10:00:00Z'/></System><EventData><Data>xKi4oCleaner.exe</Data></EventData></Event>`
	evs := ckParseEvents(out)
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+"/"+e.Subject)
	}
	want := []string{"crash/explorer.exe", "hang/startmenuexperiencehost.exe", "power/", "svc/Windows Audio", "gpu/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if evs[0].At != 1793527200 {
		t.Fatalf("time %d", evs[0].At)
	}
}
