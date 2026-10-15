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
//   - a file is deleted only when no other program has it open; files in
//     use stay and are reported, nothing is forced and nothing is retried

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

// stats is the result of cleaning (or of a dry run) for one category.
type stats struct {
	bytes       int64 // deleted (dry run: would be deleted)
	files       int
	locked      int // in use by another program
	lockedBytes int64
	denied      int // no permission
	deniedBytes int64
	failed      int // any other error
	failedBytes int64
	issues      []issue // first few files that could not be deleted
}

type issue struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // locked, denied, failed
	Size int64  `json:"size"`
}

const maxIssues = 200

func (st *stats) add(o stats) {
	st.bytes += o.bytes
	st.files += o.files
	st.locked += o.locked
	st.lockedBytes += o.lockedBytes
	st.denied += o.denied
	st.deniedBytes += o.deniedBytes
	st.failed += o.failed
	st.failedBytes += o.failedBytes
	for _, i := range o.issues {
		if len(st.issues) < maxIssues {
			st.issues = append(st.issues, i)
		}
	}
}

func (st *stats) problem(kind, path string, size int64) {
	switch kind {
	case "gone":
		return // removed by someone else meanwhile: nothing to report
	case "locked":
		st.locked++
		st.lockedBytes += size
	case "denied":
		st.denied++
		st.deniedBytes += size
	default:
		kind = "failed"
		st.failed++
		st.failedBytes += size
	}
	if len(st.issues) < maxIssues {
		st.issues = append(st.issues, issue{Path: path, Kind: kind, Size: size})
	}
}

// cleaner carries the options of one cleaning run.
type cleaner struct {
	stop    func() bool
	dry     bool            // only report what would be deleted
	exclude map[string]bool // normalized paths the user unticked
}

func (c *cleaner) stopped() bool { return c != nil && c.stop != nil && c.stop() }

// excluded reports whether p or one of its parent folders was unticked.
func (c *cleaner) excluded(p string) bool {
	if c == nil || len(c.exclude) == 0 {
		return false
	}
	for q := normPath(p); ; {
		if c.exclude[q] {
			return true
		}
		parent := filepath.Dir(q)
		if parent == q {
			return false
		}
		q = parent
	}
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
	var b int64
	var n int
	for _, p := range planAll(ts, time.Now(), nil) {
		bb, nn := sizeOf(p)
		b += bb
		n += nn
	}
	return b, n
}

// planAll joins the plans of several targets. An entry planned twice, or
// one inside a folder that is planned as a whole, is listed once - so
// sizes are not counted twice and no two workers touch the same file.
func planAll(ts []target, now time.Time, c *cleaner) []planned {
	var work []planned
	seen := map[string]bool{}
	dirs := map[string]bool{}
	for _, t := range ts {
		if c.stopped() {
			break
		}
		for _, p := range t.plan(now) {
			k := normPath(p.path)
			if seen[k] {
				continue
			}
			seen[k] = true
			work = append(work, p)
			if p.info.IsDir() && !isLink(p.info) {
				dirs[k] = true
			}
		}
	}
	if len(dirs) == 0 {
		return work
	}
	out := work[:0]
	for _, p := range work {
		if !insideAny(normPath(p.path), dirs) {
			out = append(out, p)
		}
	}
	return out
}

// insideAny reports whether one of k's parent folders is in dirs.
func insideAny(k string, dirs map[string]bool) bool {
	for q := k; ; {
		parent := filepath.Dir(q)
		if parent == q {
			return false
		}
		if dirs[parent] {
			return true
		}
		q = parent
	}
}

// remove deletes an entry. A link is removed as a link; a folder is
// emptied first and then removed if it became empty. Files that cannot be
// deleted are counted by reason and skipped - there are no retries.
func remove(p planned, st *stats, c *cleaner) {
	if c.excluded(p.path) {
		return
	}
	if isLink(p.info) {
		if c != nil && c.dry {
			st.files++
			return
		}
		if err := removeOne(p.path); err == nil {
			st.files++
		} else {
			st.problem(errKind(err), p.path, 0)
		}
		return
	}
	if p.info.IsDir() {
		for _, e := range readDir(p.path) {
			if c.stopped() {
				return
			}
			remove(e, st, c)
		}
		if c == nil || !c.dry {
			_ = removeOne(p.path) // fails while something inside stayed: fine
		}
		return
	}
	size := p.info.Size()
	if c != nil && c.dry {
		if k := probeDelete(p.path); k != "" {
			st.problem(k, p.path, size)
			return
		}
		st.bytes += size
		st.files++
		return
	}
	if err := deleteFile(p.path); err == nil {
		st.bytes += size
		st.files++
	} else {
		st.problem(errKind(err), p.path, size)
	}
}

func cleanTargets(ts []target, stop func() bool) stats {
	return cleanWith(ts, &cleaner{stop: stop})
}

// cleanWith removes what the targets plan. Big categories are worked on by
// a few goroutines at once (deleting many small files is mostly waiting for
// the disk); every file still goes through the same checks.
func cleanWith(ts []target, c *cleaner) stats {
	work := planAll(ts, time.Now(), c)
	if len(work) < 8 {
		var st stats
		for _, p := range work {
			if c.stopped() {
				break
			}
			remove(p, &st, c)
		}
		return st
	}
	jobs := make(chan planned)
	results := make(chan stats)
	const workers = 4
	for i := 0; i < workers; i++ {
		go func() {
			var st stats
			for p := range jobs {
				if !c.stopped() {
					func() {
						defer recoverLog("clean")
						remove(p, &st, c)
					}()
				}
			}
			results <- st
		}()
	}
	for _, p := range work {
		if c.stopped() {
			break
		}
		jobs <- p
	}
	close(jobs)
	var st stats
	for i := 0; i < workers; i++ {
		st.add(<-results)
	}
	return st
}

// previewEntry is one top-level entry a category would remove.
type previewEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Files int    `json:"files"`
	Dir   bool   `json:"dir"`
}

// previewTargets lists what the targets would remove, largest first.
func previewTargets(ts []target, limit int) (entries []previewEntry, total int64, files int, more int) {
	for _, p := range planAll(ts, time.Now(), nil) {
		b, n := sizeOf(p)
		total += b
		files += n
		entries = append(entries, previewEntry{Path: p.path, Size: b, Files: n, Dir: p.info.IsDir() && !isLink(p.info)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Size > entries[j].Size })
	if limit > 0 && len(entries) > limit {
		more = len(entries) - limit
		entries = entries[:limit]
	}
	return entries, total, files, more
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
// contentsExcept is the contents of dir without the named sub-folders
// (case-insensitive): the files directly in dir, and the contents of every
// other sub-folder. Used for folders where one part is out of reach.
func contentsExcept(dir string, skip ...string) []target {
	if !dirExists(dir) {
		return nil
	}
	out := []target{{Path: dir, Kind: kGlob, Pattern: "*"}}
	for _, e := range readDir(dir) {
		if isLink(e.info) || !e.info.IsDir() {
			continue
		}
		name := e.info.Name()
		left := false
		for _, s := range skip {
			if strings.EqualFold(name, s) {
				left = true
				break
			}
		}
		if !left {
			out = append(out, target{Path: e.path, Kind: kContents})
		}
	}
	return out
}

func contentsIfExists(paths ...string) []target {
	var out []target
	for _, p := range paths {
		if dirExists(p) {
			out = append(out, target{Path: p, Kind: kContents})
		}
	}
	return out
}
