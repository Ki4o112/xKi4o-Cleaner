package main

// Disk Analyzer engine (platform-neutral part).
//
// The scan builds a tree of folders only, with the total logical size, the
// size on disk and the file count of every subtree. Files are not kept in
// memory, except the largest ones; the files of one folder are read again
// when the page opens that folder. Links, junctions and mount points are
// never followed (they would count the same data twice or leave the drive).
// Folders that cannot be read are marked and skipped; the scan goes on.

import (
	"container/heap"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// dirEnt is one directory entry as the platform layer reads it.
type dirEnt struct {
	name    string
	dir     bool
	size    int64 // logical size
	alloc   int64 // size on disk (-1 = unknown)
	attrs   uint32
	mtime   int64 // unix seconds
	mtimeNs int64 // unix nanoseconds (to notice a change within the same second)
	link    bool  // a link, junction or mount point: not followed
	offline bool  // a cloud file whose data is not on this PC
}

const (
	attrReadOnly = 0x1
	attrHidden   = 0x2
	attrSystem   = 0x4
)

type dnode struct {
	name     string
	parent   *dnode
	kids     []*dnode
	size     int64 // subtree totals
	alloc    int64
	files    int64
	dirs     int64
	ownSize  int64 // files directly inside
	ownAlloc int64
	ownFiles int64
	mtime    int64
	denied   bool // could not be read
	link     bool // not followed
	partial  bool // something below could not be read
	attrs    uint32
}

func (n *dnode) path() string {
	if n.parent == nil {
		return n.name
	}
	return filepath.Join(n.parent.path(), n.name)
}

// rollup computes the subtree totals (post-order).
func rollup(n *dnode) {
	n.size, n.alloc, n.files, n.dirs = n.ownSize, n.ownAlloc, n.ownFiles, 0
	n.partial = n.denied
	for _, k := range n.kids {
		rollup(k)
		n.size += k.size
		n.alloc += k.alloc
		n.files += k.files
		n.dirs += k.dirs + 1
		n.partial = n.partial || k.partial
	}
}

// ---------------------------------------------------------------- file types

var typeExt = map[string]string{}

func init() {
	add := func(cat string, exts ...string) {
		for _, e := range exts {
			typeExt[e] = cat
		}
	}
	add("video", ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".webm", ".m4v", ".flv", ".mpg", ".mpeg", ".ts", ".m2ts", ".3gp")
	add("audio", ".mp3", ".wav", ".flac", ".aac", ".ogg", ".m4a", ".wma", ".opus", ".aiff")
	add("images", ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".heic", ".tif", ".tiff", ".raw", ".cr2", ".nef", ".arw", ".dng", ".psd", ".svg", ".ico")
	add("documents", ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp", ".txt", ".rtf", ".md", ".epub", ".csv")
	add("archives", ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".zst", ".cab", ".tgz")
	add("diskimages", ".iso", ".img", ".vhd", ".vhdx", ".vmdk", ".vdi", ".qcow2", ".wim", ".esd", ".avhdx")
	add("programs", ".exe", ".dll", ".msi", ".msp", ".sys", ".appx", ".msix", ".msixbundle", ".appxbundle", ".pak", ".so", ".ocx")
	add("code", ".js", ".ts", ".go", ".py", ".c", ".cpp", ".h", ".cs", ".java", ".rs", ".json", ".xml", ".yml", ".yaml", ".html", ".css", ".class", ".jar", ".pdb", ".obj", ".lib", ".o", ".a", ".node")
	add("system", ".log", ".etl", ".tmp", ".dmp", ".evtx", ".db", ".dat", ".bin", ".cache", ".blf", ".regtrans-ms", ".old", ".bak", ".edb", ".jrs", ".chk")
}

func fileType(name string) (ext, cat string) {
	ext = strings.ToLower(filepath.Ext(name))
	if len(ext) > 16 || strings.ContainsAny(ext, " ") {
		ext = ""
	}
	if c, ok := typeExt[ext]; ok {
		return ext, c
	}
	return ext, "other"
}

type typeStat struct {
	Size  int64 `json:"size"`
	Alloc int64 `json:"alloc"`
	Files int64 `json:"files"`
}

// ---------------------------------------------------------------- largest files

type bigFile struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Alloc int64  `json:"alloc"`
	Mtime int64  `json:"mtime"`
	Attrs uint32 `json:"attrs"`
	Ext   string `json:"ext"`
	Cat   string `json:"cat"`
}

type fileHeap []bigFile

func (h fileHeap) Len() int           { return len(h) }
func (h fileHeap) Less(i, j int) bool { return h[i].Size < h[j].Size }
func (h fileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *fileHeap) Push(x any)        { *h = append(*h, x.(bigFile)) }
func (h *fileHeap) Pop() any          { o := *h; n := len(o); x := o[n-1]; *h = o[:n-1]; return x }

// topN keeps the n largest files offered to it.
type topN struct {
	n int
	h fileHeap
}

func (t *topN) offer(f bigFile) {
	if len(t.h) < t.n {
		heap.Push(&t.h, f)
		return
	}
	if f.Size > t.h[0].Size {
		t.h[0] = f
		heap.Fix(&t.h, 0)
	}
}

func (t *topN) sorted() []bigFile {
	out := append([]bigFile(nil), t.h...)
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// ---------------------------------------------------------------- the scan

type scanStats struct {
	files, dirs, bytes, denied atomic.Int64
	current                    atomic.Value // string
}

type scanner struct {
	list    func(path string) ([]dirEnt, error) // platform directory reader
	workers int
	stop    *atomic.Bool
	st      *scanStats
	topSize int
	mu      sync.Mutex
	types   map[string]*typeStat // by category
	exts    map[string]*typeStat // by extension
	top     *topN
	queue   []*dnode
	pending int
	cond    *sync.Cond
}

// scanTree reads the tree below root. It returns early (with what it has)
// when stop is set.
func scanTree(root string, list func(string) ([]dirEnt, error), workers int, stop *atomic.Bool, st *scanStats) (*dnode, map[string]*typeStat, map[string]*typeStat, []bigFile) {
	s := &scanner{list: list, workers: workers, stop: stop, st: st, topSize: 1000,
		types: map[string]*typeStat{}, exts: map[string]*typeStat{}, top: &topN{n: 1000}}
	s.cond = sync.NewCond(&s.mu)
	r := &dnode{name: root}
	s.queue = []*dnode{r}
	s.pending = 1
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.work()
		}()
	}
	wg.Wait()
	rollup(r)
	return r, s.types, s.exts, s.top.sorted()
}

