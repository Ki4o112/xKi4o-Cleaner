//go:build windows

package main

// Drive optimization with Windows' own tool (defrag.exe): TRIM for SSDs,
// defragmentation for HDDs. Nothing is deleted; a defragmentation can be
// stopped at any time (Windows moves files safely, one at a time).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// fragInfo: Windows' analysis of an HDD volume (read-only).
type fragInfo struct {
	Letter      string `json:"letter"`
	Total       int    `json:"total"` // % of the volume fragmented
	Files       int    `json:"files"` // % of the files fragmented
	Recommended bool   `json:"recommended"`
	Error       string `json:"error,omitempty"`
}

const fragScript = `$v = Get-CimInstance Win32_Volume -Filter "DriveLetter='%s:'" -ErrorAction Stop
$r = Invoke-CimMethod -InputObject $v -MethodName DefragAnalysis -ErrorAction Stop
[pscustomobject]@{ ret=[int]$r.ReturnValue; rec=[bool]$r.DefragRecommended; total=[int]$r.DefragAnalysis.TotalPercentFragmentation; files=[int]$r.DefragAnalysis.FilePercentFragmentation } | ConvertTo-Json -Compress`

func validLetter(l string) bool {
	return len(l) == 1 && strings.ToUpper(l)[0] >= 'A' && strings.ToUpper(l)[0] <= 'Z'
}

// analyzeDrive asks Windows how fragmented a volume is.
func analyzeDrive(letter string) fragInfo {
	res := fragInfo{Letter: strings.ToUpper(letter)}
	if !validLetter(letter) {
		res.Error = T("opt.badDrive")
		return res
	}
	out, _, err := runPS(10*time.Minute, fmt.Sprintf(fragScript, res.Letter))
	var r struct {
		Ret   int  `json:"ret"`
		Rec   bool `json:"rec"`
		Total int  `json:"total"`
		Files int  `json:"files"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); jerr != nil || r.Ret != 0 {
		res.Error = T("opt.analyzeFail")
		if err != nil {
			res.Error += " (" + err.Error() + ")"
		} else if r.Ret != 0 {
			res.Error += fmt.Sprintf(" (%d)", r.Ret)
		}
		return res
	}
	res.Total, res.Files, res.Recommended = r.Total, r.Files, r.Rec
	return res
}

type optOpts struct {
	Letters []string `json:"letters"`
}

var optRun struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	stop   atomic.Bool
}

// optimizeDrives optimizes the chosen drives one after another: TRIM for an
// SSD, defragmentation for an HDD. Events: "opt-drive" per drive, "opt-done".
func optimizeDrives(o optOpts) bool {
	if !app.busy.CompareAndSwap(false, true) {
		return false
	}
	by := map[string]driveRow{}
	for _, d := range getDrives() {
		by[strings.ToUpper(d.Letter)] = d
	}
	var todo []driveRow
	seen := map[string]bool{}
	for _, l := range o.Letters {
		l = strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(l), ":"))
		if d, ok := by[l]; ok && !seen[l] && (d.Media == "SSD" || d.Media == "HDD") {
			seen[l] = true
			todo = append(todo, d)
		}
	}
	if len(todo) == 0 {
		app.busy.Store(false)
		return false
	}
	app.working.Store(true)
	optRun.stop.Store(false)
	letters := make([]string, len(todo))
	for i, d := range todo {
		letters[i] = d.Letter + ": " + d.Media
	}
	op := beginOp("optimize", "", T("log.opt"), map[string]any{"drives": letters})
	go func() {
		ok := 0
		defer func() {
			if r := recover(); r != nil {
				op.panicked(r)
			}
			op.end(T("log.optDone", ok, len(todo)), nil)
			app.working.Store(false)
			app.busy.Store(false)
			emit("opt-done", map[string]any{"ok": ok, "of": len(todo), "stopped": optRun.stop.Load()})
		}()
		for _, d := range todo {
			if optRun.stop.Load() {
				emit("opt-drive", map[string]any{"letter": d.Letter, "state": "skip"})
				continue
			}
			emit("opt-drive", map[string]any{"letter": d.Letter, "state": "run", "media": d.Media})
			ctx, cancel := context.WithCancel(context.Background())
			optRun.mu.Lock()
			optRun.cancel = cancel
			optRun.mu.Unlock()
			args := []string{d.Letter + ":", "/L"} // SSD: TRIM
			if d.Media == "HDD" {
				args = []string{d.Letter + ":", "/O", "/U", "/V"} // HDD: the right optimization for it (defragmentation), with progress
			}
			last := -1.0
			start := time.Now()
			code, err := runStreamCtx(ctx, sys32("defrag.exe"), args, func(line string, _ bool) {
				p := percentIn(line)
				if p < 0 || math.Abs(p-last) < 1 && p != 100 {
					return
				}
				last = p
				emit("opt-drive", map[string]any{"letter": d.Letter, "state": "run", "media": d.Media, "pct": p, "line": strings.TrimSpace(line)})
			})
			cancel()
			optRun.mu.Lock()
			optRun.cancel = nil
			optRun.mu.Unlock()
			res := map[string]any{"letter": d.Letter, "media": d.Media, "secs": int(time.Since(start).Seconds())}
			switch {
			case optRun.stop.Load():
				res["state"] = "stopped"
				op.warnf(d.Letter+":", T("opt.stopped"), nil)
			case err == nil && code == 0:
				ok++
				res["state"] = "ok"
				op.info(d.Letter+":", T("log.optDrive", d.Media), map[string]any{"seconds": res["secs"]})
			default:
				res["state"] = "fail"
				note := T("opt.failCode", uint32(code))
				if d.Media == "SSD" {
					note = T("trim.unsupported")
				}
				res["note"] = note
				op.warnf(d.Letter+":", note, map[string]any{"code": uint32(code)})
			}
			emit("opt-drive", res)
		}
	}()
	return true
}

// optimizeStop stops the drive being optimized and skips the rest.
func optimizeStop() bool {
	optRun.stop.Store(true)
	optRun.mu.Lock()
	if optRun.cancel != nil {
		optRun.cancel()
	}
	optRun.mu.Unlock()
	return true
}
