//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSkipSystemDir(t *testing.T) {
	win := windowsDir()
	for _, c := range []struct {
		dir, name string
		skip      bool
	}{
		{`C:\`, "Windows", true}, {`C:\`, "Program Files", true}, {`C:\`, "ProgramData", true},
		{`D:\`, "$Recycle.Bin", true}, {`C:\Users\x`, "AppData", true}, {`D:\Games`, "System Volume Information", true},
		{`C:\Users\x`, "Documents", false}, {`D:\Backup`, "Windows", false}, {`D:\`, "Games", false},
		{filepath.Dir(win), filepath.Base(win), true},
	} {
		if got := skipSystemDir(c.dir, c.name); got != c.skip {
			t.Errorf("%s + %s: %v", c.dir, c.name, got)
		}
	}
}

// Hard links are found through the file index; ordinary files have none.
func TestFileIdentity(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	writeBytes(t, a, []byte("hello"), 0)
	if _, ok := fileIdentity(a); ok {
		t.Error("a file with one name reported an identity")
	}
	b := filepath.Join(dir, "b.bin")
	if err := os.Link(a, b); err != nil {
		t.Skip("no hard links here:", err)
	}
	ia, oka := fileIdentity(a)
	ib, okb := fileIdentity(b)
	if !oka || !okb || ia != ib {
		t.Errorf("hard links: %q %v / %q %v", ia, oka, ib, okb)
	}
}

// The whole Windows path of the finder on real files, except deleting.
func TestWinFinderDupes(t *testing.T) {
	initPaths()
	root := t.TempDir()
	data := randBytes(300_000, 3)
	writeBytes(t, filepath.Join(root, "x", "one.jpg"), data, 0)
	writeBytes(t, filepath.Join(root, "y", "two.jpg"), data, 0)
	writeBytes(t, filepath.Join(root, "AppData", "three.jpg"), data, 0)
	var stop atomic.Bool
	g := findDupes(findOpts{Roots: []string{root}, MinSize: 1}, winFinderFS(), &stop, &findStats{})
	if len(g) != 1 || len(g[0].Files) != 2 {
		t.Fatalf("groups: %+v", g)
	}
	for _, f := range g[0].Files {
		if f.Protected || !unchanged(f) {
			t.Errorf("file %+v", f)
		}
	}
	if p := findPlaces(); len(p) == 0 {
		t.Error("no places")
	}
	if b := recycleBinOf(`C:\Users\x\a.txt`); !strings.HasPrefix(strings.ToLower(b), `c:\$recycle.bin\s-1-`) {
		t.Errorf("bin folder %q", b)
	}
}

// Deleting goes to the Recycle Bin of the PC, so it runs only in a
// throw-away Windows (Wine) unless asked for.
func TestWinFinderDelete(t *testing.T) {
	changesThisPC(t)
	initPaths()
	root := t.TempDir()
	data := randBytes(200_000, 4)
	writeBytes(t, filepath.Join(root, "a", "keep.mp4"), data, 0)
	writeBytes(t, filepath.Join(root, "b", "copy.mp4"), data, 0)
	fs := winFinderFS()
	var stop atomic.Bool
	fjob.mu.Lock()
	fjob.dupes = findDupes(findOpts{Roots: []string{root}, MinSize: 1}, fs, &stop, &findStats{})
	fjob.mu.Unlock()
	res := fjob.delete("dupes", []string{filepath.Join(root, "b", "copy.mp4"), filepath.Join(root, "a", "keep.mp4")}, fs)
	if len(res) != 2 {
		t.Fatalf("%+v", res)
	}
	// deleting both copies is refused as a whole
	for _, r := range res {
		if r.OK || r.Error != "dup.err.keep" {
			t.Errorf("both copies: %+v", r)
		}
	}
	res = fjob.delete("dupes", []string{filepath.Join(root, "b", "copy.mp4")}, fs)
	if len(res) != 1 || !res[0].OK || exists(filepath.Join(root, "b", "copy.mp4")) || !exists(filepath.Join(root, "a", "keep.mp4")) {
		t.Fatalf("one copy: %+v", res)
	}
}

func TestCopyText(t *testing.T) {
	changesThisPC(t) // it replaces what is on the clipboard
	if !copyText("xKi4o — тест\nline 2") {
		t.Skip("clipboard not available here")
	}
}
