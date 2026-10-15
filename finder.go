package main

// Large Files Finder and Duplicate Finder (platform-neutral part).
//
// Both walk the chosen folders with a few goroutines at once. Links,
// junctions and mount points are never followed, and cloud files whose data
// is not on this PC are skipped (reading them would download them). Windows,
// Program Files, ProgramData, AppData, the Recycle Bin and System Volume
// Information are left out unless the user asks for them.
//
// Duplicates are found in three steps, each only on what is left from the
// one before: files of the same size; then the same 64 KB at the start and
// the end; then the same SHA-256 of the whole file. Hard links (two names of
// one file) are not duplicates and are counted once.
//
// Nothing is ever selected for deletion by the app. Deleting moves files to
// the Recycle Bin, and only after checking again that each file did not
// change since the scan and that at least one unchanged copy of every group
// stays.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type findOpts struct {
	Roots     []string `json:"roots"`
	MinSize   int64    `json:"minSize"`
	Cats      []string `json:"cats"`      // kinds of file (video, images, ...); empty = all
	OlderDays int      `json:"olderDays"` // only files not changed for that many days
	Hidden    bool     `json:"hidden"`    // also hidden and system files and folders
	System    bool     `json:"system"`    // also Windows, program folders and AppData
	Exclude   []string `json:"exclude"`   // folders to leave out
	Limit     int      `json:"limit"`     // large files: how many to list
}

type foundFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"` // unix seconds
	Attrs     uint32 `json:"attrs,omitempty"`
	Ext       string `json:"ext,omitempty"`
	Cat       string `json:"cat"`
	Protected bool   `json:"protected,omitempty"`
	mtimeNs   int64  // exact time of the last change, for the check before deleting
	part      uint64 // checksum of the first and last 64 KB (duplicates)
}

type dupGroup struct {
	Hash   string      `json:"hash"`
	Size   int64       `json:"size"`
	Files  []foundFile `json:"files"`
	Wasted int64       `json:"wasted"` // what deleting all but one would free
}

type findStats struct {
	files, dirs, bytes, matched, denied atomic.Int64
	toHash, hashed                      atomic.Int64
	phase, current                      atomic.Value // string
}

type findProgress struct {
	Phase   string `json:"phase"` // walk, compare, hash
	Files   int64  `json:"files"`
	Dirs    int64  `json:"dirs"`
	Bytes   int64  `json:"bytes"`
	Matched int64  `json:"matched"`
	Denied  int64  `json:"denied"`
	ToHash  int64  `json:"toHash"`
	Hashed  int64  `json:"hashed"`
	Current string `json:"current"`
}

func (s *findStats) progress() findProgress {
	p := findProgress{Files: s.files.Load(), Dirs: s.dirs.Load(), Bytes: s.bytes.Load(), Matched: s.matched.Load(),
		Denied: s.denied.Load(), ToHash: s.toHash.Load(), Hashed: s.hashed.Load()}
	p.Phase, _ = s.phase.Load().(string)
	p.Current, _ = s.current.Load().(string)
	return p
}

// finderFS is what the finder needs from the platform.
type finderFS struct {
	list      func(string) ([]dirEnt, error)
	identity  func(string) (string, bool) // the same id for two names of one file (hard links)
	protected func(string, uint32) bool   // never deleted from the app
	recycle   func([]string) error        // move to the Recycle Bin
	inUse     func(string) string         // "" or locked / denied / gone
	skipDir   func(dir, name string) bool // system folders (unless System is set)
}

const findWorkers = 8

