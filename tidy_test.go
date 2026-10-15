package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeEF map[string][]efEnt

func (f fakeEF) ReadDir(dir string) ([]efEnt, error) {
	e, ok := f[dir]
	if !ok {
		return nil, os.ErrNotExist
	}
	return e, nil
}

func TestFindEmptyDirs(t *testing.T) {
	root := t.TempDir()
	mkd := func(p ...string) { _ = os.MkdirAll(filepath.Join(append([]string{root}, p...)...), 0o755) }
	mkd("a", "b", "c") // a holds only empty folders: a is the one found (with 2 inside)
	mkd("d", "e")      // d holds a file and an empty folder e
	mk(t, filepath.Join(root, "d", "f.txt"), 1, 0)
	mkd("proj", ".git", "refs", "heads") // dot folders are never touched; proj holds one, so it is not empty
	mkd("prot", "x")                     // a protected folder: never proposed, but looked into
	mkd("node_modules", "z")
	mkd("g")
	mkd("Microsoft", "Windows", "x") // skipped: not even looked into
	got := findEmptyDirs(osEfFS{}, root, []string{filepath.Join(root, "prot")}, []string{filepath.Join(root, "Microsoft")}, time.Now().Add(time.Minute), 1000)
	var paths []string
	for _, d := range got {
		paths = append(paths, strings.TrimPrefix(d.Path, root+string(filepath.Separator)))
	}
	want := []string{"a", filepath.Join("d", "e"), "g", filepath.Join("prot", "x")}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("found %v, want %v", paths, want)
	}
	if got[0].Subs != 2 {
		t.Errorf("a has %d empty folders inside, want 2", got[0].Subs)
	}
	// the root alone (empty) is never proposed
	empty := t.TempDir()
	if got := findEmptyDirs(osEfFS{}, empty, nil, nil, time.Now().Add(time.Minute), 10); len(got) != 0 {
		t.Errorf("root proposed: %v", got)
	}
	// links are never followed nor removed; an unreadable folder counts as not empty
	f := fakeEF{
		"R":                        {{Name: "link", Dir: true, Link: true}, {Name: "locked", Dir: true}, {Name: "ok", Dir: true}},
		filepath.Join("R", "ok"):   {},
		filepath.Join("R", "link"): {},
	}
	got2 := findEmptyDirs(f, "R", nil, nil, time.Now().Add(time.Minute), 10)
	if len(got2) != 1 || filepath.Base(got2[0].Path) != "ok" {
		t.Errorf("fake: %+v", got2)
	}
}

func TestShortcutBroken(t *testing.T) {
	exists := func(p string) bool { return p == `C:\Apps\here.exe` }
	drive := func(d string) bool { return d == `C:\` }
	cases := []struct {
		li   lnkInfo
		want bool
	}{
		{lnkInfo{Target: `C:\Apps\gone.exe`}, true},
		{lnkInfo{Target: `c:\Apps\gone.exe`}, true},
		{lnkInfo{Target: `C:\Apps\here.exe`}, false},
		{lnkInfo{Target: `E:\Games\game.exe`}, false},                   // drive not connected
		{lnkInfo{Target: `\\server\share\x.exe`, Network: true}, false}, // network
		{lnkInfo{Special: true}, false},                                 // Store app / Windows Installer
		{lnkInfo{Target: `%ProgramFiles%\x.exe`}, false},                // not expanded: unknown
		{lnkInfo{Target: `shell:AppsFolder\x`}, false},
		{lnkInfo{Target: ``}, false},
	}
	for _, c := range cases {
		if got := shortcutBroken(c.li, exists, drive); got != c.want {
			t.Errorf("%+v: got %v", c.li, got)
		}
	}
}
