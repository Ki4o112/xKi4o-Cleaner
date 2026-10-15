package main

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeBytes(t *testing.T, path string, data []byte, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		_ = os.Chtimes(path, old, old)
	}
}

func randBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// testFS uses the real disk, and a folder as the "Recycle Bin".
func testFS(t *testing.T, bin string) finderFS {
	return finderFS{
		list:      listDirFull,
		identity:  fileIdentity,
		protected: func(p string, _ uint32) bool { return strings.Contains(strings.ToLower(p), "protected") },
		recycle: func(paths []string) error {
			for _, p := range paths {
				if err := os.Rename(p, filepath.Join(bin, filepath.Base(p)+"."+time.Now().Format("150405.000000000"))); err != nil {
					return err
				}
			}
			return nil
		},
		inUse: func(string) string { return "" },
		skipDir: func(dir, name string) bool {
			return strings.EqualFold(name, "AppData")
		},
	}
}

func groupPaths(g dupGroup, root string) []string {
	var out []string
	for _, f := range g.Files {
		r, _ := filepath.Rel(root, f.Path)
		out = append(out, filepath.ToSlash(r))
	}
	sort.Strings(out)
	return out
}

func TestFindDupes(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	big := randBytes(1<<20+333, 1)
	writeBytes(t, filepath.Join(root, "Photos", "a.jpg"), big, 48*time.Hour)
	writeBytes(t, filepath.Join(root, "Backup", "a copy.jpg"), big, 0)
	writeBytes(t, filepath.Join(root, "Backup", "deep", "a again.jpg"), big, 0)
	// same size, same start and end, different middle: not a duplicate
	mid := append([]byte(nil), big...)
	mid[len(mid)/2] ^= 0xFF
	writeBytes(t, filepath.Join(root, "Other", "almost.jpg"), mid, 0)
	// a hard link is the same file, not a copy
	if err := os.Link(filepath.Join(root, "Photos", "a.jpg"), filepath.Join(root, "Photos", "hardlink.jpg")); err != nil {
		t.Skip("no hard links here:", err)
	}
	// a symlink is never followed or counted
	_ = os.Symlink(filepath.Join(root, "Photos", "a.jpg"), filepath.Join(root, "Photos", "link.jpg"))
	// small identical documents
	doc := randBytes(10_000, 2)
	writeBytes(t, filepath.Join(root, "Docs", "cv.pdf"), doc, 0)
	writeBytes(t, filepath.Join(root, "Downloads", "cv(1).pdf"), doc, 0)
	// below the minimum size
	writeBytes(t, filepath.Join(root, "Docs", "tiny1.txt"), []byte("same"), 0)
	writeBytes(t, filepath.Join(root, "Docs", "tiny2.txt"), []byte("same"), 0)
	// system folders are left out
	writeBytes(t, filepath.Join(root, "AppData", "cache.bin"), big, 0)
	// an excluded folder
	writeBytes(t, filepath.Join(root, "Skip", "x.jpg"), big, 0)
	// a protected copy is listed but can never be deleted
	writeBytes(t, filepath.Join(root, "protected", "p.pdf"), doc, 0)

	fs := testFS(t, bin)
	o := findOpts{Roots: []string{root, filepath.Join(root, "Docs")}, MinSize: 100, Exclude: []string{filepath.Join(root, "Skip")}}
	var stop atomic.Bool
	st := &findStats{}
	groups := findDupes(o, fs, &stop, st)
	if len(groups) != 2 {
		for _, g := range groups {
			t.Log(groupPaths(g, root))
		}
		t.Fatalf("%d groups, want 2", len(groups))
	}
	g0 := groupPaths(groups[0], root)
	if len(g0) != 3 || g0[0] != "Backup/a copy.jpg" || g0[1] != "Backup/deep/a again.jpg" || !strings.HasPrefix(g0[2], "Photos/") {
		t.Errorf("big group: %v", g0)
	}
	if groups[0].Wasted != int64(len(big))*2 || len(groups[0].Hash) != 64 {
		t.Errorf("wasted %d hash %q", groups[0].Wasted, groups[0].Hash)
	}
	if groups[0].Files[0].Mtime > groups[0].Files[1].Mtime {
		t.Error("files not oldest first")
	}
	g1 := groupPaths(groups[1], root)
	if strings.Join(g1, ",") != "Docs/cv.pdf,Downloads/cv(1).pdf,protected/p.pdf" {
		t.Errorf("doc group: %v", g1)
	}
	if p := st.progress(); p.ToHash == 0 || p.Hashed != p.ToHash || p.Files < 10 {
		t.Errorf("progress %+v", p)
	}

	// kinds of file
	o2 := o
	o2.Cats = []string{"documents"}
	if g := findDupes(o2, fs, &stop, &findStats{}); len(g) != 1 || g[0].Files[0].Cat != "documents" {
		t.Errorf("documents only: %+v", g)
	}

	// deleting: never every copy, never a protected one, never a changed one
	all := []string{filepath.Join(root, "Docs", "cv.pdf"), filepath.Join(root, "Downloads", "cv(1).pdf")}
	ok, refused := planDupDelete(groups, []string{filepath.Join(root, "protected", "p.pdf")}, fs)
	if len(ok) != 0 || len(refused) != 1 || refused[0].Error != "da.err.protected" {
		t.Errorf("protected copy: ok %d refused %+v", len(ok), refused)
	}
	ok, refused = planDupDelete(groups, all, fs) // the protected copy stays as the kept one
	if len(ok) != 2 || len(refused) != 0 {
		t.Errorf("protected keeper: ok %d refused %+v", len(ok), refused)
	}
	ok, refused = planDupDelete(groups, append(all, filepath.Join(root, "protected", "p.pdf")), fs)
	if len(ok) != 0 || len(refused) != 3 || refused[0].Error != "dup.err.keep" {
		t.Errorf("every copy: ok %d refused %+v", len(ok), refused)
	}
	// with the protected copy changed, nothing can be kept: all refused
	writeBytes(t, filepath.Join(root, "protected", "p.pdf"), randBytes(10_000, 9), 0)
	ok, refused = planDupDelete(groups, all, fs)
	if len(ok) != 0 || len(refused) != 2 || refused[0].Error != "dup.err.keep" {
		t.Errorf("no keeper: ok %d refused %+v", len(ok), refused)
	}
	// a file changed since the scan is refused
	writeBytes(t, filepath.Join(root, "Backup", "a copy.jpg"), randBytes(1<<20+333, 5), 0)
	ok, refused = planDupDelete(groups, []string{filepath.Join(root, "Backup", "a copy.jpg"), filepath.Join(root, "nope.jpg")}, fs)
	if len(ok) != 0 || len(refused) != 2 || refused[0].Error != "dup.err.changed" || refused[1].Error != "da.err.unknown" {
		t.Errorf("changed/unknown: %+v", refused)
	}

	// through the job: the deleted copy goes to the bin, the group shrinks
	fjob.dupes = groups
	res := fjob.delete("dupes", []string{filepath.Join(root, "Backup", "deep", "a again.jpg")}, fs)
	if len(res) != 1 || !res[0].OK || exists(filepath.Join(root, "Backup", "deep", "a again.jpg")) {
		t.Fatalf("delete: %+v", res)
	}
	if bin, _ := os.ReadDir(bin); len(bin) != 1 {
		t.Errorf("bin has %d items", len(bin))
	}
	r := fjob.result("dupes")
	if len(r.Dupes) != 2 || len(r.Dupes[0].Files) != 2 {
		t.Errorf("after delete: %+v", r.Dupes)
	}
	if !exists(filepath.Join(root, "Photos", "a.jpg")) {
		t.Fatal("the kept copy is gone")
	}
}

