package main

// Restoring what this app moved to the Recycle Bin.
//
// Every file or folder the app sends to the Recycle Bin (from the Disk
// Analyzer, the Large Files Finder or the Duplicate Finder) is noted in
// recycled.json with where it was and when. Windows keeps each deleted item
// in <drive>\$Recycle.Bin\<user SID>\ as two entries: $R<id> holds the data
// and $I<id> the original path, size and time of deletion. To put an item
// back it is renamed from $R<id> to its old path (the same drive, so nothing
// is copied) and its $I<id> is removed - exactly what "Restore" in the
// Recycle Bin does. An item is never put over a file that exists now.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

type binItem struct {
	Orig    string // where it was
	Size    int64
	Deleted int64 // unix ms
	IFile   string
	RFile   string
}

// parseIFile reads a $I file (format 1: Windows Vista to 8, format 2:
// Windows 10 and 11).
func parseIFile(b []byte) (orig string, size, deleted int64, ok bool) {
	if len(b) < 28 {
		return "", 0, 0, false
	}
	le := binary.LittleEndian
	ver := le.Uint64(b[0:])
	size = int64(le.Uint64(b[8:]))
	ft := int64(le.Uint64(b[16:]))
	if ft > 116444736000000000 {
		deleted = (ft - 116444736000000000) / 10000
	}
	var u []uint16
	switch ver {
	case 1:
		if len(b) < 24+520 {
			return "", 0, 0, false
		}
		for i := 24; i+1 < 24+520; i += 2 {
			c := le.Uint16(b[i:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
	case 2:
		n := int(le.Uint32(b[24:]))
		if n <= 0 || 28+n*2 > len(b) {
			return "", 0, 0, false
		}
		for i := 0; i < n; i++ {
			c := le.Uint16(b[28+i*2:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
	default:
		return "", 0, 0, false
	}
	orig = string(utf16.Decode(u))
	return orig, size, deleted, orig != ""
}

// makeIFile builds a format-2 $I file (used by the tests).
func makeIFile(orig string, size, deletedMs int64) []byte {
	u := utf16.Encode([]rune(orig))
	u = append(u, 0)
	b := make([]byte, 28+len(u)*2)
	le := binary.LittleEndian
	le.PutUint64(b[0:], 2)
	le.PutUint64(b[8:], uint64(size))
	le.PutUint64(b[16:], uint64(deletedMs*10000+116444736000000000))
	le.PutUint32(b[24:], uint32(len(u)))
	for i, c := range u {
		le.PutUint16(b[28+i*2:], c)
	}
	return b
}

// listBin reads the items of one Recycle Bin folder.
func listBin(dir string) []binItem {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []binItem
	for _, e := range ents {
		n := e.Name()
		if len(n) < 3 || !strings.EqualFold(n[:2], "$I") || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || len(b) > 64<<10 {
			continue
		}
		orig, size, del, ok := parseIFile(b)
		if !ok {
			continue
		}
		r := filepath.Join(dir, "$R"+n[2:])
		if _, err := os.Lstat(r); err != nil {
			continue // only the info is left: nothing to restore
		}
		out = append(out, binItem{Orig: orig, Size: size, Deleted: del, IFile: filepath.Join(dir, n), RFile: r})
	}
	return out
}

var (
	errBinExists  = errors.New("rc.err.exists")
	errBinMissing = errors.New("rc.err.notInBin")
)

// restoreBinItem moves an item back to where it was.
func restoreBinItem(it binItem) error {
	if _, err := os.Lstat(it.Orig); err == nil {
		return errBinExists
	}
	if err := os.MkdirAll(filepath.Dir(it.Orig), 0o755); err != nil {
		return err
	}
	if err := os.Rename(it.RFile, it.Orig); err != nil {
		return err
	}
	_ = os.Remove(it.IFile)
	return nil
}

// ---------------------------------------------------------------- what the app recycled

type recycledEntry struct {
	ID       int64  `json:"id"`
	Source   string `json:"source"` // disk, large, dupes
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	At       int64  `json:"at"` // unix ms
	Restored int64  `json:"restored,omitempty"`
	InBin    bool   `json:"inBin"` // filled when listed
}

type recycleLog struct {
	mu   sync.Mutex
	path string
	list []recycledEntry
	last int64
}

var rlog = &recycleLog{}

func (r *recycleLog) load() {
	r.list = nil
	if r.path == "" {
		return
	}
	if b, err := os.ReadFile(r.path); err == nil {
		_ = json.Unmarshal(b, &r.list)
	}
	for _, e := range r.list {
		if e.ID > r.last {
			r.last = e.ID
		}
	}
}

func (r *recycleLog) save() {
	if r.path == "" {
		return
	}
	if len(r.list) > 3000 {
		r.list = r.list[len(r.list)-3000:]
	}
	b, err := json.Marshal(r.list)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.path), 0o755)
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, r.path)
	}
}

// noteRecycled remembers what was just moved to the Recycle Bin.
func noteRecycled(source string, items []delResult) {
	r := rlog
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UnixMilli()
	for _, it := range items {
		if !it.OK {
			continue
		}
		id := time.Now().UnixMicro()
		if id <= r.last {
			id = r.last + 1
		}
		r.last = id
		r.list = append(r.list, recycledEntry{ID: id, Source: source, Path: it.Path, Size: it.Size, At: now})
	}
	r.save()
}

// findInBin finds the Recycle Bin item of an entry: the same original path,
// deleted within two minutes of when the app recycled it (the newest such
// item if the same path was deleted more than once).
func findInBin(e recycledEntry, items []binItem) (binItem, bool) {
	var best binItem
	found := false
	for _, it := range items {
		if !strings.EqualFold(filepath.Clean(it.Orig), filepath.Clean(e.Path)) {
			continue
		}
		d := it.Deleted - e.At
		if d < -120_000 || d > 120_000 {
			continue
		}
		if !found || abs64(it.Deleted-e.At) < abs64(best.Deleted-e.At) {
			best, found = it, true
		}
	}
	return best, found
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// recycledList returns what the app recycled, newest first, with whether
// each item can still be restored. binOf gives the Recycle Bin folder for a
// path.
func recycledList(binOf func(string) string) []recycledEntry {
	r := rlog
	r.mu.Lock()
	defer r.mu.Unlock()
	bins := map[string][]binItem{}
	out := make([]recycledEntry, 0, len(r.list))
	for i := len(r.list) - 1; i >= 0; i-- {
		e := r.list[i]
		if e.Restored == 0 {
			dir := binOf(e.Path)
			items, ok := bins[dir]
			if !ok {
				items = listBin(dir)
				bins[dir] = items
			}
			_, e.InBin = findInBin(e, items)
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// restoreRecycled puts one item back.
func restoreRecycled(id int64, binOf func(string) string) error {
	r := rlog
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.list {
		e := &r.list[i]
		if e.ID != id {
			continue
		}
		if e.Restored != 0 {
			return errors.New("rc.err.done")
		}
		it, ok := findInBin(*e, listBin(binOf(e.Path)))
		if !ok {
			return errBinMissing
		}
		if err := restoreBinItem(it); err != nil {
			return err
		}
		e.Restored = time.Now().UnixMilli()
		r.save()
		return nil
	}
	return errors.New("rc.err.unknown")
}
