package main

// Empty folders and broken shortcuts. Only proposed, never removed without
// the user's choice; removed ones go to the Recycle Bin.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type efEnt struct {
	Name string
	Dir  bool
	Link bool // a link, junction or other reparse point: never followed, never removed
}

type efFS interface {
	ReadDir(dir string) ([]efEnt, error)
}

type emptyDir struct {
	Path string `json:"path"`
	Subs int    `json:"subs"` // empty folders inside it
	Root string `json:"root"` // the place it was found in
}

// folder names never looked into nor removed: tools keep empty folders
// there on purpose (a Git repository breaks without its empty "refs")
var efSkipNames = map[string]bool{"node_modules": true, "__pycache__": true, "system volume information": true, "$recycle.bin": true}

func efSkip(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, ".") || strings.HasPrefix(n, "$") || efSkipNames[n]
}

// findEmptyDirs returns the topmost empty folders under root (a folder
// holding only empty folders is empty). root itself is never returned, nor
// a protected folder or anything holding one (their insides are searched);
// folders in skip are not even looked into.
func findEmptyDirs(fs efFS, root string, protected, skip []string, deadline time.Time, limit int) []emptyDir {
	var out []emptyDir
	isProt := func(p string) bool {
		for _, x := range protected {
			if x != "" && under(x, p) {
				return true
			}
		}
		return false
	}
	skipped := func(p string) bool {
		for _, x := range skip {
			if x != "" && lowerClean(x) == lowerClean(p) {
				return true
			}
		}
		return false
	}
	var walk func(dir string, depth int, top bool) (empty bool, subs int)
	walk = func(dir string, depth int, top bool) (bool, int) {
		if depth > 40 || len(out) >= limit || time.Now().After(deadline) {
			return false, 0
		}
		ents, err := fs.ReadDir(dir)
		if err != nil {
			return false, 0 // unknown: treated as not empty
		}
		empty, subs := true, 0
		type kid struct {
			path string
			subs int
		}
		var kids []kid
		for _, e := range ents {
			if !e.Dir || e.Link || efSkip(e.Name) {
				empty = false
				continue
			}
			p := filepath.Join(dir, e.Name)
			if skipped(p) {
				empty = false
				continue
			}
			if isProt(p) {
				empty = false
				// still look inside: its empty subfolders are found
				walk(p, depth+1, true)
				continue
			}
			ke, ks := walk(p, depth+1, false)
			if ke {
				kids = append(kids, kid{p, ks})
				subs += 1 + ks
			} else {
				empty = false
			}
		}
		if !empty || top {
			for _, k := range kids {
				if len(out) < limit {
					out = append(out, emptyDir{Path: k.path, Subs: k.subs, Root: root})
				}
			}
			return false, 0
		}
		return true, subs
	}
	walk(root, 0, true)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out
}

type osEfFS struct{}

func (osEfFS) ReadDir(dir string) ([]efEnt, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]efEnt, 0, len(ents))
	for _, e := range ents {
		t := e.Type()
		out = append(out, efEnt{Name: e.Name(), Dir: e.IsDir(), Link: t&(os.ModeSymlink|os.ModeIrregular) != 0 || reparseDir(filepath.Join(dir, e.Name()), e)})
	}
	return out, nil
}

type brokenLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
	Where  string `json:"where"` // desktop, startmenu, quicklaunch
}

// shortcutBroken: the shortcut points to a file or folder on a drive that
// is there, and the file or folder is not. Network targets, drives that are
// not connected right now, removable drives and shortcuts with no plain
// path (Store apps, Windows Installer) are never called broken.
func shortcutBroken(li lnkInfo, exists func(string) bool, driveOK func(string) bool) bool {
	t := strings.TrimSpace(li.Target)
	if li.Special || li.Network || t == "" || strings.ContainsAny(t, "%?*") {
		return false
	}
	if len(t) < 3 || t[1] != ':' || (t[2] != '\\' && t[2] != '/') {
		return false
	}
	if !driveOK(strings.ToUpper(t[:1]) + `:\`) {
		return false
	}
	return !exists(t)
}
