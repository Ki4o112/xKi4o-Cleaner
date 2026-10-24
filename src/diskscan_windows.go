//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------- reading folders

const (
	fileFullDirectoryInfo        = 14
	fileFullDirectoryRestartInfo = 15
	reparseNameSurrogate         = 0x20000000
	attrRecallOnOpen             = 0x40000
	attrRecallOnDataAccess       = 0x400000
	attrOffline                  = 0x1000
)

var dirBufPool = sync.Pool{New: func() any { b := make([]byte, 64*1024); return &b }}

// listDirFull reads one folder with sizes on disk, in large batches (one
// handle per folder, no per-file calls).
func listDirFull(path string) ([]dirEnt, error) {
	p, err := windows.UTF16PtrFromString(longName(path))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if h != 0 {
			windows.CloseHandle(h)
		}
	}()
	bp := dirBufPool.Get().(*[]byte)
	defer dirBufPool.Put(bp)
	buf := *bp
	var out []dirEnt
	class := uint32(fileFullDirectoryRestartInfo)
	for {
		err := windows.GetFileInformationByHandleEx(h, class, &buf[0], uint32(len(buf)))
		first := class == fileFullDirectoryRestartInfo
		class = fileFullDirectoryInfo
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			if first && (errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
				errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_INVALID_LEVEL)) {
				windows.CloseHandle(h)
				h = 0
				return listDirSimple(path)
			}
			if len(out) == 0 {
				return nil, err
			}
			break
		}
		off := 0
		for {
			rec := buf[off:]
			next := binary.LittleEndian.Uint32(rec[0:])
			mtime := int64(binary.LittleEndian.Uint64(rec[24:]))
			eof := int64(binary.LittleEndian.Uint64(rec[40:]))
			alloc := int64(binary.LittleEndian.Uint64(rec[48:]))
			attrs := binary.LittleEndian.Uint32(rec[56:])
			nameLen := int(binary.LittleEndian.Uint32(rec[60:]))
			tag := binary.LittleEndian.Uint32(rec[64:]) // EaSize holds the reparse tag for reparse points
			name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&rec[68])), nameLen/2))
			if name != "." && name != ".." {
				e := dirEnt{name: name, dir: attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0, size: eof, alloc: alloc, attrs: attrs,
					mtime: filetimeUnix(mtime), mtimeNs: filetimeNanos(mtime)}
				if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && tag&reparseNameSurrogate != 0 {
					e.link = true // junction, symlink, mount point: never followed
				}
				if attrs&(attrRecallOnOpen|attrRecallOnDataAccess|attrOffline) != 0 {
					e.offline = true
				}
				if e.dir {
					e.size, e.alloc = 0, 0
				}
				out = append(out, e)
			}
			if next == 0 {
				break
			}
			off += int(next)
		}
	}
	runtime.KeepAlive(bp)
	return out, nil
}

// listDirSimple is the fallback for file systems that do not report sizes
// on disk this way (the size on disk is then taken as the logical size).
func listDirSimple(path string) ([]dirEnt, error) {
	ents, err := os.ReadDir(longName(path))
	if err != nil {
		return nil, err
	}
	out := make([]dirEnt, 0, len(ents))
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		var attrs uint32
		if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
			attrs = d.FileAttributes
		}
		de := dirEnt{name: e.Name(), dir: fi.IsDir(), size: fi.Size(), alloc: -1, attrs: attrs, mtime: fi.ModTime().Unix(), mtimeNs: fi.ModTime().UnixNano(), link: isLink(fi)}
		if de.dir {
			de.size, de.alloc = 0, 0
		}
		out = append(out, de)
	}
	return out, nil
}

// filetimeNanos converts a FILETIME to unix nanoseconds (what os.Stat's
// ModTime().UnixNano() gives for the same file).
func filetimeNanos(ft int64) int64 {
	if ft <= 116444736000000000 {
		return 0
	}
	return (ft - 116444736000000000) * 100
}

func filetimeUnix(ft int64) int64 {
	if ft <= 116444736000000000 {
		return 0
	}
	return (ft - 116444736000000000) / 10000000
}

