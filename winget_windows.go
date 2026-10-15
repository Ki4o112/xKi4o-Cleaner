//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// winget is the Windows Package Manager (part of "App Installer"). Updates
// are found and installed by it; this app only shows them and starts it.

type wgList struct {
	Available bool       `json:"available"` // winget is installed
	Updates   []wgUpdate `json:"updates"`
	Error     string     `json:"error,omitempty"`
	Checked   int64      `json:"checked"` // unix ms
}

var wg struct {
	mu      sync.Mutex // one winget at a time
	cache   wgList
	running atomic.Bool
	stop    atomic.Bool
}

func wingetExe() string {
	if p := filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\WindowsApps\winget.exe`); fileExistsAny(p) {
		return p
	}
	if p, err := exec.LookPath("winget.exe"); err == nil {
		return p
	}
	return ""
}

// runWinget runs winget hidden; its output is UTF-8 (older builds: the console code page).
func runWinget(timeout time.Duration, args ...string) (string, int, error) {
	exe := wingetExe()
	if exe == "" {
		return "", -1, os.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hiddenCmd(ctx, exe, args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	b := []byte(out.String())
	s := string(b)
	if !utf8.Valid(b) {
		s = oemDecode(b)
	}
	return s, code, err
}

// wingetUpdates asks winget which installed programs have a newer version.
// fresh=false gives the last answer if it is under 30 minutes old.
func wingetUpdates(fresh bool) wgList {
	if !fresh {
		wg.mu.Lock()
		c := wg.cache
		wg.mu.Unlock()
		if c.Checked > 0 && time.Since(time.UnixMilli(c.Checked)) < 30*time.Minute {
			return c
		}
	}
	if wg.running.Load() {
		wg.mu.Lock()
		c := wg.cache
		wg.mu.Unlock()
		return c
	}
	wg.mu.Lock()
	defer wg.mu.Unlock()
	res := wgList{Checked: time.Now().UnixMilli(), Updates: []wgUpdate{}}
	if wingetExe() == "" {
		wg.cache = res
		return res
	}
	res.Available = true
	out, code, err := runWinget(3*time.Minute, "upgrade", "--accept-source-agreements")
	if err != nil && code == -1 {
		res.Error = err.Error()
		wg.cache = res
		return res
	}
	ups := parseWingetUpgrade(out)
	const noUpdates = 0x8A15002B // "no applicable upgrade found"
	if len(ups) == 0 && code != 0 && code != noUpdates && !strings.Contains(out, "-----") {
		res.Error = wingetMessage(out) // no table: a real failure
	}
	for _, u := range ups {
		if strings.HasSuffix(u.ID, "…") || strings.HasSuffix(u.ID, "...") {
			tmp := filepath.Join(os.TempDir(), "xki4o-winget-export.json")
			_, _, _ = runWinget(2*time.Minute, "export", "-o", tmp, "--accept-source-agreements")
			b, _ := os.ReadFile(tmp)
			_ = os.Remove(tmp)
			ups = fullIDs(ups, b)
			break
		}
	}
	if ups != nil {
		res.Updates = ups
	}
	wg.cache = res
	return res
}

// wingetUpgrade updates the programs one after another. The page gets a
// "winget" event for each: run, then ok or fail with winget's own message.
func wingetUpgrade(ids []string) bool {
	if len(ids) == 0 || wingetExe() == "" || !wg.running.CompareAndSwap(false, true) {
		return false
	}
	wg.stop.Store(false)
	var clean []string
	for _, id := range ids {
		if validWingetID(id) {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		wg.running.Store(false)
		return false
	}
	app.working.Store(true)
	go func() {
		defer recoverLog("winget")
		defer wg.running.Store(false)
		defer app.working.Store(false)
		op := beginOp("winget", "", T("log.wgStart", len(clean)), map[string]any{"ids": clean})
		ok := 0
		for i, id := range clean {
			if wg.stop.Load() {
				emit("winget", map[string]any{"id": id, "state": "skip"})
				continue
			}
			emit("winget", map[string]any{"id": id, "state": "run", "n": i + 1, "of": len(clean)})
			start := time.Now()
			tick := time.NewTicker(time.Second)
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-done:
						return
					case <-tick.C:
						emit("winget", map[string]any{"id": id, "state": "run", "n": i + 1, "of": len(clean), "secs": int(time.Since(start).Seconds())})
					}
				}
			}()
			wg.mu.Lock()
			out, code, err := runWinget(45*time.Minute, "upgrade", "--id", id, "--exact", "--silent",
				"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
			wg.mu.Unlock()
			tick.Stop()
			close(done)
			msg := wingetMessage(out)
			if err != nil && code == -1 {
				msg = err.Error()
			}
			if code == 0 {
				ok++
				op.info(id, T("log.wgOK"), map[string]any{"message": msg})
				emit("winget", map[string]any{"id": id, "state": "ok", "msg": msg})
			} else {
				op.fail(id, msg, map[string]any{"exit": uint32(code)})
				emit("winget", map[string]any{"id": id, "state": "fail", "msg": msg, "code": uint32(code)})
			}
		}
		op.end(T("log.wgDone", ok, len(clean)), nil)
		// what is still out of date
		wg.mu.Lock()
		wg.cache.Checked = 0
		wg.mu.Unlock()
		emit("winget", map[string]any{"state": "end", "ok": ok, "of": len(clean)})
	}()
	return true
}

func wingetStop() bool { wg.stop.Store(true); return true }

// openAppInstaller opens App Installer (which brings winget) in the Microsoft Store.
func openAppInstaller() bool {
	shellOpen("ms-windows-store://pdp/?ProductId=9NBLGGH4NNS1")
	return true
}