// walkFiles calls visit for every file below the roots that passes the
// filters (visit may be called from several goroutines at once).
func walkFiles(o findOpts, fs finderFS, stop *atomic.Bool, st *findStats, visit func(foundFile)) {
	cats := map[string]bool{}
	for _, c := range o.Cats {
		cats[c] = true
	}
	var cutoff int64
	if o.OlderDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -o.OlderDays).Unix()
	}
	excl := map[string]bool{}
	for _, e := range o.Exclude {
		if e != "" {
			excl[normPath(e)] = true
		}
	}
	seenRoot := map[string]bool{}
	var roots []string
	for _, r := range o.Roots {
		r = filepath.Clean(r)
		if r == "" || r == "." || seenRoot[normPath(r)] {
			continue
		}
		seenRoot[normPath(r)] = true
		roots = append(roots, r)
	}
	// a root inside another root would be walked twice
	var top []string
	for _, r := range roots {
		inside := false
		for _, q := range roots {
			if q != r && isUnder(normPath(r), normPath(q)) {
				inside = true
				break
			}
		}
		if !inside {
			top = append(top, r)
		}
	}

	sem := make(chan struct{}, findWorkers)
	var wg sync.WaitGroup
	var walk func(dir string)
	walk = func(dir string) {
		if stop.Load() {
			return
		}
		st.current.Store(dir)
		ents, err := fs.list(dir)
		if err != nil {
			st.denied.Add(1)
			return
		}
		st.dirs.Add(1)
		for _, e := range ents {
			hidden := e.attrs&(attrHidden|attrSystem) != 0
			p := filepath.Join(dir, e.name)
			if e.dir {
				if e.link || (hidden && !o.Hidden) || excl[normPath(p)] || (!o.System && fs.skipDir != nil && fs.skipDir(dir, e.name)) {
					continue
				}
				select {
				case sem <- struct{}{}:
					wg.Add(1)
					go func(d string) {
						defer func() { <-sem; wg.Done() }()
						defer recoverLog("finder")
						walk(d)
					}(p)
				default:
					walk(p)
				}
				continue
			}
			if e.link || e.offline {
				continue
			}
			st.files.Add(1)
			st.bytes.Add(e.size)
			if (hidden && !o.Hidden) || e.size < o.MinSize || e.size <= 0 {
				continue
			}
			if cutoff != 0 && e.mtime > cutoff {
				continue
			}
			ext, cat := fileType(e.name)
			if len(cats) > 0 && !cats[cat] {
				continue
			}
			st.matched.Add(1)
			f := foundFile{Path: p, Size: e.size, Mtime: e.mtime, Attrs: e.attrs, Ext: ext, Cat: cat, mtimeNs: e.mtimeNs}
			if fs.protected != nil {
				f.Protected = fs.protected(p, e.attrs)
			}
			visit(f)
		}
	}
	for _, r := range top {
		walk(r)
	}
	wg.Wait()
}

// isUnder: p is q or inside q (both normalized).
func isUnder(p, q string) bool {
	if p == q {
		return true
	}
	q = strings.TrimSuffix(q, string(filepath.Separator)) + string(filepath.Separator)
	return strings.HasPrefix(p, q)
}

// findLarge lists the largest matching files, largest first.
func findLarge(o findOpts, fs finderFS, stop *atomic.Bool, st *findStats) []foundFile {
	if o.Limit <= 0 || o.Limit > 5000 {
		o.Limit = 500
	}
	st.phase.Store("walk")
	var mu sync.Mutex
	var top []foundFile
	smallest := int64(-1)
	walkFiles(o, fs, stop, st, func(f foundFile) {
		mu.Lock()
		defer mu.Unlock()
		if len(top) >= o.Limit && f.Size <= smallest {
			return
		}
		top = append(top, f)
		if len(top) > o.Limit*2 { // trim now and then, not for every file
			sort.Slice(top, func(i, j int) bool { return top[i].Size > top[j].Size })
			top = top[:o.Limit]
			smallest = top[len(top)-1].Size
		}
	})
	sort.Slice(top, func(i, j int) bool {
		if top[i].Size != top[j].Size {
			return top[i].Size > top[j].Size
		}
		return top[i].Path < top[j].Path
	})
	if len(top) > o.Limit {
		top = top[:o.Limit]
	}
	return top
}

var crcTable = crc64.MakeTable(crc64.ECMA)

const edgeBytes = 64 << 10

// partialHash checks the first and the last 64 KB of a file.
func partialHash(path string, size int64) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := crc64.New(crcTable)
	buf := make([]byte, edgeBytes)
	n, err := io.ReadFull(f, buf[:min(int64(edgeBytes), size)])
	if err != nil {
		return 0, err
	}
	h.Write(buf[:n])
	if size > 2*edgeBytes {
		if _, err := f.Seek(size-edgeBytes, io.SeekStart); err != nil {
			return 0, err
		}
		n, err = io.ReadFull(f, buf)
		if err != nil {
			return 0, err
		}
		h.Write(buf[:n])
	}
	return h.Sum64(), nil
}

var errStopped = errors.New("stopped")

