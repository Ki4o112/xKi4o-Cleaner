//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func reparseDir(_ string, e os.DirEntry) bool {
	fi, err := e.Info()
	if err != nil {
		return true // unknown: left alone
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

type tidyPlace struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Def  bool   `json:"def"`  // ticked at first
	Warn bool   `json:"warn"` // programs keep folders there (AppData)
}

// tidyPlaces: where empty folders are looked for. Windows' and programs'
// own folders are not offered (programs expect their empty folders there).
func tidyPlaces() []tidyPlace {
	var out []tidyPlace
	add := func(id string, f *windows.KNOWNFOLDERID, def, warn bool) {
		if p := knownFolder(f, ""); p != "" && dirExists(p) {
			out = append(out, tidyPlace{ID: id, Path: p, Def: def, Warn: warn})
		}
	}
	add("desktop", windows.FOLDERID_Desktop, true, false)
	add("documents", windows.FOLDERID_Documents, true, false)
	add("downloads", windows.FOLDERID_Downloads, true, false)
	add("pictures", windows.FOLDERID_Pictures, true, false)
	add("music", windows.FOLDERID_Music, true, false)
	add("videos", windows.FOLDERID_Videos, true, false)
	add("roaming", windows.FOLDERID_RoamingAppData, false, true)
	add("local", windows.FOLDERID_LocalAppData, false, true)
	return out
}

// tidySkip: folders of Windows and of this app, never looked into.
func tidySkip() []string {
	return []string{P.temp, dataDirPath(""), filepath.Join(P.local, "Temp"), filepath.Join(P.local, "Packages"), filepath.Join(P.local, "Microsoft"),
		filepath.Join(P.roaming, "Microsoft"), filepath.Join(P.local, "Programs"), filepath.Join(P.local, "Comms"), filepath.Join(P.local, "ConnectedDevicesPlatform")}
}

// tidyProtected: never proposed (nor anything holding them).
func tidyProtected() []string {
	out := []string{windowsDir(), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), P.programData, P.profile}
	for _, f := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Desktop, windows.FOLDERID_Documents, windows.FOLDERID_Downloads, windows.FOLDERID_Pictures,
		windows.FOLDERID_Music, windows.FOLDERID_Videos, windows.FOLDERID_Favorites, windows.FOLDERID_SavedGames, windows.FOLDERID_Links,
		windows.FOLDERID_Contacts, windows.FOLDERID_SkyDrive, windows.FOLDERID_CameraRoll, windows.FOLDERID_SavedPictures,
		windows.FOLDERID_Screenshots, windows.FOLDERID_PublicDesktop, windows.FOLDERID_StartMenu, windows.FOLDERID_CommonStartMenu, windows.FOLDERID_RoamingAppData,
		windows.FOLDERID_LocalAppData, windows.FOLDERID_LocalAppDataLow} {
		if p := knownFolder(f, ""); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tidyRootOK: a folder the user may search (not a drive, Windows or a program folder).
func tidyRootOK(p string) bool {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || len(p) <= 3 || !dirExists(p) {
		return false
	}
	for _, x := range []string{windowsDir(), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), P.programData} {
		if x != "" && under(p, x) {
			return false
		}
	}
	return true
}

var tidyLast struct {
	mu     sync.Mutex
	empty  map[string]bool
	broken map[string]bool
}

type emptyResult struct {
	Dirs    []emptyDir `json:"dirs"`
	Partial bool       `json:"partial"` // stopped at the time or count limit
}

func emptyScan(roots []string) emptyResult {
	res := emptyResult{Dirs: []emptyDir{}}
	prot := tidyProtected()
	deadline := time.Now().Add(90 * time.Second)
	const limit = 5000
	seen := map[string]bool{}
	for _, r := range roots {
		if !tidyRootOK(r) {
			continue
		}
		for _, d := range findEmptyDirs(osEfFS{}, filepath.Clean(r), prot, tidySkip(), deadline, limit) {
			if k := strings.ToLower(d.Path); !seen[k] {
				seen[k] = true
				res.Dirs = append(res.Dirs, d)
			}
		}
	}
	res.Partial = time.Now().After(deadline) || len(res.Dirs) >= limit
	tidyLast.mu.Lock()
	tidyLast.empty = map[string]bool{}
	for _, d := range res.Dirs {
		tidyLast.empty[strings.ToLower(d.Path)] = true
	}
	tidyLast.mu.Unlock()
	return res
}

func driveFixed(root string) bool {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return false
	}
	return windows.GetDriveType(p) == windows.DRIVE_FIXED
}