// longName adds the \\?\ prefix for paths longer than MAX_PATH.
func longName(p string) string {
	if len(p) < 240 || strings.HasPrefix(p, `\\?\`) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}

// enableBackupPrivilege lets an administrator read folders whose permissions
// would otherwise hide them (only reading; nothing else changes).
var backupPriv sync.Once

func enableBackupPrivilege() {
	backupPriv.Do(func() {
		var tok windows.Token
		if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok) != nil {
			return
		}
		defer tok.Close()
		var luid windows.LUID
		name, _ := windows.UTF16PtrFromString("SeBackupPrivilege")
		if windows.LookupPrivilegeValue(nil, name, &luid) != nil {
			return
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		_ = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
	})
}

// ---------------------------------------------------------------- volumes

type diskVolume struct {
	Root  string `json:"root"`
	Label string `json:"label"`
	FS    string `json:"fs"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Kind  string `json:"kind"` // fixed, removable, network
}

func diskVolumes() []diskVolume {
	mask, _ := windows.GetLogicalDrives()
	type slot struct {
		v  diskVolume
		ok bool
	}
	var slots [26]slot
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		var kind string
		switch windows.GetDriveType(rp) {
		case windows.DRIVE_FIXED:
			kind = "fixed"
		case windows.DRIVE_REMOVABLE:
			kind = "removable"
		case windows.DRIVE_REMOTE:
			kind = "network"
		default:
			continue
		}
		// Each drive is asked on its own: a disconnected network drive or a
		// sleeping USB disk can block for many seconds and must not hold up
		// the list (it is left out when it does not answer in time).
		wg.Add(1)
		go func(i int, root, kind string) {
			defer wg.Done()
			defer recoverLog("drives")
			rp, _ := windows.UTF16PtrFromString(root)
			label := make([]uint16, 261)
			fs := make([]uint16, 261)
			if windows.GetVolumeInformation(rp, &label[0], 261, nil, nil, nil, &fs[0], 261) != nil {
				return // no media
			}
			t, f := diskSpace(root)
			mu.Lock()
			slots[i] = slot{diskVolume{Root: root, Label: windows.UTF16ToString(label), FS: windows.UTF16ToString(fs), Total: t, Free: f, Kind: kind}, true}
			mu.Unlock()
		}(i, root, kind)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2500 * time.Millisecond):
	}
	mu.Lock()
	defer mu.Unlock()
	var out []diskVolume
	for _, s := range slots {
		if s.ok {
			out = append(out, s.v)
		}
	}
	return out
}

// diskPickFolder lets the user choose a folder to analyse.
func diskPickFolder() string { return pickFolder(T("da.pickTitle")) }

var (
	procSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree        = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")
)

type browseInfo struct {
	owner       uintptr
	root        uintptr
	displayName *uint16
	title       *uint16
	flags       uint32
	callback    uintptr
	lparam      uintptr
	image       int32
}

func pickFolder(title string) string {
	var out string
	withCOM(func() {
		name := make([]uint16, windows.MAX_PATH)
		t, _ := windows.UTF16PtrFromString(title)
		bi := browseInfo{owner: app.hwnd, displayName: &name[0], title: t, flags: 0x1 | 0x40 | 0x200} // RETURNONLYFSDIRS, NEWDIALOGSTYLE, NONEWFOLDERBUTTON
		pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			return
		}
		defer procCoTaskMemFree.Call(pidl)
		buf := make([]uint16, 32768)
		if r, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
			out = windows.UTF16ToString(buf)
		}
	})
	return out
}

// ---------------------------------------------------------------- the scan job

type diskJob struct {
	mu       sync.Mutex
	root     *dnode
	rootPath string
	types    map[string]*typeStat
	exts     map[string]*typeStat
	top      []bigFile
	running  bool
	stop     atomic.Bool
	st       *scanStats
	started  time.Time
	took     time.Duration
	stale    bool // types/top changed by deletions or a refresh after the scan
	gen      int64
}

var dj = &diskJob{}

type diskProgress struct {
	Files   int64  `json:"files"`
	Dirs    int64  `json:"dirs"`
	Bytes   int64  `json:"bytes"`
	Denied  int64  `json:"denied"`
	Current string `json:"current"`
	Ms      int64  `json:"ms"`
}