// fullHash is the SHA-256 of the whole file.
func fullHash(path string, stop *atomic.Bool, done func(int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if stop.Load() {
			return "", errStopped
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			done(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findDupes returns groups of identical files, the most space wasted first.
func findDupes(o findOpts, fs finderFS, stop *atomic.Bool, st *findStats) []dupGroup {
	if o.MinSize <= 0 {
		o.MinSize = 1
	}
	st.phase.Store("walk")
	var mu sync.Mutex
	bySize := map[int64][]foundFile{}
	walkFiles(o, fs, stop, st, func(f foundFile) {
		mu.Lock()
		bySize[f.Size] = append(bySize[f.Size], f)
		mu.Unlock()
	})
	if stop.Load() {
		return nil
	}

	// step 2: same size, then the same start and end (hard links counted once)
	st.phase.Store("compare")
	var cands [][]foundFile
	for _, fl := range bySize {
		if len(fl) < 2 {
			continue
		}
		if fs.identity != nil {
			fl = dropHardLinks(fl, fs.identity)
			if len(fl) < 2 {
				continue
			}
		}
		cands = append(cands, fl)
	}
	var groups [][]foundFile
	var gmu sync.Mutex
	jobs := make(chan []foundFile)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fl := range jobs {
				byKey := map[uint64][]foundFile{}
				for _, f := range fl {
					if stop.Load() {
						break
					}
					st.current.Store(f.Path)
					var k uint64
					err := errStopped
					func() {
						defer recoverLog("finder")
						k, err = partialHash(f.Path, f.Size)
					}()
					if err != nil {
						continue // unreadable now: left out
					}
					f.part = k
					byKey[k] = append(byKey[k], f)
				}
				gmu.Lock()
				for _, g := range byKey {
					if len(g) > 1 {
						groups = append(groups, g)
					}
				}
				gmu.Unlock()
			}
		}()
	}
	for _, c := range cands {
		if stop.Load() {
			break
		}
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	if stop.Load() {
		return nil
	}

	// step 3: SHA-256 of the whole file (small files were read whole already)
	st.phase.Store("hash")
	var total int64
	for _, g := range groups {
		if g[0].Size > 2*edgeBytes {
			total += g[0].Size * int64(len(g))
		}
	}
	st.toHash.Store(total)
	var out []dupGroup
	var omu sync.Mutex
	gjobs := make(chan []foundFile)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range gjobs {
				byHash := map[string][]foundFile{}
				for _, f := range g {
					if stop.Load() {
						break
					}
					st.current.Store(f.Path)
					h, err := "", errStopped
					func() {
						defer recoverLog("finder")
						h, err = fullHash(f.Path, stop, func(n int64) {
							if f.Size > 2*edgeBytes {
								st.hashed.Add(n)
							}
						})
					}()
					if err != nil {
						continue
					}
					byHash[h] = append(byHash[h], f)
				}
				omu.Lock()
				for h, fl := range byHash {
					if len(fl) < 2 {
						continue
					}
					sort.Slice(fl, func(i, j int) bool {
						if fl[i].Mtime != fl[j].Mtime {
							return fl[i].Mtime < fl[j].Mtime
						}
						return fl[i].Path < fl[j].Path
					})
					out = append(out, dupGroup{Hash: h, Size: fl[0].Size, Files: fl, Wasted: fl[0].Size * int64(len(fl)-1)})
				}
				omu.Unlock()
			}
		}()
	}
	for _, g := range groups {
		if stop.Load() {
			break
		}
		gjobs <- g
	}
	close(gjobs)
	wg.Wait()
	if stop.Load() {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Wasted != out[j].Wasted {
			return out[i].Wasted > out[j].Wasted
		}
		return out[i].Files[0].Path < out[j].Files[0].Path
	})
	return out
}

