package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpLogStatesAndReload(t *testing.T) {
	dir := t.TempDir()
	logInit(dir)
	defer logInit(t.TempDir())

	ok := beginOp("clean", "", "Cleaning", map[string]any{"n": 2})
	ok.info("temp", "done", map[string]any{"freed": 100})
	ok.end("freed 100", nil)

	warned := beginOp("tweak", "menu-delay", "Apply", nil)
	warned.warnf("menu-delay", "needs a restart", nil)
	warned.end("applied", nil)

	failed := beginOp("disk", "", "Delete", nil)
	failed.fail(`C:\x`, "in use", nil)
	failed.end("0 of 1", nil)

	cut := beginOp("files", "", "Delete duplicates", nil) // never ended: the app "crashed"
	cut.info("", "step", nil)

	logEvent("app", "info", "", "started", nil)
	var nilOp *opHandle
	nilOp.info("", "ignored", nil) // a nil handle does nothing
	nilOp.end("", nil)

	v := logOps(logFilter{})
	if len(v.Ops) != 4 {
		t.Fatalf("ops: %+v", v.Ops)
	}
	want := map[string]string{"clean": "ok", "tweak": "warn", "disk": "error", "files": "running"}
	for _, s := range v.Ops {
		if want[s.Kind] != s.State {
			t.Errorf("%s: state %s, want %s", s.Kind, s.State, want[s.Kind])
		}
	}
	if v.Ops[0].Kind != "files" {
		t.Errorf("not newest first: %+v", v.Ops)
	}
	if es := logEntries(ok.id); len(es) != 3 || es[0].Phase != "begin" || es[2].Phase != "end" || es[1].Subj != "temp" {
		t.Errorf("entries: %+v", es)
	}
	if p := logOps(logFilter{State: "problems"}); len(p.Ops) != 2 {
		t.Errorf("problems: %+v", p.Ops)
	}
	if q := logOps(logFilter{Query: "DUPLIC"}); len(q.Ops) != 1 {
		t.Errorf("query: %+v", q.Ops)
	}

	// a new start reads the file back: the unfinished operation was cut off
	logInit(dir)
	v = logOps(logFilter{})
	if len(v.Ops) != 4 || v.Cut != 1 || v.Errors != 1 {
		t.Fatalf("after reload: %+v", v)
	}
	for _, s := range v.Ops {
		if s.Kind == "files" && s.State != "cut" {
			t.Errorf("unfinished operation: %s", s.State)
		}
		if s.Kind == "clean" && (s.Summary != "freed 100" || s.Steps != 1) {
			t.Errorf("clean summary: %+v", s)
		}
	}
	// ids stay unique and increasing after a reload
	next := beginOp("clean", "", "again", nil)
	if next.id <= v.Ops[0].ID {
		t.Errorf("id %d not after %d", next.id, v.Ops[0].ID)
	}
	next.end("", nil)

	if !logClear() {
		t.Fatal("clear failed")
	}
	if v := logOps(logFilter{}); len(v.Ops) != 0 {
		t.Errorf("after clear: %+v", v.Ops)
	}
	if _, err := os.Stat(filepath.Join(dir, "log.jsonl")); !os.IsNotExist(err) {
		t.Errorf("log file still there: %v", err)
	}
}

func TestOpLogRotation(t *testing.T) {
	dir := t.TempDir()
	logInit(dir)
	defer logInit(t.TempDir())
	big := strings.Repeat("x", 1000)
	op := beginOp("clean", "", "big", nil)
	for i := 0; i < 4500; i++ {
		op.info("", big, nil)
	}
	op.end("", nil)
	if _, err := os.Stat(filepath.Join(dir, "log.1.jsonl")); err != nil {
		t.Fatalf("not rotated: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "log.jsonl"))
	if err != nil || fi.Size() > logMaxBytes+4096 {
		t.Fatalf("current log: %v %v", fi, err)
	}
	// memory is capped, and the operation's summary survives the trimming
	if n := len(olog.entries); n > logKeep+500 {
		t.Errorf("%d entries kept", n)
	}
	logInit(dir)
	if len(olog.entries) > logKeep {
		t.Errorf("reload keeps %d", len(olog.entries))
	}
}

func TestPanicIsLoggedWithStack(t *testing.T) {
	logInit(t.TempDir())
	defer logInit(t.TempDir())
	op := beginOp("clean", "", "x", nil)
	func() {
		defer func() {
			if r := recover(); r != nil {
				op.panicked(r)
			}
		}()
		var m map[string]int
		m["boom"] = 1
	}()
	op.end("", nil)
	es := logEntries(op.id)
	if len(es) != 3 || es[1].Level != "error" || !strings.Contains(es[1].Data["stack"].(string), "TestPanicIsLoggedWithStack") {
		t.Fatalf("%+v", es)
	}
	if v := logOps(logFilter{}); v.Ops[0].State != "error" {
		t.Errorf("state %s", v.Ops[0].State)
	}
}

func TestLogClientAndMask(t *testing.T) {
	logInit(t.TempDir())
	defer logInit(t.TempDir())
	logClient("error", "TypeError: x is undefined", "at render (app:12)")
	es := logEntries(0)
	if len(es) != 1 || es[0].Kind != "ui" || es[0].Level != "error" {
		t.Fatalf("%+v", es)
	}
	if p := logRecentProblems(5); len(p) != 1 {
		t.Errorf("problems %+v", p)
	}
	in := `C:\Users\Иван\AppData\x and c:\users\иван\Desktop`
	out := maskProfile(in, `C:\Users\Иван`)
	if out != `%USERPROFILE%\AppData\x and %USERPROFILE%\Desktop` {
		t.Errorf("mask: %s", out)
	}
	if maskProfile("abc", "") != "abc" {
		t.Error("empty profile")
	}
}

func TestOpEndHook(t *testing.T) {
	logInit(t.TempDir())
	defer logInit(t.TempDir())
	var got []opSummary
	onOpEnd = func(s opSummary) { got = append(got, s) }
	defer func() { onOpEnd = nil }()
	op := beginOp("restore", "", "Restore", nil)
	op.fail("x", "nope", nil)
	op.end("done", nil)
	op.end("twice", nil) // ending again does nothing
	if len(got) != 1 || got[0].State != "error" || got[0].Summary != "done" || got[0].Errors != 1 {
		t.Fatalf("%+v", got)
	}
}

// A panic in a background worker is logged and does not stop the program.
func TestRecoverLog(t *testing.T) {
	logInit(t.TempDir())
	defer logInit(t.TempDir())
	done := make(chan bool)
	go func() {
		defer func() { done <- true }()
		defer recoverLog("worker")
		var s []int
		_ = s[3]
	}()
	<-done
	es := logEntries(0)
	if len(es) != 1 || es[0].Level != "error" || es[0].Subj != "worker" || !strings.Contains(es[0].Data["stack"].(string), "TestRecoverLog") {
		t.Fatalf("%+v", es)
	}
	// and the cleaning workers survive a bad entry: the other files still go
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		mk(t, filepath.Join(root, "c", string(rune('a'+i))+".tmp"), 10, 0)
	}
	st := cleanWith([]target{{Path: filepath.Join(root, "c"), Kind: kContents}}, &cleaner{})
	if st.files != 12 {
		t.Fatalf("cleaned %d", st.files)
	}
}
