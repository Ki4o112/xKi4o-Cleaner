//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

type updateResult struct {
	OK      bool   `json:"ok"`
	Restart bool   `json:"restart"` // the new version is starting; this one closes
	Error   string `json:"error,omitempty"`
}

// installUpdate downloads the newer version found by checkUpdate, checks
// it, puts it in place of this exe and starts it. Only on the user's click.
func installUpdate() updateResult {
	upd.mu.Lock()
	info, a := upd.info, upd.asset
	if upd.busy {
		upd.mu.Unlock()
		return updateResult{Error: T("upd.err.busy")}
	}
	if !info.Newer || a == nil {
		upd.mu.Unlock()
		return updateResult{Error: T("upd.err.nothing")}
	}
	upd.busy = true
	upd.mu.Unlock()
	defer func() {
		upd.mu.Lock()
		upd.busy = false
		upd.mu.Unlock()
	}()
	app.working.Store(true) // closing the window asks first
	defer app.working.Store(false)

	op := beginOp("update", "", T("log.update", info.Latest), map[string]any{"from": appVersion, "to": info.Latest, "size": a.Size, "sha256": a.Digest})
	fail := func(err error) updateResult {
		msg := updateErrText(err)
		op.fail("", msg, nil)
		op.end("", nil)
		return updateResult{Error: msg}
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	tmp := updateTemp(exe)
	var last time.Time
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err = downloadVerified(ctx, *a, tmp, func(done, total int64) {
		if time.Since(last) > 150*time.Millisecond || done == total {
			last = time.Now()
			emit("update-progress", map[string]int64{"done": done, "total": total})
		}
	})
	if err != nil {
		return fail(err)
	}
	op.info("", T("log.updChecked"), nil)
	if err := swapExe(exe, tmp); err != nil {
		_ = os.Remove(tmp)
		return fail(err)
	}
	op.info(exe, T("log.updSwapped"), nil)
	// the new version waits for this one to close, then starts
	cmd := exec.CommandContext(context.Background(), exe, "--after-update="+strconv.Itoa(os.Getpid())) //nolint:gosec // our own, just-checked exe
	if err := cmd.Start(); err != nil {
		op.warnf("", T("upd.err.start"), map[string]any{"error": err.Error()})
		op.end(T("log.updDone", info.Latest), nil)
		return updateResult{OK: true, Error: T("upd.err.start")}
	}
	_ = cmd.Process.Release()
	op.end(T("log.updDone", info.Latest), nil)
	go func() {
		time.Sleep(600 * time.Millisecond) // the page shows "restarting" first
		app.w.Dispatch(func() { app.w.Terminate() })
	}()
	return updateResult{OK: true, Restart: true}
}

// afterUpdate: started by the old version, wait until it has closed (it
// holds the single-instance lock and the WebView2 profile).
func afterUpdate(args []string) bool {
	for _, a := range args {
		s, ok := strings.CutPrefix(a, "--after-update=")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(s)
		if err != nil || pid <= 0 {
			return true
		}
		if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)); err == nil {
			_, _ = windows.WaitForSingleObject(h, 20000)
			_ = windows.CloseHandle(h)
		}
		return true
	}
	return false
}

// changelog is the list of changes, shipped inside the exe.
func changelog() string { return changelogMD }

// openReleasePage opens a page of this app's releases on GitHub (nothing else).
func openReleasePage(u string) bool {
	if !strings.HasPrefix(u, "https://github.com/"+updateRepo+"/releases") {
		u = "https://github.com/" + updateRepo + "/releases/latest"
	}
	shellOpen(u)
	return true
}

// updOff: the update check is turned off in Settings.
var updOff atomic.Bool

// updateLoop looks for a new version every 3 hours while the app runs (the
// page looks once when it opens), also while it waits in the tray. A new one
// is told once: a Windows notification, and the page shows the update.
func updateLoop() {
	defer recoverLog("update check")
	told := ""
	for {
		time.Sleep(updEvery)
		upd.mu.Lock()
		busy := upd.busy
		upd.mu.Unlock()
		if updOff.Load() || busy {
			continue
		}
		v := checkUpdate()
		if !v.Newer || v.Latest == told {
			continue
		}
		told = v.Latest
		emit("upd-found", v)
		if app.w != nil {
			app.w.Dispatch(func() {
				if trayNotify(T("upd.nTitle", v.Latest), T("upd.nText", appVersion)) {
					win.noteUpd.Store(true)
				}
			})
		}
	}
}

const updEvery = 3 * time.Hour
