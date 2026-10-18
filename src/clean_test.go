package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

// A dry run reports exactly what a real run then removes, and deletes nothing.
func TestDryRunMatchesRealRun(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "app", "Cache")
	mk(t, filepath.Join(cache, "a.bin"), 1000, 0)
	mk(t, filepath.Join(cache, "sub", "b.bin"), 500, 0)
	mk(t, filepath.Join(cache, "sub", "deep", "c.bin"), 250, 0)
	ts := []target{{Path: cache, Kind: kContents}}

	dry := cleanWith(ts, &cleaner{dry: true})
	if dry.bytes != 1750 || dry.files != 3 {
		t.Fatalf("dry run: %d bytes / %d files, want 1750 / 3", dry.bytes, dry.files)
	}
	for _, f := range []string{"a.bin", "sub/b.bin", "sub/deep/c.bin"} {
		if !exists(filepath.Join(cache, f)) {
			t.Fatalf("dry run deleted %s", f)
		}
	}
	real := cleanWith(ts, &cleaner{})
	if real.bytes != dry.bytes || real.files != dry.files {
		t.Fatalf("real run %d/%d differs from dry run %d/%d", real.bytes, real.files, dry.bytes, dry.files)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("cache not empty: %v", entries)
	}
}

// Files and folders the user unticked stay, with everything below them.
func TestExclusions(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	mk(t, filepath.Join(cache, "go.bin"), 100, 0)
	mk(t, filepath.Join(cache, "keep.bin"), 200, 0)
	mk(t, filepath.Join(cache, "KeepDir", "x.bin"), 300, 0)
	mk(t, filepath.Join(cache, "mixed", "go2.bin"), 10, 0)
	mk(t, filepath.Join(cache, "mixed", "keep2.bin"), 20, 0)
	ex := map[string]bool{}
	for _, p := range []string{filepath.Join(cache, "keep.bin"), filepath.Join(cache, "keepdir"), filepath.Join(cache, "mixed", "keep2.bin")} {
		ex[normPath(p)] = true // case-insensitive, like Windows
	}
	st := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{exclude: ex})
	if st.bytes != 110 || st.files != 2 {
		t.Fatalf("cleaned %d bytes / %d files, want 110 / 2", st.bytes, st.files)
	}
	for _, f := range []string{"keep.bin", "KeepDir/x.bin", "mixed/keep2.bin"} {
		if !exists(filepath.Join(cache, f)) {
			t.Errorf("excluded %s was deleted", f)
		}
	}
	for _, f := range []string{"go.bin", "mixed/go2.bin"} {
		if exists(filepath.Join(cache, f)) {
			t.Errorf("%s was not deleted", f)
		}
	}
}

// A folder without permission is reported as denied; the rest is cleaned
// and the run does not stop or retry.
func TestDeniedIsReportedAndSkipped(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root POSIX user to make a folder unwritable")
	}
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	mk(t, filepath.Join(cache, "free.bin"), 100, 0)
	mk(t, filepath.Join(cache, "ro", "stuck.bin"), 50, 0)
	if err := os.Chmod(filepath.Join(cache, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(cache, "ro"), 0o755)
	st := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{})
	if st.bytes != 100 || st.files != 1 {
		t.Fatalf("cleaned %d/%d, want 100/1", st.bytes, st.files)
	}
	if st.denied != 1 || st.deniedBytes != 50 || len(st.issues) != 1 || st.issues[0].Kind != "denied" {
		t.Fatalf("denied not reported: %+v", st)
	}
}

// Missing folders are not an error: nothing to do, nothing reported.
func TestMissingFolders(t *testing.T) {
	root := t.TempDir()
	st := cleanWith([]target{
		{Path: filepath.Join(root, "nope", "Cache"), Kind: kContents},
		{Path: filepath.Join(root, "nope", "file.dmp"), Kind: kWhole},
		{Path: filepath.Join(root, "nope", "Logs"), Kind: kGlob, Pattern: "*.log"},
	}, &cleaner{})
	if st.bytes != 0 || st.files != 0 || st.failed != 0 || st.denied != 0 || len(st.issues) != 0 {
		t.Fatalf("unexpected result for missing folders: %+v", st)
	}
	if e, total, files, more := previewTargets([]target{{Path: filepath.Join(root, "nope"), Kind: kContents}}, 10); len(e) != 0 || total != 0 || files != 0 || more != 0 {
		t.Fatal("preview of a missing folder is not empty")
	}
}

// A stop request ends the run between files; what is left stays intact.
func TestStopBetweenFiles(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	for i := 0; i < 20; i++ {
		mk(t, filepath.Join(cache, "d", string(rune('a'+i))+".bin"), 10, 0)
	}
	var n atomic.Int32
	st := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{stop: func() bool { return n.Add(1) > 6 }})
	if st.files == 0 || st.files >= 20 {
		t.Fatalf("stop not honoured: %d files deleted", st.files)
	}
	left, _ := os.ReadDir(filepath.Join(cache, "d"))
	if len(left)+st.files != 20 {
		t.Fatalf("%d left + %d deleted != 20", len(left), st.files)
	}
}

