//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var rfRun struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	out    string // the folder of the last search's results
}

type rfStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Out       string `json:"out,omitempty"`
	Error     string `json:"error,omitempty"`
}

func winfrExe() string {
	if p := filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\WindowsApps\winfr.exe`); fileExistsAny(p) {
		return p
	}
	if p, err := exec.LookPath("winfr.exe"); err == nil {
		return p
	}
	return ""
}

func recoverState() rfStatus {
	rfRun.mu.Lock()
	defer rfRun.mu.Unlock()
	return rfStatus{Installed: winfrExe() != "", Running: rfRun.cancel != nil, Out: rfRun.out}
}

// recoverInstall gets Windows File Recovery from the Microsoft Store through
// winget (the page gets the usual "winget" events).
func recoverInstall() bool {
	return wingetUpgrade([]string{winfrStoreID}, wgOpts{Install: true})
}

// recoverStart runs winfr; the page gets "recover" events with the progress
// and, at the end, the folder with what was found.
func recoverStart(r recoverReq) rfStatus {
	exe := winfrExe()
	if exe == "" {
		return rfStatus{Error: T("rf.err.missing")}
	}
	if !app.admin {
		return rfStatus{Installed: true, Error: T("blocked.admin")}
	}
	args, bad := winfrArgs(r)
	if bad != "" {
		return rfStatus{Installed: true, Error: T(bad)}
	}
	dst := args[1] + `\`
	if _, err := os.Stat(dst); err != nil {
		return rfStatus{Installed: true, Error: T("rf.err.drive")}
	}
	rfRun.mu.Lock()
	if rfRun.cancel != nil {
		rfRun.mu.Unlock()
		return recoverState()
	}
	ctx, cancel := context.WithCancel(context.Background())
	rfRun.cancel = cancel
	rfRun.out = ""
	rfRun.mu.Unlock()
	before := recoveryDirs(dst)
	app.working.Store(true)
	go func() {
		defer recoverLog("recover")
		defer app.working.Store(false)
		op := beginOp("recover", "", T("log.rfStart", args[0], args[1]), map[string]any{"args": args})
		last := ""
		code, err := runStreamCtx(ctx, exe, args, func(text string, _ bool) {
			text = strings.TrimSpace(text)
			if text == "" {
				return
			}
			last = text
			emit("recover", map[string]any{"state": "run", "pct": winfrPercent(text), "line": text})
		})
		stopped := ctx.Err() != nil
		out := ""
		for d := range recoveryDirs(dst) {
			if !before[d] {
				out = d // winfr's new Recovery_<date> folder
			}
		}
		rfRun.mu.Lock()
		rfRun.cancel = nil
		rfRun.out = out
		rfRun.mu.Unlock()
		cancel()
		switch {
		case stopped:
			op.info("", T("note.stopped"), nil)
			emit("recover", map[string]any{"state": "end", "stopped": true, "out": out})
		case err != nil || code != 0:
			msg := T("rf.err.run", strings.TrimSpace(last+" "+errText(err, code)))
			op.fail("", msg, nil)
			emit("recover", map[string]any{"state": "end", "error": msg, "out": out})
		default:
			emit("recover", map[string]any{"state": "end", "out": out, "files": countFiles(out)})
		}
		op.end("", map[string]any{"out": out, "exit": code})
	}()
	return rfStatus{Installed: true, Running: true}
}

func recoverStop() bool {
	rfRun.mu.Lock()
	c := rfRun.cancel
	rfRun.mu.Unlock()
	if c != nil {
		c()
	}
	return true
}

func recoverOpen() bool {
	if d := recoverState().Out; d != "" && dirExists(d) {
		shellOpen(d)
		return true
	}
	return false
}

func recoveryDirs(root string) map[string]bool {
	m, _ := filepath.Glob(filepath.Join(root, "Recovery_*"))
	out := map[string]bool{}
	for _, d := range m {
		out[d] = true
	}
	return out
}

func countFiles(dir string) int {
	if dir == "" {
		return 0
	}
	n := 0
	deadline := time.Now().Add(5 * time.Second)
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		if time.Now().After(deadline) {
			return filepath.SkipAll
		}
		return nil
	})
	return n
}