func TestFindLarge(t *testing.T) {
	root := t.TempDir()
	writeBytes(t, filepath.Join(root, "v", "movie.mkv"), make([]byte, 300_000), 400*24*time.Hour)
	writeBytes(t, filepath.Join(root, "v", "clip.mp4"), make([]byte, 200_000), 0)
	writeBytes(t, filepath.Join(root, "iso", "win.iso"), make([]byte, 250_000), 400*24*time.Hour)
	writeBytes(t, filepath.Join(root, "small.txt"), make([]byte, 10), 0)
	writeBytes(t, filepath.Join(root, "AppData", "huge.bin"), make([]byte, 900_000), 0)
	fs := testFS(t, t.TempDir())
	var stop atomic.Bool
	got := findLarge(findOpts{Roots: []string{root}, MinSize: 100_000}, fs, &stop, &findStats{})
	var names []string
	for _, f := range got {
		names = append(names, filepath.Base(f.Path))
	}
	if strings.Join(names, ",") != "movie.mkv,win.iso,clip.mp4" {
		t.Errorf("large: %v", names)
	}
	got = findLarge(findOpts{Roots: []string{root}, MinSize: 100_000, OlderDays: 365, Limit: 1}, fs, &stop, &findStats{})
	if len(got) != 1 || filepath.Base(got[0].Path) != "movie.mkv" {
		t.Errorf("old + limit: %+v", got)
	}
	got = findLarge(findOpts{Roots: []string{root}, MinSize: 100_000, System: true}, fs, &stop, &findStats{})
	if len(got) != 4 || filepath.Base(got[0].Path) != "huge.bin" {
		t.Errorf("with system folders: %+v", got)
	}
	got = findLarge(findOpts{Roots: []string{root}, MinSize: 1, Cats: []string{"diskimages"}}, fs, &stop, &findStats{})
	if len(got) != 1 || got[0].Cat != "diskimages" {
		t.Errorf("disk images: %+v", got)
	}
	// deleting from the list: unknown and changed files are refused
	fjob.large = findLarge(findOpts{Roots: []string{root}, MinSize: 100_000}, fs, &stop, &findStats{})
	writeBytes(t, filepath.Join(root, "v", "clip.mp4"), make([]byte, 200_001), 0)
	res := fjob.delete("large", []string{filepath.Join(root, "v", "movie.mkv"), filepath.Join(root, "v", "clip.mp4"), filepath.Join(root, "small.txt")}, fs)
	okN := 0
	for _, r := range res {
		if r.OK {
			okN++
		}
	}
	if okN != 1 || exists(filepath.Join(root, "v", "movie.mkv")) || !exists(filepath.Join(root, "v", "clip.mp4")) {
		t.Errorf("delete: %+v", res)
	}
	if r := fjob.result("large"); len(r.Large) != 2 {
		t.Errorf("list after delete: %d", len(r.Large))
	}
}