func TestPreviewLargestFirst(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	mk(t, filepath.Join(cache, "small.bin"), 10, 0)
	mk(t, filepath.Join(cache, "big", "a.bin"), 600, 0)
	mk(t, filepath.Join(cache, "big", "b.bin"), 400, 0)
	mk(t, filepath.Join(cache, "mid.bin"), 300, 0)
	e, total, files, more := previewTargets([]target{{Path: cache, Kind: kContents}}, 2)
	if total != 1310 || files != 4 || more != 1 || len(e) != 2 {
		t.Fatalf("preview: total %d files %d more %d entries %d", total, files, more, len(e))
	}
	if filepath.Base(e[0].Path) != "big" || !e[0].Dir || e[0].Size != 1000 || e[0].Files != 2 || filepath.Base(e[1].Path) != "mid.bin" {
		t.Fatalf("order/contents wrong: %+v", e)
	}
}

func TestIssuesCapped(t *testing.T) {
	var st stats
	for i := 0; i < maxIssues+50; i++ {
		st.problem("locked", "x", 1)
	}
	st.problem("gone", "y", 5) // vanished meanwhile: not a problem
	if st.locked != maxIssues+50 || st.lockedBytes != int64(maxIssues+50) || len(st.issues) != maxIssues {
		t.Fatalf("counts %d/%d issues %d", st.locked, st.lockedBytes, len(st.issues))
	}
	var sum stats
	sum.add(st)
	sum.add(st)
	if sum.locked != 2*(maxIssues+50) || len(sum.issues) != maxIssues {
		t.Fatalf("add: %d issues %d", sum.locked, len(sum.issues))
	}
}

// Many entries go through the parallel path; the result must match a dry run.
func TestParallelClean(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	for i := 0; i < 60; i++ {
		mk(t, filepath.Join(cache, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)), "f.bin"), 100+i, 0)
	}
	ex := map[string]bool{normPath(filepath.Join(cache, "dab")): true}
	dry := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{dry: true, exclude: ex})
	st := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{exclude: ex})
	if st.files != 59 || st.bytes != dry.bytes || dry.files != 59 {
		t.Fatalf("parallel: %+v vs dry %+v", st, dry)
	}
	left, _ := os.ReadDir(cache)
	if len(left) != 1 || left[0].Name() != "dab" {
		t.Fatalf("left: %v", left)
	}
}

// Targets that overlap (the same folder twice, or files inside a folder that
// another target removes whole) are counted once, in the size, the preview,
// the dry run and the real run.
func TestOverlappingTargetsCountOnce(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "app", "Cache")
	mk(t, filepath.Join(cache, "a.tmp"), 1000, 0)
	mk(t, filepath.Join(cache, "sub", "b.tmp"), 500, 0)
	mk(t, filepath.Join(cache, "sub", "c.log"), 250, 0)
	ts := []target{
		{Path: cache, Kind: kContents},
		{Path: cache, Kind: kContents},
		{Path: filepath.Join(cache, "sub"), Kind: kGlob, Pattern: "*.tmp"},
		{Path: cache, Kind: kGlob, Pattern: "*.tmp", Recurse: true},
	}
	if b, n := sizeTargets(ts); b != 1750 || n != 3 {
		t.Fatalf("size %d / %d, want 1750 / 3", b, n)
	}
	if _, total, files, _ := previewTargets(ts, 0); total != 1750 || files != 3 {
		t.Fatalf("preview %d / %d, want 1750 / 3", total, files)
	}
	if dry := cleanWith(ts, &cleaner{dry: true}); dry.bytes != 1750 || dry.files != 3 {
		t.Fatalf("dry run %d / %d, want 1750 / 3", dry.bytes, dry.files)
	}
	if st := cleanWith(ts, &cleaner{}); st.bytes != 1750 || st.files != 3 || st.failed+st.locked+st.denied != 0 {
		t.Fatalf("real run %+v", st)
	}
}

// A part of a folder that is out of reach (USOShared\Logs\System belongs to
// SYSTEM alone) is left out; the rest is cleaned, the folders themselves stay.
func TestContentsExcept(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "Logs")
	mk(t, filepath.Join(logs, "top.log"), 10, 0)
	mk(t, filepath.Join(logs, "System", "a.etl"), 20, 0)
	mk(t, filepath.Join(logs, "User", "b.etl"), 30, 0)
	mk(t, filepath.Join(logs, "User", "deep", "c.etl"), 40, 0)
	ts := contentsExcept(logs, "SYSTEM")
	if len(ts) != 2 {
		t.Fatalf("targets: %+v", ts)
	}
	st := cleanWith(ts, &cleaner{})
	if st.files != 3 || st.bytes != 80 {
		t.Fatalf("cleaned %d files, %d bytes", st.files, st.bytes)
	}
	for _, p := range []string{filepath.Join(logs, "System", "a.etl"), filepath.Join(logs, "User")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if contentsExcept(filepath.Join(root, "missing"), "System") != nil {
		t.Error("a missing folder gives targets")
	}
}
