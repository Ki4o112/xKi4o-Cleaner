//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const everyoneSID = "*S-1-1-0"

var lockMu sync.Mutex

type lockView struct {
	Path   string `json:"path"`
	At     int64  `json:"at"`
	Exists bool   `json:"exists"`
}

type lockResult struct {
	OK    bool       `json:"ok"`
	Error string     `json:"error,omitempty"`
	List  []lockView `json:"list"`
}

func lockFile() string { return dataDirPath("locked.json") }

func lockLoad() []lockedFolder {
	var ls []lockedFolder
	if b, err := os.ReadFile(lockFile()); err == nil {
		_ = json.Unmarshal(b, &ls)
	}
	return ls
}

func lockSave(ls []lockedFolder) error {
	b, _ := json.MarshalIndent(ls, "", "  ")
	_ = os.MkdirAll(filepath.Dir(lockFile()), 0o755)
	return os.WriteFile(lockFile(), b, 0o600)
}

func lockViews(ls []lockedFolder) []lockView {
	out := []lockView{}
	for _, l := range ls {
		_, err := os.Lstat(l.Path) // the folder itself can still be seen (only its content is denied)
		out = append(out, lockView{Path: l.Path, At: l.At, Exists: err == nil})
	}
	return out
}

func lockList() lockResult {
	lockMu.Lock()
	defer lockMu.Unlock()
	return lockResult{OK: true, List: lockViews(lockLoad())}
}

func setHidden(path string, on bool) {
	p, _ := windows.UTF16PtrFromString(path)
	a, err := windows.GetFileAttributes(p)
	if err != nil {
		return
	}
	if on {
		a |= windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM
	} else {
		a &^= windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM
	}
	_ = windows.SetFileAttributes(p, a)
}

// lockFolder hides the folder and denies everyone any access to it.
func lockFolder(path, pw string) lockResult {
	lockMu.Lock()
	defer lockMu.Unlock()
	res := lockResult{List: lockViews(lockLoad())}
	path = filepath.Clean(path)
	exe, _ := os.Executable()
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		res.Error = T("lk.err.folder")
		return res
	}
	if !lockTargetOK(path, protectedPath(path), P.profile, filepath.Dir(exe), filepath.Dir(lockFile()), P.local) {
		res.Error = T("lk.err.target")
		return res
	}
	if len(pw) < 4 {
		res.Error = T("lk.err.short")
		return res
	}
	ls := lockLoad()
	for _, l := range ls {
		if strings.EqualFold(l.Path, path) {
			res.Error = T("lk.err.already")
			return res
		}
	}
	op := beginOp("lock", "", T("log.lock", path), nil)
	// the password first: if anything below fails, the folder can still be opened from here
	ls = append(ls, newLock(path, pw, time.Now().UnixMilli()))
	if err := lockSave(ls); err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		res.Error = err.Error()
		return res
	}
	setHidden(path, true)
	out, code, err := runQuiet(time.Minute, sys32("icacls.exe"), path, "/deny", everyoneSID+":(OI)(CI)F")
	if err != nil || code != 0 {
		setHidden(path, false)
		_ = lockSave(ls[:len(ls)-1])
		res.Error = T("lk.err.acl", strings.TrimSpace(out+" "+errText(err, code)))
		op.fail("", res.Error, nil)
		op.end("", nil)
		res.List = lockViews(lockLoad())
		return res
	}
	op.end("", nil)
	return lockResult{OK: true, List: lockViews(ls)}
}

// unlockFolder gives the access back after the right password.
func unlockFolder(path, pw string) lockResult {
	lockMu.Lock()
	defer lockMu.Unlock()
	ls := lockLoad()
	res := lockResult{List: lockViews(ls)}
	i := -1
	for k, l := range ls {
		if strings.EqualFold(l.Path, filepath.Clean(path)) {
			i = k
		}
	}
	if i < 0 {
		res.Error = T("lk.err.unknown")
		return res
	}
	if !ls[i].check(pw) {
		time.Sleep(time.Second) // guessing stays slow
		res.Error = T("lk.err.pw")
		return res
	}
	op := beginOp("lock", "", T("log.unlock", ls[i].Path), nil)
	if err := denyRemove(ls[i].Path); err != nil {
		res.Error = T("lk.err.acl", err.Error())
		op.fail("", res.Error, nil)
		op.end("", nil)
		return res
	}
	setHidden(ls[i].Path, false)
	ls = append(ls[:i], ls[i+1:]...)
	_ = lockSave(ls)
	op.end("", nil)
	return lockResult{OK: true, List: lockViews(ls)}
}

// denyRemove takes the deny entry away; when Windows refuses, the
// administrators take ownership first and try again.
func denyRemove(path string) error {
	if _, gone := os.Lstat(path); gone != nil {
		return nil
	}
	_, code, err := runQuiet(time.Minute, sys32("icacls.exe"), path, "/remove:d", everyoneSID)
	if err == nil && code == 0 {
		return nil
	}
	_, _, _ = runQuiet(time.Minute, sys32("takeown.exe"), "/F", path, "/A")
	out, code, err := runQuiet(time.Minute, sys32("icacls.exe"), path, "/remove:d", everyoneSID)
	if err == nil && code == 0 {
		return nil
	}
	return errors.New(strings.TrimSpace(out + " " + errText(err, code)))
}
