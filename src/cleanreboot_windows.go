//go:build windows

package main

// Files a program still holds open cannot be deleted now; Windows can delete
// them at the next restart, before any program starts (MoveFileEx with
// MOVEFILE_DELAY_UNTIL_REBOOT). Only files inside the folders the cleaner
// itself cleans are accepted.

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// cleanRoots: the folders (and files) the catalog cleans, normalized.
func cleanRoots() map[string]bool {
	roots := map[string]bool{}
	for _, it := range catalog {
		if it.targets == nil || !it.available() {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			for _, t := range it.targets() {
				roots[normPath(t.Path)] = true
			}
		}()
	}
	return roots
}

// insideRoots: p is one of the roots or inside one.
func insideRoots(p string, roots map[string]bool) bool {
	k := normPath(p)
	return roots[k] || insideAny(k, roots)
}

// cleanAtReboot asks Windows to delete these files at the next restart. It
// returns how many were accepted.
func cleanAtReboot(paths []string) map[string]any {
	if !app.admin {
		return map[string]any{"ok": false, "error": T("blocked.admin")}
	}
	roots := cleanRoots()
	op := beginOp("clean", "", T("log.atReboot", len(paths)), nil)
	n := 0
	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) || !insideRoots(p, roots) || !safeTarget(p) {
			continue
		}
		if fi, err := os.Lstat(p); err != nil || fi.IsDir() {
			continue
		}
		u, err := windows.UTF16PtrFromString(p)
		if err != nil {
			continue
		}
		if err := windows.MoveFileEx(u, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
			op.warnf(p, err.Error(), nil)
			continue
		}
		n++
	}
	op.end(T("log.atRebootDone", n), nil)
	return map[string]any{"ok": n > 0, "count": n}
}