// diskScan starts scanning a drive or folder in the background. Progress
// comes as "disk-progress" events, the end as "disk-done".
func diskScan(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return false
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return false
	}
	if len(path) == 2 && path[1] == ':' {
		path += `\`
	}
	dj.mu.Lock()
	if dj.running {
		dj.mu.Unlock()
		return false
	}
	dj.running = true
	dj.stop.Store(false)
	st := &scanStats{}
	st.current.Store(path)
	dj.st, dj.started = st, time.Now()
	dj.gen++
	gen := dj.gen
	dj.mu.Unlock()
	if app.admin {
		enableBackupPrivilege()
	}
	go func() {
		done := make(chan struct{})
		go func() {
			t := time.NewTicker(250 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					emit("disk-progress", progressOf(st))
				}
			}
		}()
		var root *dnode
		var types, exts map[string]*typeStat
		var top []bigFile
		func() {
			defer recoverLog("disk scan")
			root, types, exts, top = scanTree(path, listDirFull, scanWorkers(), &dj.stop, st)
		}()
		close(done)
		dj.mu.Lock()
		stopped := dj.stop.Load()
		if root != nil && gen == dj.gen {
			dj.root, dj.rootPath, dj.types, dj.exts, dj.top = root, path, types, exts, top
			dj.took, dj.stale = time.Since(dj.started), false
		}
		dj.running = false
		dj.mu.Unlock()
		p := progressOf(st)
		emit("disk-done", map[string]any{"ok": root != nil, "stopped": stopped, "path": path, "progress": p})
	}()
	return true
}

func scanWorkers() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		n = 4
	}
	if n > 16 {
		n = 16
	}
	return n
}

func progressOf(st *scanStats) diskProgress {
	cur, _ := st.current.Load().(string)
	return diskProgress{Files: st.files.Load(), Dirs: st.dirs.Load(), Bytes: st.bytes.Load(), Denied: st.denied.Load(), Current: cur, Ms: sinceMs(dj.started)}
}

func diskCancel() bool {
	dj.stop.Store(true)
	return true
}

// ---------------------------------------------------------------- results

type diskItem struct {
	Name      string     `json:"name"`
	Path      string     `json:"path"`
	Dir       bool       `json:"dir"`
	Size      int64      `json:"size"`
	Alloc     int64      `json:"alloc"`
	Files     int64      `json:"files,omitempty"`
	Dirs      int64      `json:"dirs,omitempty"`
	Mtime     int64      `json:"mtime,omitempty"`
	Ext       string     `json:"ext,omitempty"`
	Cat       string     `json:"cat,omitempty"`
	Link      bool       `json:"link,omitempty"`
	Denied    bool       `json:"denied,omitempty"`
	Partial   bool       `json:"partial,omitempty"`
	Protected bool       `json:"protected,omitempty"`
	Hidden    bool       `json:"hidden,omitempty"`
	System    bool       `json:"system,omitempty"`
	Offline   bool       `json:"offline,omitempty"`
	Kids      []diskItem `json:"kids,omitempty"` // largest children, for the tree map
}

type diskView struct {
	OK      bool         `json:"ok"`
	Root    string       `json:"root"`
	Path    string       `json:"path"`
	Item    diskItem     `json:"item"`
	Items   []diskItem   `json:"items"`
	More    int          `json:"more"`
	Volume  *diskVolume  `json:"volume,omitempty"`
	Scanned diskProgress `json:"scanned"`
	TookMs  int64        `json:"tookMs"`
	Stale   bool         `json:"stale"`
}

// protectedPath: Windows' own folders and files, and other users' data.
// They are marked in the list and cannot be deleted from the app.
func protectedPath(p string) bool {
	l := strings.ToLower(filepath.Clean(p))
	vol := strings.ToLower(filepath.VolumeName(l)) + `\`
	rel := strings.TrimPrefix(l, vol)
	first := rel
	if i := strings.IndexByte(rel, '\\'); i >= 0 {
		first = rel[:i]
	}
	if programFolderOK(rel) {
		return false // a program's own folder (or something in it): it can go, to the Recycle Bin
	}
	switch first {
	case "windows", "program files", "program files (x86)", "programdata", "system volume information", "$recycle.bin",
		"recovery", "boot", "efi", "$windows.~bt", "$windows.~ws", "$winreagent", "config.msi", "msocache", "documents and settings":
		return true
	case "pagefile.sys", "hiberfil.sys", "swapfile.sys", "bootmgr", "bootnxt", "dumpstack.log.tmp", "dumpstack.log":
		return true
	}
	if first == "users" && rel != "users" {
		parts := strings.Split(rel, `\`)
		me := strings.ToLower(filepath.Base(P.profile))
		if len(parts) >= 2 && parts[1] != me && parts[1] != "public" {
			return true // another user's profile
		}
		if len(parts) == 2 { // a profile folder itself
			return true
		}
		if len(parts) >= 3 && parts[2] == "appdata" && len(parts) <= 4 {
			return true // AppData, AppData\Local, ... themselves
		}
	}
	if rel == "" || rel == "users" {
		return true // drive root and C:\Users
	}
	return false
}

func nodeItem(n *dnode) diskItem {
	p := n.path()
	return diskItem{Name: n.name, Path: p, Dir: true, Size: n.size, Alloc: n.alloc, Files: n.files, Dirs: n.dirs, Mtime: n.mtime,
		Link: n.link, Denied: n.denied, Partial: n.partial, Protected: protectedPath(p),
		Hidden: n.attrs&attrHidden != 0, System: n.attrs&attrSystem != 0}
}

// diskChildren lists a scanned folder: its sub-folders (with totals from
// the scan) and its files (read now), largest first.
func diskChildren(path string) (v diskView) {
	dj.mu.Lock()
	defer dj.mu.Unlock()
	if dj.root == nil {
		return v
	}
	if path == "" {
		path = dj.rootPath
	}
	n := dj.root.find(filepath.Clean(path))
	if n == nil {
		return v
	}
	v.OK, v.Root, v.Path, v.Item = true, dj.rootPath, n.path(), nodeItem(n)
	v.TookMs, v.Stale = dj.took.Milliseconds(), dj.stale
	if dj.st != nil {
		v.Scanned = progressOf(dj.st)
	}
	for _, k := range n.kids {
		it := nodeItem(k)
		// largest grandchildren, for the nested tree map
		kids := append([]*dnode(nil), k.kids...)
		sort.Slice(kids, func(i, j int) bool { return kids[i].alloc > kids[j].alloc })
		for i, g := range kids {
			if i >= 10 || g.alloc <= 0 {
				break
			}
			it.Kids = append(it.Kids, diskItem{Name: g.name, Path: g.path(), Dir: true, Size: g.size, Alloc: g.alloc, Files: g.files})
		}
		if k.ownAlloc > 0 && len(k.kids) > 0 {
			it.Kids = append(it.Kids, diskItem{Name: "", Path: "", Size: k.ownSize, Alloc: k.ownAlloc, Files: k.ownFiles})
		}
		v.Items = append(v.Items, it)
	}
	if !n.denied {
		if ents, err := listDirFull(v.Path); err == nil {
			for _, e := range ents {
				if e.dir {
					continue
				}
				ext, cat := fileType(e.name)
				p := filepath.Join(v.Path, e.name)
				v.Items = append(v.Items, diskItem{Name: e.name, Path: p, Size: e.size, Alloc: e.alloc, Mtime: e.mtime, Ext: ext, Cat: cat,
					Protected: protectedPath(p) || e.attrs&attrSystem != 0, Hidden: e.attrs&attrHidden != 0, System: e.attrs&attrSystem != 0,
					Offline: e.offline, Link: e.attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0})
			}
		}
	}
	sort.SliceStable(v.Items, func(i, j int) bool { return v.Items[i].Alloc > v.Items[j].Alloc })
	if len(v.Items) > 3000 {
		v.More = len(v.Items) - 3000
		v.Items = v.Items[:3000]
	}
	// only the scanned drive is asked (a sleeping network drive would stall the list)
	root := filepath.VolumeName(dj.rootPath) + `\`
	if t, f := diskSpace(root); t > 0 {
		v.Volume = &diskVolume{Root: root, Total: t, Free: f}
	}
	return v
}

type diskFilter struct {
	Min   int64  `json:"min"`
	Ext   string `json:"ext"`
	Cat   string `json:"cat"`
	Under string `json:"under"`
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// diskTop returns the largest files of the scan that match the filter.
func diskTop(f diskFilter) []diskItem {
	dj.mu.Lock()
	defer dj.mu.Unlock()
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 300
	}
	under := strings.ToLower(filepath.Clean(f.Under))
	ext := strings.ToLower(strings.TrimSpace(f.Ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []diskItem
	for _, b := range dj.top {
		lp := strings.ToLower(b.Path)
		switch {
		case b.Size < f.Min:
			continue
		case ext != "" && b.Ext != ext:
			continue
		case f.Cat != "" && b.Cat != f.Cat:
			continue
		case f.Under != "" && !strings.HasPrefix(lp, under+`\`) && lp != under:
			continue
		case q != "" && !strings.Contains(strings.ToLower(filepath.Base(b.Path)), q):
			continue
		}
		out = append(out, diskItem{Name: filepath.Base(b.Path), Path: b.Path, Size: b.Size, Alloc: b.Alloc, Mtime: b.Mtime, Ext: b.Ext, Cat: b.Cat,
			Protected: protectedPath(b.Path) || b.Attrs&attrSystem != 0, Hidden: b.Attrs&attrHidden != 0, System: b.Attrs&attrSystem != 0})
		if len(out) >= f.Limit {
			break
		}
	}
	return out
}

type diskTypeRow struct {
	Key string `json:"key"`
	typeStat
}

// diskTypes returns the space per kind of file and the top extensions.
func diskTypes() map[string]any {
	dj.mu.Lock()
	defer dj.mu.Unlock()
	var cats, exts []diskTypeRow
	for k, v := range dj.types {
		cats = append(cats, diskTypeRow{k, *v})
	}
	for k, v := range dj.exts {
		exts = append(exts, diskTypeRow{k, *v})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Alloc > cats[j].Alloc })
	sort.Slice(exts, func(i, j int) bool { return exts[i].Alloc > exts[j].Alloc })
	if len(exts) > 40 {
		exts = exts[:40]
	}
	return map[string]any{"cats": cats, "exts": exts, "stale": dj.stale}
}

// diskRefresh scans one folder again (instead of the whole drive) and puts
// the new numbers in place.
func diskRefresh(path string) bool {
	dj.mu.Lock()
	if dj.running || dj.root == nil {
		dj.mu.Unlock()
		return false
	}
	n := dj.root.find(filepath.Clean(path))
	if n == nil {
		dj.mu.Unlock()
		return false
	}
	dj.running = true
	dj.stop.Store(false)
	st := &scanStats{}
	dj.st, dj.started = st, time.Now()
	p := n.path()
	dj.mu.Unlock()
	go func() {
		var fresh *dnode
		var top []bigFile
		func() {
			defer func() { _ = recover() }()
			fresh, _, _, top = scanTree(p, listDirFull, scanWorkers(), &dj.stop, st)
		}()
		dj.mu.Lock()
		ok := fresh != nil && !dj.stop.Load()
		if ok {
			if cur := dj.root.find(p); cur != nil {
				replaceSubtree(cur, fresh)
				// largest files: those below the folder come from the new scan
				lp := strings.ToLower(p) + `\`
				var keep []bigFile
				for _, b := range dj.top {
					if !strings.HasPrefix(strings.ToLower(b.Path), lp) {
						keep = append(keep, b)
					}
				}
				t := &topN{n: 1000}
				for _, b := range append(keep, top...) {
					t.offer(b)
				}
				dj.top = t.sorted()
				dj.stale = true
			}
		}
		dj.running = false
		dj.mu.Unlock()
		emit("disk-done", map[string]any{"ok": ok, "refresh": true, "path": p, "progress": progressOf(st)})
	}()
	return true
}

type diskDeleteResult struct {
	Path  string `json:"path"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

var (
	procSHFileOperationW = shell32.NewProc("SHFileOperationW")
)

type shFileOpStruct struct {
	hwnd     uintptr
	wFunc    uint32
	pFrom    *uint16
	pTo      *uint16
	fFlags   uint16
	aborted  int32
	nameMaps uintptr
	title    *uint16
}

// diskDelete moves the chosen files and folders to the Recycle Bin, after
// the page showed exactly what was chosen. Anything protected, not fully
// read, in use, or outside the scanned folder is refused.
func diskDelete(paths []string) []diskDeleteResult {
	op := beginOp("disk", "", T("log.diskDel"), map[string]any{"count": len(paths)})
	var out []diskDeleteResult
	var moved []delResult
	var freed int64
	for _, p := range paths {
		r := diskDeleteResult{Path: p}
		size, err := diskDeleteOne(p)
		if err != nil {
			r.Error = err.Error()
			op.warnf(p, r.Error, nil)
		} else {
			r.OK = true
			freed += size
			moved = append(moved, delResult{Path: filepath.Clean(p), OK: true, Size: size})
			op.info(p, T("log.recycled"), map[string]any{"size": size})
		}
		out = append(out, r)
	}
	noteRecycled("disk", moved)
	op.end(T("log.delSum", len(moved), len(paths), sizeText(freed)), map[string]any{"freed": freed})
	return out
}

func diskDeleteOne(p string) (int64, error) {
	p = filepath.Clean(p)
	dj.mu.Lock()
	defer dj.mu.Unlock()
	if dj.running {
		return 0, errors.New(T("da.err.busy"))
	}
	if dj.root == nil || !filepath.IsAbs(p) {
		return 0, errors.New(T("da.err.unknown"))
	}
	root := strings.ToLower(filepath.Clean(dj.rootPath))
	lp := strings.ToLower(p)
	if lp == root || !(strings.HasPrefix(lp, strings.TrimSuffix(root, `\`)+`\`)) {
		return 0, errors.New(T("da.err.outside"))
	}
	if protectedPath(p) {
		return 0, errors.New(T("da.err.protected"))
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, errors.New(T("da.err.gone"))
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && d.FileAttributes&attrSystem != 0 {
		return 0, errors.New(T("da.err.protected"))
	}
	dir := dj.root.find(filepath.Dir(p))
	var node *dnode
	if fi.IsDir() && !isLink(fi) {
		node = dj.root.find(p)
		if node == nil {
			return 0, errors.New(T("da.err.unknown"))
		}
		if node.partial {
			return 0, errors.New(T("da.err.partial"))
		}
	} else if k := probeDelete(p); k == "locked" {
		return 0, errors.New(T("da.err.inuse"))
	} else if k == "denied" {
		return 0, errors.New(T("da.err.denied"))
	}
	if err := recycle(p); err != nil {
		return 0, err
	}
	if _, err := os.Lstat(p); err == nil {
		return 0, errors.New(T("da.err.notdone"))
	}
	// update the tree in place
	if node != nil {
		removeNode(node)
	} else if dir != nil {
		alloc := fi.Size()
		removeFile(dir, fi.Size(), alloc)
	}
	var keep []bigFile
	for _, b := range dj.top {
		bl := strings.ToLower(b.Path)
		if bl != lp && !strings.HasPrefix(bl, lp+`\`) {
			keep = append(keep, b)
		}
	}
	dj.top = keep
	dj.stale = true
	if node != nil {
		return node.size, nil
	}
	return fi.Size(), nil
}

// recycle sends one item to the Recycle Bin. Windows asks itself if an item
// is too big for the Bin and would be deleted for good.
func recycle(p string) error {
	from, err := windows.UTF16FromString(p) // ends in one NUL ...
	if err != nil {
		return err
	}
	from = append(from, 0)                                                                                // ... the list needs two
	op := shFileOpStruct{hwnd: app.hwnd, wFunc: 3, pFrom: &from[0], fFlags: 0x40 | 0x10 | 0x4000 | 0x400} // FO_DELETE; ALLOWUNDO, NOCONFIRMATION, WANTNUKEWARNING, NOERRORUI
	var r uintptr
	withCOM(func() {
		r, _, _ = procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	})
	runtime.KeepAlive(from)
	if r != 0 {
		return errors.New(T("da.err.code", int(r)))
	}
	if op.aborted != 0 {
		return errors.New(T("da.err.aborted"))
	}
	return nil
}
