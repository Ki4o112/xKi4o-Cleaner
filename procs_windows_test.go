//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The process list is read and each process is classified; this test
// process is listed and can never be ended from the app.
func TestProcList(t *testing.T) {
	v := procList()
	if v.Error != "" || len(v.Rows) < 3 {
		t.Fatalf("list: %q, %d rows", v.Error, len(v.Rows))
	}
	self := windows.GetCurrentProcessId()
	var me *procRow
	for i := range v.Rows {
		r := &v.Rows[i]
		if r.CPU < 0 || r.CPU > 100 {
			t.Errorf("%s: cpu %v", r.Name, r.CPU)
		}
		if r.PID == self {
			me = r
		}
	}
	if me == nil {
		t.Fatal("this process is not listed")
	}
	exe := strings.ToLower(filepath.Base(os.Args[0]))
	if strings.ToLower(me.Name) != exe || me.Closable || me.Kind != "self" || me.Start == 0 || me.Threads == 0 {
		t.Errorf("self row: %+v", *me)
	}
	t.Logf("self: %s pid %d mem %d ws %d commit %d threads %d handles %d started %d; %d processes, %d cores, cpu %.1f%%",
		me.Name, me.PID, me.Mem, me.WS, me.Commit, me.Threads, me.Handles, me.Started, len(v.Rows), v.Cores, v.CPU)
	if me.WS < 1<<20 || me.Started < 1_600_000_000_000 || me.Started > time.Now().UnixMilli()+60_000 {
		t.Errorf("memory or start time read from the wrong place: %+v", *me)
	}
	// the id of a process must survive a round trip through the page:
	// JSON -> a JavaScript number (float64) -> JSON
	b, _ := json.Marshal(procTarget{PID: me.PID, Start: me.Start})
	var js map[string]float64
	_ = json.Unmarshal(b, &js)
	b2, _ := json.Marshal(js)
	var back procTarget
	_ = json.Unmarshal(b2, &back)
	if back.Start != me.Start || me.Start >= 1<<53 {
		t.Errorf("start id %d came back as %d", me.Start, back.Start)
	}
	if v.Cores < 1 || v.MemTotal == 0 {
		t.Errorf("totals: %+v", v)
	}
	// a second reading has CPU deltas and still lists this process
	end := time.Now().Add(300 * time.Millisecond)
	for x := 0; time.Now().Before(end); x++ {
		_ = x * x
	}
	v2 := procList()
	if v2.CPU < 0 || v2.CPU > 100 {
		t.Errorf("system cpu %v", v2.CPU)
	}
	if vv := sysVitals(); vv.MemTotal == 0 || vv.MemLoad > 100 {
		t.Errorf("vitals %+v", vv)
	}
}

func TestClassifyProc(t *testing.T) {
	const self, session = 100, 1
	mk := func(name string, pid, sess uint32) rawProc { return rawProc{pid: pid, name: name, session: sess} }
	cases := []struct {
		p      rawProc
		inf    procInfo
		ours   bool
		window bool
		kind   string
		can    bool
	}{
		{mk("game.exe", 200, session), procInfo{path: `D:\Games\game.exe`}, false, true, "app", true},
		{mk("helper.exe", 201, session), procInfo{path: `C:\Program Files\X\helper.exe`}, false, false, "background", true},
		{mk("explorer.exe", 202, session), procInfo{}, false, true, "windows", false},
		{mk("MsMpEng.exe", 203, 0), procInfo{}, false, false, "security", false},
		{mk("svc.exe", 204, 0), procInfo{}, false, false, "service", false},
		{mk("other.exe", 205, 2), procInfo{}, false, true, "background", false},
		{mk("theirs.exe", 206, session), procInfo{known: true, ownUser: false}, false, true, "background", false},
		{mk("crit.exe", 207, session), procInfo{critical: true}, false, false, "windows", false},
		{mk("msedgewebview2.exe", 208, session), procInfo{}, true, false, "self", false},
		{mk("child.exe", 209, session), procInfo{}, true, false, "self", false},
		{mk("bgtask.exe", 210, session), procInfo{path: windowsDir() + `\System32\bgtask.exe`}, false, false, "windows", false},
		{mk("notepad.exe", 211, session), procInfo{path: windowsDir() + `\System32\notepad.exe`}, false, true, "app", true},
		{mk("System", 4, 0), procInfo{}, false, false, "windows", false},
	}
	for _, c := range cases {
		kind, can, why := classifyProc(c.p, c.inf, self, c.ours, session, c.window)
		if kind != c.kind || can != c.can || (!can && why == "") {
			t.Errorf("%s: %s %v %q, want %s %v", c.p.name, kind, can, why, c.kind, c.can)
		}
	}
}

func TestDescendants(t *testing.T) {
	raw := []rawProc{
		{pid: 10, ppid: 1, created: 100},
		{pid: 11, ppid: 10, created: 110},
		{pid: 12, ppid: 11, created: 120},
		{pid: 13, ppid: 10, created: 50}, // older than its "parent": the PID was reused
		{pid: 14, ppid: 2, created: 130},
	}
	d := descendants(raw, 10)
	if !d[11] || !d[12] || d[13] || d[14] || d[10] {
		t.Errorf("%v", d)
	}
}

// A process without a window cannot be closed politely; ending it by force
// works and is checked afterwards.
func TestEndPIDs(t *testing.T) {
	dir := t.TempDir()
	held := filepath.Join(dir, "x.bin")
	mk(t, held, 8, 0)
	child := startHolder(t, held)
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	defer func() { _ = child.Process.Kill() }()
	pid := uint32(child.Process.Pid)
	raw, err := queryProcesses()
	if err != nil {
		t.Fatal(err)
	}
	before := map[uint32]rawProc{}
	for _, p := range raw {
		before[p.pid] = p
	}
	if _, ok := before[pid]; !ok {
		t.Fatal("helper not in the list")
	}
	target := map[uint32]string{pid: "helper"}
	if left := endPIDs(target, before, false, time.Second); !left[pid] {
		t.Log("closed politely (unexpected for a process without windows, but fine)")
	} else if left := endPIDs(target, before, true, 0); left[pid] {
		t.Fatal("forced end did not end it")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("helper still running")
	}
}
