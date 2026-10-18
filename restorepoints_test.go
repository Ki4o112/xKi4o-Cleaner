package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestorePointDeletePlan(t *testing.T) {
	pts := []restorePoint{{Seq: 39, Desc: "driver"}, {Seq: 41, Desc: "xKi4o Cleaner", Time: "2026-10-14T10:00:00Z"}, {Seq: 40, Desc: "Windows Update"}}
	all := rpDeletePlan(pts, false)
	if len(all) != 3 || all[0].Seq != 41 || all[2].Seq != 39 {
		t.Fatalf("all: %+v", all)
	}
	keep := rpDeletePlan(pts, true)
	if len(keep) != 2 || keep[0].Seq != 40 || keep[1].Seq != 39 {
		t.Fatalf("keep newest: %+v", keep)
	}
	if len(rpDeletePlan(nil, true)) != 0 || len(rpDeletePlan(pts[:1], true)) != 0 {
		t.Error("nothing to delete when only the kept one exists")
	}
	if pts[0].Seq != 39 {
		t.Error("the caller's list was reordered")
	}
}

// Only the app's own points beyond the newest three are planned for removal.
func TestRestorePointTrimPlan(t *testing.T) {
	pts := []restorePoint{{Seq: 10, Desc: "xKi4o Cleaner"}, {Seq: 11, Desc: "Windows Update"}, {Seq: 12, Desc: "xKi4o Cleaner"},
		{Seq: 13, Desc: "Installed NVIDIA Graphics Driver"}, {Seq: 14, Desc: "xKi4o Cleaner"}, {Seq: 15, Desc: "xKi4o Cleaner"}, {Seq: 16, Desc: "xKi4o Cleaner"}}
	plan := rpTrimPlan(pts, 3)
	if len(plan) != 2 || plan[0].Seq != 12 || plan[1].Seq != 10 {
		t.Fatalf("plan %+v", plan)
	}
	if len(rpTrimPlan(pts[:3], 3)) != 0 || len(rpTrimPlan(pts, 0)) != 0 || len(rpTrimPlan(nil, 3)) != 0 {
		t.Error("nothing to remove")
	}
	for _, p := range rpTrimPlan(pts, 1) {
		if p.Desc != "xKi4o Cleaner" {
			t.Errorf("not the app's: %+v", p)
		}
	}
}

func TestRemoveRestorePointsReportsEach(t *testing.T) {
	logInit(t.TempDir())
	defer logInit(t.TempDir())
	pts := []restorePoint{{Seq: 7, Desc: "a"}, {Seq: 8, Desc: "b"}, {Seq: 9, Desc: "c"}}
	var asked []int
	op := beginOp("restore", "", "Deleting restore points", nil)
	res := removeRestorePoints(pts, true, func(seq int) error {
		asked = append(asked, seq)
		if seq == 7 {
			return errors.New("refused")
		}
		return nil
	}, op)
	op.end("", nil)
	if len(asked) != 2 || asked[0] != 8 || asked[1] != 7 {
		t.Fatalf("asked %v (the newest, 9, must be kept)", asked)
	}
	if res.Total != 3 || res.Deleted != 1 || res.Failed != 1 || res.Kept != 1 {
		t.Fatalf("%+v", res)
	}
	es := logEntries(op.id)
	if len(es) != 4 || es[1].Level != "info" || es[2].Level != "error" || es[2].Subj != "a" {
		t.Fatalf("log: %+v", es)
	}
	if v := logOps(logFilter{}); v.Ops[0].State != "error" {
		t.Errorf("state %s", v.Ops[0].State)
	}
}

// No test may delete the restore points of the PC it runs on: the tests only
// use removeRestorePoints with a stand-in for the Windows call.
func TestTestsNeverDeleteRestorePoints(t *testing.T) {
	files, _ := filepath.Glob("*_test.go")
	if len(files) == 0 {
		t.Skip("the source files are not next to this test program")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, call := range []string{"srRemove(", "deleteRestorePoints(", "SRRemoveRestorePoint"} {
			if f != "restorepoints_test.go" && strings.Contains(src, call) {
				t.Errorf("%s calls %s", f, call)
			}
		}
	}
}