func brokenScan() []brokenLink {
	out := []brokenLink{}
	desktop := knownFolder(windows.FOLDERID_Desktop, filepath.Join(P.profile, "Desktop"))
	public := knownFolder(windows.FOLDERID_PublicDesktop, "")
	places := []struct {
		dir, where string
		deep       bool
	}{
		{desktop, "desktop", false}, {public, "desktop", false},
		{knownFolder(windows.FOLDERID_StartMenu, filepath.Join(P.roaming, `Microsoft\Windows\Start Menu`)), "startmenu", true},
		{knownFolder(windows.FOLDERID_CommonStartMenu, filepath.Join(P.programData, `Microsoft\Windows\Start Menu`)), "startmenu", true},
		{filepath.Join(P.roaming, `Microsoft\Internet Explorer\Quick Launch`), "quicklaunch", false},
	}
	drives := map[string]bool{}
	driveOK := func(d string) bool {
		v, ok := drives[d]
		if !ok {
			v = driveFixed(d)
			drives[d] = v
		}
		return v
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	seen := map[string]bool{}
	check := func(p, where string) {
		if seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		li, err := readLnkFile(p)
		if err != nil {
			return
		}
		if shortcutBroken(li, exists, driveOK) {
			out = append(out, brokenLink{Path: p, Target: li.Target, Where: where})
		}
	}
	for _, pl := range places {
		if pl.dir == "" {
			continue
		}
		if !pl.deep {
			ents, _ := os.ReadDir(pl.dir)
			for _, e := range ents {
				if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".lnk") {
					check(filepath.Join(pl.dir, e.Name()), pl.where)
				}
			}
			continue
		}
		n := 0
		_ = filepath.WalkDir(pl.dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // an unreadable folder is skipped, the walk goes on
			}
			if n++; n > 20000 {
				return filepath.SkipAll
			}
			if d.IsDir() && d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".lnk") {
				check(p, pl.where)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Where != out[j].Where {
			return out[i].Where < out[j].Where
		}
		return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
	})
	tidyLast.mu.Lock()
	tidyLast.broken = map[string]bool{}
	for _, b := range out {
		tidyLast.broken[strings.ToLower(b.Path)] = true
	}
	tidyLast.mu.Unlock()
	return out
}

// stillEmpty: the folder holds no file at all (looked at again right before removing it).
func stillEmpty(p string) bool {
	ok := true
	n := 0
	_ = filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
		if err != nil || n > 10000 {
			ok = false
			return filepath.SkipAll
		}
		n++
		if q == p {
			return nil
		}
		if !d.IsDir() || d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 || reparseDir(q, d) {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return ok
}

// tidyDelete moves the chosen empty folders or broken shortcuts of the last
// scan to the Recycle Bin, each checked again first.
func tidyDelete(kind string, paths []string) []delResult {
	tidyLast.mu.Lock()
	allowed := tidyLast.empty
	if kind == "lnk" {
		allowed = tidyLast.broken
	}
	tidyLast.mu.Unlock()
	op := beginOp("tidy", "", T(map[bool]string{true: "log.tidyLnk", false: "log.tidyDirs"}[kind == "lnk"]), map[string]any{"count": len(paths)})
	var res []delResult
	prot := tidyProtected()
	for _, p := range paths {
		r := delResult{Path: p}
		switch {
		case !allowed[strings.ToLower(p)]:
			r.Error = T("tidy.err.notFound")
		case kind == "dir" && (protectedFor(p, prot) || protectedFor(p, tidySkip()) || !stillEmpty(p)):
			r.Error = T("tidy.err.notEmpty")
		case kind == "lnk":
			li, err := readLnkFile(p)
			if err != nil || !shortcutBroken(li, func(t string) bool { _, e := os.Stat(t); return e == nil }, driveFixed) {
				r.Error = T("tidy.err.works")
			}
		}
		if r.Error == "" {
			if err := recycleMany([]string{p}); err != nil {
				r.Error = err.Error()
			} else if _, err := os.Lstat(p); err == nil {
				r.Error = T("un.err.stayed")
			} else {
				r.OK = true
			}
		}
		if r.OK {
			op.info(p, T("log.tidyRemoved"), nil)
		} else {
			op.warnf(p, r.Error, nil)
		}
		res = append(res, r)
	}
	noteRecycled("tidy", res)
	n := 0
	for _, r := range res {
		if r.OK {
			n++
		}
	}
	op.end(T("log.tidyDone", n, len(res)), nil)
	return res
}
