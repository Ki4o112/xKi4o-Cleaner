//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Quick fixes on the Repair page: small repairs Windows has itself, each
// only after the user confirms it. None deletes personal files: the icon
// fix removes Windows' own icon cache, which Explorer builds again.

type qfResult struct {
	OK      bool   `json:"ok"`
	Reboot  bool   `json:"reboot,omitempty"`
	Removed int    `json:"removed,omitempty"`
	Error   string `json:"error,omitempty"`
}

var quickFixIDs = map[string]bool{"explorer": true, "icons": true, "store": true, "winsock": true}

// iconCacheFiles: Explorer's icon cache (never the thumbnails or anything else).
func iconCacheFiles() []string {
	if P.local == "" {
		return nil
	}
	out := []string{filepath.Join(P.local, "IconCache.db")}
	m, _ := filepath.Glob(filepath.Join(P.local, `Microsoft\Windows\Explorer`, "iconcache_*.db"))
	return append(out, m...)
}

func killExplorer() {
	for pid := range procPIDs(map[string]bool{"explorer.exe": true}) {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

func quickFix(id string) (res qfResult) {
	if !quickFixIDs[id] {
		return qfResult{Error: T("qf.err.unknown")}
	}
	if (id == "winsock") && !app.admin {
		return qfResult{Error: T("blocked.admin")}
	}
	op := beginOp("repair", "", T("qf."+id+".log"), nil)
	defer func() {
		if r := recover(); r != nil {
			res = qfResult{Error: T("err.unexpected", r)}
			op.panicked(r)
		}
		if res.Error != "" {
			op.fail("", res.Error, nil)
		}
		op.end("", map[string]any{"ok": res.OK, "removed": res.Removed, "reboot": res.Reboot})
	}()
	switch id {
	case "explorer":
		res.OK = restartExplorer()
	case "icons":
		killExplorer()
		// Windows starts Explorer again by itself within a moment: delete
		// while it is gone, a few tries for files it still holds.
		files := iconCacheFiles()
		done := map[string]bool{}
		for try := 0; try < 10 && len(done) < len(files); try++ {
			for _, f := range files {
				if done[f] {
					continue
				}
				err := os.Remove(f)
				if err == nil {
					res.Removed++
				}
				if err == nil || os.IsNotExist(err) {
					done[f] = true
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		_, _, _ = runQuiet(15*time.Second, sys32("ie4uinit.exe"), "-show")
		if len(procPIDs(map[string]bool{"explorer.exe": true})) == 0 {
			restartExplorer()
		}
		res.OK = true
	case "store":
		_, code, err := runQuiet(90*time.Second, sys32("WSReset.exe"))
		res.OK = err == nil && code == 0
		if !res.OK {
			res.Error = T("qf.err.run", "WSReset", errText(err, code))
		}
	case "winsock":
		out, code, err := runQuiet(30*time.Second, sys32("netsh.exe"), "winsock", "reset")
		res.OK = err == nil && code == 0
		if !res.OK {
			res.Error = T("qf.err.run", "netsh", strings.TrimSpace(out+" "+errText(err, code)))
		}
		flushDNSCache()
		res.Reboot = res.OK
	}
	return res
}

func errText(err error, code int) string {
	if err != nil {
		return err.Error()
	}
	if code != 0 {
		return T("qf.code", code)
	}
	return ""
}