func (s *scanner) next() *dnode {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 && s.pending > 0 && !s.stop.Load() {
		s.cond.Wait()
	}
	if len(s.queue) == 0 || s.stop.Load() {
		s.cond.Broadcast()
		return nil
	}
	n := s.queue[len(s.queue)-1]
	s.queue = s.queue[:len(s.queue)-1]
	return n
}

func (s *scanner) work() {
	types, exts := map[string]*typeStat{}, map[string]*typeStat{}
	top := &topN{n: s.topSize}
	for {
		n := s.next()
		if n == nil {
			break
		}
		var kids []*dnode
		func() {
			defer recoverLog("disk scan")
			kids = s.readOne(n, types, exts, top)
		}()
		s.mu.Lock()
		s.queue = append(s.queue, kids...)
		s.pending += len(kids) - 1
		s.cond.Broadcast()
		s.mu.Unlock()
	}
	s.mu.Lock()
	for k, v := range types {
		mergeStat(s.types, k, v)
	}
	for k, v := range exts {
		mergeStat(s.exts, k, v)
	}
	for _, f := range top.h {
		s.top.offer(f)
	}
	s.mu.Unlock()
}

func mergeStat(m map[string]*typeStat, k string, v *typeStat) {
	t := m[k]
	if t == nil {
		t = &typeStat{}
		m[k] = t
	}
	t.Size += v.Size
	t.Alloc += v.Alloc
	t.Files += v.Files
}

