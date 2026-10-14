package main

// File engine shared by scanning and cleaning.
//
// Safety rules enforced here, for every target:
//   - a target path must be absolute and at least two folders deep
//     (never a drive root such as C:\ or a top folder such as C:\Windows)
//   - a target path may never be one of the protected roots (Windows,
//     System32, the user profile, AppData, Program Files, ...)
//   - links, junctions and other reparse points are never followed:
//     the link itself is removed, whatever it points to is left alone
//   - files that are in use simply stay; nothing is forced

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type kind int

const (
	kContents   kind = iota // everything inside Path
	kOlder                  // items inside Path last changed over 24 h ago; "Low" kept
	kGlob                   // files matching Pattern inside Path (Recurse: also below)
	kKeepNewest             // in every sub-folder of Path the newest item stays, older (>24 h) go
	kWhole                  // Path itself
)

const ageLimit = 24 * time.Hour

type target struct {
	Path    string
	Kind    kind
	Pattern string
	Recurse bool
}

type planned struct {
	path string
	info fs.FileInfo
}

type stats struct {
	bytes  int64
	files  int
	failed int
}

// protectedRoots is filled at start-up with folders that must never be a
// cleaning target themselves (their sub-folders can be).
var protectedRoots = map[string]bool{}

func protect(paths ...string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		protectedRoots[normPath(p)] = true
	}
}

func normPath(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

func isSep(r rune) bool { return r == '/' || r == '\\' }

// safeTarget reports whether p may be used as a cleaning target.
func safeTarget(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	clean := filepath.Clean(p)
	vol := filepath.VolumeName(clean)
	parts := strings.FieldsFunc(clean[len(vol):], isSep)
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == ".." {
			return false
		}
	}
	return !protectedRoots[normPath(clean)]
}

// plan lists what a target would remove right now.
func (t target) plan(now time.Time) []planned {
	if !safeTarget(t.Path) {
		return nil
	}
	cutoff := now.Add(-ageLimit)
	switch t.Kind {
	case kWhole:
		if fi, err := os.Lstat(t.Path); err == nil {
			return []planned{{t.Path, fi}}
		}
		return nil

	case kContents, kOlder:
		dir, ok := realDir(t.Path)
		if !ok {
			return nil
		}
		var out []planned
		for _, e := range readDir(dir) {
			if t.Kind == kOlder {
				if strings.EqualFold(e.info.Name(), "Low") || !e.info.ModTime().Before(cutoff) {
					continue
				}
			}
			out = append(out, e)
		}
		return out

	case kGlob:
		dir, ok := realDir(t.Path)
		if !ok {
			return nil
		}
		var out []planned
		globWalk(dir, strings.ToLower(t.Pattern), t.Recurse, &out)
		return out

	case kKeepNewest:
		dir, ok := realDir(t.Path)
		if !ok {
			return nil
		}
		var out []planned
		for _, sub := range readDir(dir) {
			if isLink(sub.info) || !sub.info.IsDir() {
				continue
			}
			items := readDir(sub.path)
			sort.Slice(items, func(i, j int) bool {
				return items[i].info.ModTime().After(items[j].info.ModTime())
			})
			for i, it := range items {
				if i == 0 || !it.info.ModTime().Before(cutoff) {
					continue
				}
				out = append(out, it)
			}
		}
		return out
	}
	return nil
}

// realDir returns p if it is an existing, real (non-link) directory.
func realDir(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil || isLink(fi) || !fi.IsDir() {
		return "", false
	}
	return p, true
}

func readDir(dir string) []planned {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]planned, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, planned{filepath.Join(dir, e.Name()), info})
	}
	return out
}

func globWalk(dir, pattern string, recurse bool, out *[]planned) {
	for _, e := range readDir(dir) {
		if isLink(e.info) {
			continue
		}
		if e.info.IsDir() {
			if recurse {
				globWalk(e.path, pattern, true, out)
			}
			continue
		}
		if ok, _ := filepath.Match(pattern, strings.ToLower(e.info.Name())); ok {
			*out = append(*out, e)
		}
	}
}

// sizeOf returns bytes and file count below an entry, links not followed.
func sizeOf(p planned) (int64, int) {
	if isLink(p.info) {
		return 0, 0
	}
	if !p.info.IsDir() {
		return p.info.Size(), 1
	}
	var b int64
	var n int
	for _, e := range readDir(p.path) {
		bb, nn := sizeOf(e)
		b += bb
		n += nn
	}
	return b, n
}

func sizeTargets(ts []target) (int64, int) {
	now := time.Now()
	var b int64
	var n int
	for _, t := range ts {
		for _, p := range t.plan(now) {
			bb, nn := sizeOf(p)
			b += bb
			n += nn
		}
	}
	return b, n
}

// remove deletes an entry. A link is removed as a link; a folder is
// emptied first and then removed if it became empty.
func remove(p planned, st *stats) {
	if isLink(p.info) {
		if removeOne(p.path) == nil {
			st.files++
		} else {
			st.failed++
		}
		return
	}
	if p.info.IsDir() {
		for _, e := range readDir(p.path) {
			remove(e, st)
		}
		_ = removeOne(p.path)
		return
	}
	size := p.info.Size()
	if removeOne(p.path) == nil {
		st.bytes += size
		st.files++
	} else {
		st.failed++
	}
}

func cleanTargets(ts []target, stop func() bool) stats {
	var st stats
	now := time.Now()
	for _, t := range ts {
		if stop != nil && stop() {
			break
		}
		for _, p := range t.plan(now) {
			remove(p, &st)
		}
	}
	return st
}

// subDirs returns existing real sub-folders of dir whose name matches
// pattern (filepath.Match syntax, case-insensitive).
func subDirs(dir, pattern string) []string {
	var out []string
	pattern = strings.ToLower(pattern)
	for _, e := range readDir(dir) {
		if isLink(e.info) || !e.info.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(pattern, strings.ToLower(e.info.Name())); ok {
			out = append(out, e.path)
		}
	}
	return out
}

func dirExists(p string) bool {
	_, ok := realDir(p)
	return ok
}

func fileExists(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && !fi.IsDir()
}

// contentsIfExists returns kContents targets for the given folders that exist.
func contentsIfExists(paths ...string) []target {
	var out []target
	for _, p := range paths {
		if dirExists(p) {
			out = append(out, target{Path: p, Kind: kContents})
		}
	}
	return out
}
