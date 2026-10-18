//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileIdentity: the volume and file index. Two names with the same identity
// are one file (hard links).
func fileIdentity(path string) (string, bool) {
	p, err := windows.UTF16PtrFromString(longName(path))
	if err != nil {
		return "", false
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	var fi windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &fi) != nil {
		return "", false
	}
	if fi.NumberOfLinks <= 1 {
		return "", false // no other name: nothing to compare
	}
	return fmt.Sprintf("%x:%x:%x", fi.VolumeSerialNumber, fi.FileIndexHigh, fi.FileIndexLow), true
}

// skipSystemDir: folders the finders leave out unless asked (Windows, the
// program folders, ProgramData, AppData and the hidden system folders).
func skipSystemDir(dir, name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "$recycle.bin", "system volume information", "$windows.~bt", "$windows.~ws", "$winreagent", "config.msi",
		"recovery", "msocache", "appdata", "$sysreset", "$getcurrent":
		return true
	}
	if isVolumeRoot(dir) {
		switch n {
		case "windows", "program files", "program files (x86)", "programdata", "windows.old", "perflogs":
			return true
		}
	}
	return strings.EqualFold(filepath.Join(dir, name), windowsDir())
}

func isVolumeRoot(dir string) bool {
	c := filepath.Clean(dir)
	return len(c) <= 3 && strings.HasSuffix(c, `\`) || filepath.VolumeName(c) == c
}

func winFinderFS() finderFS {
	return finderFS{
		list:     listDirFull,
		identity: fileIdentity,
		protected: func(p string, attrs uint32) bool {
			return protectedPath(p) || attrs&attrSystem != 0 || underWindowsDir(p)
		},
		recycle: recycleMany,
		inUse:   probeDelete,
		skipDir: skipSystemDir,
	}
}

// recycleMany moves several items to the Recycle Bin in one call.
func recycleMany(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	var from []uint16
	for _, p := range paths {
		u, err := windows.UTF16FromString(p) // ends in one NUL
		if err != nil {
			return err
		}
		from = append(from, u...)
	}
	from = append(from, 0) // the list ends with a second NUL
	op := shFileOpStruct{hwnd: app.hwnd, wFunc: 3, pFrom: &from[0], fFlags: 0x40 | 0x10 | 0x4000 | 0x400}
	var r uintptr
	withCOM(func() {
		r, _, _ = procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	})
	runtime.KeepAlive(from)
	if r != 0 {
		return fmt.Errorf("%s", T("da.err.code", int(r)))
	}
	if op.aborted != 0 {
		return fmt.Errorf("%s", T("da.err.aborted"))
	}
	return nil
}

// ---------------------------------------------------------------- bindings

type findPlace struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Kind  string `json:"kind"` // folder, drive
	Label string `json:"label,omitempty"`
	Total uint64 `json:"total,omitempty"`
	Free  uint64 `json:"free,omitempty"`
}

// findPlaces: the user's main folders and the drives, as starting points.
func findPlaces() []findPlace {
	var out []findPlace
	for _, k := range []struct {
		id string
		f  *windows.KNOWNFOLDERID
	}{
		{"downloads", windows.FOLDERID_Downloads}, {"documents", windows.FOLDERID_Documents}, {"desktop", windows.FOLDERID_Desktop},
		{"pictures", windows.FOLDERID_Pictures}, {"videos", windows.FOLDERID_Videos}, {"music", windows.FOLDERID_Music},
	} {
		if p, err := windows.KnownFolderPath(k.f, 0); err == nil && dirExists(p) {
			out = append(out, findPlace{ID: k.id, Path: p, Kind: "folder"})
		}
	}
	if P.profile != "" {
		out = append(out, findPlace{ID: "profile", Path: P.profile, Kind: "folder"})
	}
	for _, v := range diskVolumes() {
		if v.Kind == "network" {
			continue
		}
		out = append(out, findPlace{ID: "drive-" + strings.ToLower(v.Root[:1]), Path: v.Root, Kind: "drive", Label: v.Label, Total: v.Total, Free: v.Free})
	}
	return out
}

func findStart(kind string, o findOpts) bool {
	var roots []string
	for _, r := range o.Roots {
		if r = strings.TrimSpace(r); r != "" && filepath.IsAbs(r) {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		return false
	}
	o.Roots = roots
	enableBackupPrivilege()
	return fjob.start(kind, o, winFinderFS(), emit)
}

func findCancel() bool                    { return fjob.cancel() }
func findResultOf(kind string) findResult { return fjob.result(kind) }
func findPickFolder() string              { return pickFolder(T("find.pickTitle")) }

// trKey translates an error that is a text key, and leaves other errors as
// they are.
func trKey(s string) string {
	if _, ok := texts[s]; ok {
		return T(s)
	}
	return s
}

// findDelete moves the chosen files of the last search to the Recycle Bin.
func findDelete(kind string, paths []string) []delResult {
	if kind != "dupes" && kind != "large" {
		return nil
	}
	src := map[string]string{"dupes": "dupes", "large": "files"}[kind]
	title := map[string]string{"dupes": T("log.dupDel"), "large": T("log.largeDel")}[kind]
	op := beginOp(src, "", title, map[string]any{"count": len(paths)})
	var res []delResult
	func() {
		defer func() {
			if r := recover(); r != nil {
				op.panicked(r)
			}
		}()
		res = fjob.delete(kind, paths, winFinderFS())
	}()
	var freed int64
	n := 0
	for i := range res {
		r := &res[i]
		r.Error = trKey(r.Error)
		if r.OK {
			n++
			freed += r.Size
			op.info(r.Path, T("log.recycled"), map[string]any{"size": r.Size})
		} else {
			op.warnf(r.Path, r.Error, nil)
		}
	}
	noteRecycled(kind, res)
	op.end(T("log.delSum", n, len(paths), sizeText(freed)), map[string]any{"freed": freed})
	return res
}

func recycleBinOf(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return ""
	}
	return filepath.Join(vol+`\`, "$Recycle.Bin", currentUserSID())
}

var mySIDCache string

func currentUserSID() string {
	if mySIDCache == "" {
		mySIDCache = processUserSID(windows.GetCurrentProcessId())
	}
	return mySIDCache
}

func dataDirPath(name string) string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", name)
}