// readOne reads one folder: files are counted, sub-folders become nodes.
func (s *scanner) readOne(n *dnode, types, exts map[string]*typeStat, top *topN) []*dnode {
	p := n.path()
	s.st.current.Store(p)
	ents, err := s.list(p)
	if err != nil {
		n.denied = true
		s.st.denied.Add(1)
		return nil
	}
	var kids []*dnode
	for _, e := range ents {
		if e.dir {
			k := &dnode{name: e.name, parent: n, mtime: e.mtime, attrs: e.attrs, link: e.link}
			n.kids = append(n.kids, k)
			s.st.dirs.Add(1)
			if !e.link {
				kids = append(kids, k)
			}
			continue
		}
		if e.link {
			continue // a link to a file: the data is counted where it really is
		}
		alloc := e.alloc
		if alloc < 0 {
			alloc = e.size
		}
		n.ownSize += e.size
		n.ownAlloc += alloc
		n.ownFiles++
		s.st.files.Add(1)
		s.st.bytes.Add(e.size)
		ext, cat := fileType(e.name)
		mergeStat(types, cat, &typeStat{e.size, alloc, 1})
		if ext != "" {
			mergeStat(exts, ext, &typeStat{e.size, alloc, 1})
		}
		if e.size > 0 && (len(top.h) < top.n || e.size > top.h[0].Size) {
			top.offer(bigFile{Path: filepath.Join(p, e.name), Size: e.size, Alloc: alloc, Mtime: e.mtime, Attrs: e.attrs, Ext: ext, Cat: cat})
		}
	}
	return kids
}

// find returns the node of a path below root (case-insensitive), or nil.
func (n *dnode) find(path string) *dnode {
	rel, err := filepath.Rel(n.name, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil
	}
	if rel == "." {
		return n
	}
	cur := n
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		var next *dnode
		for _, k := range cur.kids {
			if strings.EqualFold(k.name, part) {
				next = k
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// replaceSubtree puts a freshly scanned node in place of an old one and
// fixes the totals of everything above it.
func replaceSubtree(old, fresh *dnode) {
	p := old.parent
	if p == nil {
		name := old.name
		*old = *fresh
		old.name = name
		for _, k := range old.kids {
			k.parent = old
		}
		return
	}
	fresh.parent, fresh.name = p, old.name
	for i, k := range p.kids {
		if k == old {
			p.kids[i] = fresh
		}
	}
	for a := p; a != nil; a = a.parent {
		rollupShallow(a)
	}
}

// removeNode takes a deleted folder out of the tree.
func removeNode(n *dnode) {
	p := n.parent
	if p == nil {
		return
	}
	for i, k := range p.kids {
		if k == n {
			p.kids = append(p.kids[:i], p.kids[i+1:]...)
			break
		}
	}
	for a := p; a != nil; a = a.parent {
		rollupShallow(a)
	}
}

// removeFile takes a deleted file out of its folder's totals.
func removeFile(dir *dnode, size, alloc int64) {
	dir.ownSize -= size
	dir.ownAlloc -= alloc
	dir.ownFiles--
	for a := dir; a != nil; a = a.parent {
		rollupShallow(a)
	}
}

func rollupShallow(n *dnode) {
	n.size, n.alloc, n.files, n.dirs = n.ownSize, n.ownAlloc, n.ownFiles, 0
	n.partial = n.denied
	for _, k := range n.kids {
		n.size += k.size
		n.alloc += k.alloc
		n.files += k.files
		n.dirs += k.dirs + 1
		n.partial = n.partial || k.partial
	}
}

// sinceMs is a small helper for progress timing.
func sinceMs(t time.Time) int64 { return time.Since(t).Milliseconds() }

// windowsOwned: folders in Program Files and ProgramData that belong to
// Windows itself (or to all programs), never deleted from the Disk Analyzer.
var windowsOwned = map[string]bool{"windows defender": true, "windows defender advanced threat protection": true, "windowsapps": true,
	"modifiablewindowsapps": true, "common files": true, "windows nt": true, "windows mail": true, "windows media player": true,
	"windows photo viewer": true, "windows portable devices": true, "windows security": true, "windows sidebar": true,
	"windowspowershell": true, "internet explorer": true, "microsoft": true, "microsoft.net": true, "reference assemblies": true,
	"msbuild": true, "dotnet": true, "uninstall information": true, "packages": true, "package cache": true, "regid.1991-06.com.microsoft": true,
	"ssh": true, "usoshared": true, "usoprivate": true, "softwaredistribution": true, "desktop": true, "documents": true,
	"start menu": true, "templates": true, "application data": true}

// programFolderOK: rel (lower case, below the drive) is a program's folder in
// Program Files, Program Files (x86) or ProgramData, or inside one, and not
// one that belongs to Windows.
func programFolderOK(rel string) bool {
	parts := strings.Split(rel, `\`)
	if len(parts) < 2 || parts[1] == "" {
		return false
	}
	switch parts[0] {
	case "program files", "program files (x86)", "programdata":
		return !windowsOwned[parts[1]] && !strings.HasPrefix(parts[1], "microsoft") && !strings.HasPrefix(parts[1], "windows")
	}
	return false
}
