package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestDiskScanTotalsLinksAndTop(t *testing.T) {
	root := t.TempDir()
	mk(t, filepath.Join(root, "a", "big.iso"), 5000, 0)
	mk(t, filepath.Join(root, "a", "x.txt"), 100, 0)
	mk(t, filepath.Join(root, "a", "deep", "y.mp4"), 3000, 0)
	mk(t, filepath.Join(root, "b", "z.log"), 700, 0)
	mk(t, filepath.Join(root, "top.bin"), 10, 0)
	linked := os.Symlink(root, filepath.Join(root, "b", "loop")) == nil // a link back to the top: never followed
	if _, err := os.Lstat(filepath.Join(root, "b", "loop")); err != nil {
		linked = false // Wine creates links it does not show again
	}
	var stop atomic.Bool
	st := &scanStats{}
	r, types, exts, top := scanTree(root, listDirFull, 4, &stop, st)
	if r.size != 8810 || r.files != 5 {
		t.Fatalf("totals: %d bytes %d files", r.size, r.files)
	}
	a := r.find(filepath.Join(root, "a"))
	if a == nil || a.size != 8100 || a.files != 3 || a.dirs != 1 {
		t.Fatalf("folder a: %+v", a)
	}
	if n := r.find(filepath.Join(root, "B", "loop")); linked && (n == nil || !n.link || len(n.kids) != 0) {
		t.Fatal("link not marked or followed")
	} else if !linked {
		t.Log("symlinks not permitted here: link part not checked")
	}
	if len(top) != 5 || filepath.Base(top[0].Path) != "big.iso" || top[0].Cat != "diskimages" {
		t.Fatalf("top: %+v", top)
	}
	if types["video"] == nil || types["video"].Size != 3000 || exts[".log"] == nil || exts[".log"].Files != 1 {
		t.Fatalf("types: %+v exts: %+v", types, exts)
	}
	if st.files.Load() != 5 {
		t.Fatalf("progress files %d", st.files.Load())
	}
	// a file deleted and a folder removed update every total above them
	removeFile(a, 100, 100)
	if a.size != 8000 || r.size != 8710 || r.files != 4 {
		t.Fatalf("after file removal: a %d root %d/%d", a.size, r.size, r.files)
	}
	removeNode(r.find(filepath.Join(root, "b")))
	if r.size != 8010 || r.find(filepath.Join(root, "b")) != nil {
		t.Fatalf("after folder removal: %d", r.size)
	}
	// refreshing one folder replaces only that subtree
	mk(t, filepath.Join(root, "a", "new.bin"), 990, 0)
	fresh, _, _, _ := scanTree(filepath.Join(root, "a"), listDirFull, 2, &stop, &scanStats{})
	replaceSubtree(r.find(filepath.Join(root, "a")), fresh)
	if got := r.find(filepath.Join(root, "a")); got == nil || got.size != 9090 || r.size != 9100 {
		t.Fatalf("after refresh: root %d", r.size)
	}
}

func TestDiskScanUnreadableAndCancel(t *testing.T) {
	root := t.TempDir()
	mk(t, filepath.Join(root, "ok", "f"), 10, 0)
	calls := 0
	list := func(p string) ([]dirEnt, error) {
		calls++
		if filepath.Base(p) == "ok" {
			return nil, os.ErrPermission
		}
		return listDirFull(p)
	}
	var stop atomic.Bool
	r, _, _, _ := scanTree(root, list, 3, &stop, &scanStats{})
	ok := r.find(filepath.Join(root, "ok"))
	if ok == nil || !ok.denied || !r.partial || r.files != 0 {
		t.Fatalf("denied folder not marked: %+v", ok)
	}
	// a stop request ends the scan quickly and safely
	for i := 0; i < 200; i++ {
		mk(t, filepath.Join(root, "many", string(rune('a'+i%26))+string(rune('a'+i/26)), "f"), 1, 0)
	}
	stop.Store(true)
	r, _, _, _ = scanTree(root, listDirFull, 4, &stop, &scanStats{})
	if r == nil || r.files > 1 {
		t.Fatalf("cancel: %+v", r)
	}
}

func TestFileTypes(t *testing.T) {
	for name, want := range map[string]string{"a.MP4": "video", "b.7z": "archives", "c.vhdx": "diskimages", "d.unknownext": "other", "noext": "other", "e.LOG": "system"} {
		if _, c := fileType(name); c != want {
			t.Errorf("%s: %s, want %s", name, c, want)
		}
	}
}

// Refreshing the scanned folder itself keeps the tree consistent: later
// deletions still update the totals at the top.
func TestDiskRefreshRootThenDelete(t *testing.T) {
	root := t.TempDir()
	mk(t, filepath.Join(root, "a", "f.bin"), 100, 0)
	mk(t, filepath.Join(root, "b", "g.bin"), 50, 0)
	var stop atomic.Bool
	r, _, _, _ := scanTree(root, listDirFull, 2, &stop, &scanStats{})
	mk(t, filepath.Join(root, "c", "h.bin"), 25, 0)
	fresh, _, _, _ := scanTree(root, listDirFull, 2, &stop, &scanStats{})
	replaceSubtree(r, fresh)
	if r.size != 175 || r.name != root {
		t.Fatalf("after root refresh: %d %q", r.size, r.name)
	}
	removeNode(r.find(filepath.Join(root, "a")))
	if r.size != 75 {
		t.Fatalf("root total not updated after delete: %d", r.size)
	}
}

// The largest-files list is exact even when one folder holds more big files
// than one worker's share.
func TestDiskTopIsExact(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1300; i++ {
		mk(t, filepath.Join(root, "one", fmt.Sprintf("f%04d.bin", i)), i+1, 0)
	}
	mk(t, filepath.Join(root, "two", "x.bin"), 5, 0)
	var stop atomic.Bool
	_, _, _, top := scanTree(root, listDirFull, 4, &stop, &scanStats{})
	if len(top) != 1000 || top[0].Size != 1300 || top[999].Size != 301 {
		t.Fatalf("top: %d entries, first %d, last %d", len(top), top[0].Size, top[len(top)-1].Size)
	}
}