// dropHardLinks keeps one name of each file.
func dropHardLinks(fl []foundFile, identity func(string) (string, bool)) []foundFile {
	seen := map[string]bool{}
	out := fl[:0:0]
	for _, f := range fl {
		if id, ok := identity(f.Path); ok {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		out = append(out, f)
	}
	return out
}

// ---------------------------------------------------------------- deleting

type delResult struct {
	Path  string `json:"path"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Size  int64  `json:"size,omitempty"`
}

// unchanged: the file is still the one the scan saw (same size, same time
// of the last change to the nanosecond as far as the file system keeps it).
func unchanged(f foundFile) bool {
	fi, err := os.Lstat(f.Path)
	if err != nil || fi.IsDir() || fi.Size() != f.Size {
		return false
	}
	if f.mtimeNs != 0 {
		return fi.ModTime().UnixNano() == f.mtimeNs
	}
	return fi.ModTime().Unix() == f.Mtime
}

var fullCheckLimit int64 = 256 << 20

// sameContent: the file still has the content the scan hashed. Files up
// to 256 MB are read whole again; for bigger ones the exact time of the
// last change, the size and the first and last 64 KB are compared (reading
// many gigabytes again would keep the user waiting for minutes).
func sameContent(f foundFile, hash string) bool {
	if !unchanged(f) {
		return false
	}
	if f.Size > fullCheckLimit && f.part != 0 && f.mtimeNs != 0 {
		k, err := partialHash(f.Path, f.Size)
		return err == nil && k == f.part
	}
	var never atomic.Bool
	h, err := fullHash(f.Path, &never, func(int64) {})
	return err == nil && h == hash
}

// planDupDelete checks a deletion request against the scan: every path must
// be a file of a group, unchanged, and every group must keep at least one
// unchanged copy that is not being deleted. It returns the files that may
// go and the refused ones with the reason (a text key).
func planDupDelete(groups []dupGroup, paths []string, fs finderFS) (ok []foundFile, refused []delResult) {
	want := map[string]bool{}
	for _, p := range paths {
		want[normPath(p)] = true
	}
	found := map[string]bool{}
	for _, g := range groups {
		var del []foundFile
		keeper := false
		for _, f := range g.Files {
			if want[normPath(f.Path)] {
				found[normPath(f.Path)] = true
				del = append(del, f)
			} else if !keeper && sameContent(f, g.Hash) {
				keeper = true // read again: this copy really stays as it was
			}
		}
		if len(del) == 0 {
			continue
		}
		if !keeper {
			for _, f := range del {
				refused = append(refused, delResult{Path: f.Path, Error: "dup.err.keep"})
			}
			continue
		}
		for _, f := range del {
			switch {
			case f.Protected || (fs.protected != nil && fs.protected(f.Path, f.Attrs)):
				refused = append(refused, delResult{Path: f.Path, Error: "da.err.protected"})
			case !unchanged(f):
				refused = append(refused, delResult{Path: f.Path, Error: "dup.err.changed"})
			case fs.inUse != nil && fs.inUse(f.Path) == "locked":
				refused = append(refused, delResult{Path: f.Path, Error: "da.err.inuse"})
			default:
				ok = append(ok, f)
			}
		}
	}
	for _, p := range paths {
		if !found[normPath(p)] {
			refused = append(refused, delResult{Path: p, Error: "da.err.unknown"})
		}
	}
	return ok, refused
}

// planFileDelete checks a deletion request from the large files list.
func planFileDelete(list []foundFile, paths []string, fs finderFS) (ok []foundFile, refused []delResult) {
	byPath := map[string]foundFile{}
	for _, f := range list {
		byPath[normPath(f.Path)] = f
	}
	for _, p := range paths {
		f, known := byPath[normPath(p)]
		switch {
		case !known:
			refused = append(refused, delResult{Path: p, Error: "da.err.unknown"})
		case f.Protected || (fs.protected != nil && fs.protected(f.Path, f.Attrs)):
			refused = append(refused, delResult{Path: p, Error: "da.err.protected"})
		case !unchanged(f):
			refused = append(refused, delResult{Path: p, Error: "dup.err.changed"})
		case fs.inUse != nil && fs.inUse(f.Path) == "locked":
			refused = append(refused, delResult{Path: p, Error: "da.err.inuse"})
		default:
			ok = append(ok, f)
		}
	}
	return ok, refused
}

// recycleFiles moves the files to the Recycle Bin in batches and checks
// each one afterwards.
func recycleFiles(files []foundFile, fs finderFS) []delResult {
	var out []delResult
	for i := 0; i < len(files); i += 100 {
		batch := files[i:min(i+100, len(files))]
		paths := make([]string, len(batch))
		for k, f := range batch {
			paths[k] = f.Path
		}
		err := fs.recycle(paths)
		for _, f := range batch {
			r := delResult{Path: f.Path, Size: f.Size}
			if _, e := os.Lstat(f.Path); e == nil {
				r.Error = "da.err.notdone"
				if err != nil {
					r.Error = err.Error()
				}
			} else {
				r.OK = true
			}
			out = append(out, r)
		}
	}
	return out
}

// removeFromGroups drops deleted files; groups left with one file go.
func removeFromGroups(groups []dupGroup, gone map[string]bool) []dupGroup {
	var out []dupGroup
	for _, g := range groups {
		var keep []foundFile
		for _, f := range g.Files {
			if !gone[normPath(f.Path)] {
				keep = append(keep, f)
			}
		}
		if len(keep) > 1 {
			g.Files = keep
			g.Wasted = g.Size * int64(len(keep)-1)
			out = append(out, g)
		}
	}
	return out
}