func TestFinderJobAndCancel(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		writeBytes(t, filepath.Join(root, "d", string(rune('a'+i%26)), "f"+string(rune('a'+i/26))+".bin"), bytes.Repeat([]byte{byte(i % 3)}, 5000), 0)
	}
	fs := testFS(t, t.TempDir())
	events := make(chan string, 1000)
	emit := func(name string, _ any) { events <- name }
	if !fjob.start("dupes", findOpts{Roots: []string{root}, MinSize: 1}, fs, emit) {
		t.Fatal("did not start")
	}
	if fjob.start("large", findOpts{Roots: []string{root}}, fs, emit) {
		t.Fatal("a second search started")
	}
	deadline := time.After(20 * time.Second)
	for done := false; !done; {
		select {
		case e := <-events:
			done = e == "find-done"
		case <-deadline:
			t.Fatal("no find-done")
		}
	}
	r := fjob.result("dupes")
	if r.Running || len(r.Dupes) != 3 || r.Wasted == 0 {
		t.Fatalf("result: running %v, %d groups", r.Running, len(r.Dupes))
	}
	// a cancelled search keeps the previous result
	if !fjob.start("dupes", findOpts{Roots: []string{root}, MinSize: 1}, fs, emit) {
		t.Fatal("did not start again")
	}
	fjob.cancel()
	for done := false; !done; {
		select {
		case e := <-events:
			done = e == "find-done"
		case <-time.After(20 * time.Second):
			t.Fatal("no find-done after cancel")
		}
	}
	if r := fjob.result("dupes"); len(r.Dupes) != 3 {
		t.Errorf("previous result lost: %d", len(r.Dupes))
	}
	if fjob.start("bogus", findOpts{}, fs, emit) {
		t.Error("unknown kind started")
	}
}

// Before deleting copies, the kept copy is checked again: small files by
// their whole content, big ones by size, exact time and both ends.
func TestKeeperCheck(t *testing.T) {
	root := t.TempDir()
	data := randBytes(400_000, 7)
	writeBytes(t, filepath.Join(root, "a", "x.bin"), data, 0)
	writeBytes(t, filepath.Join(root, "b", "x.bin"), data, 0)
	fs := testFS(t, t.TempDir())
	var stop atomic.Bool
	for _, limit := range []int64{256 << 20, 1000} { // whole-file check, then the quick one
		fullCheckLimit = limit
		g := findDupes(findOpts{Roots: []string{root}, MinSize: 1}, fs, &stop, &findStats{})
		if len(g) != 1 || g[0].Files[0].part == 0 {
			t.Fatalf("limit %d: %+v", limit, g)
		}
		keep := g[0].Files[0]
		if !sameContent(keep, g[0].Hash) {
			t.Fatalf("limit %d: unchanged keeper refused", limit)
		}
		// the start of the keeper changes, its time is put back: still noticed
		fi, _ := os.Stat(keep.Path)
		changed := append([]byte(nil), data...)
		changed[10] ^= 1
		writeBytes(t, keep.Path, changed, 0)
		_ = os.Chtimes(keep.Path, fi.ModTime(), fi.ModTime())
		if sameContent(keep, g[0].Hash) {
			t.Fatalf("limit %d: changed keeper accepted", limit)
		}
		writeBytes(t, keep.Path, data, 0)
	}
	fullCheckLimit = 256 << 20
}
