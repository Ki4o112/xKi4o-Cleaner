//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type avState struct {
	On      bool       `json:"on"` // Defender is the antivirus and it runs
	RealT   bool       `json:"rt"`
	Mode    string     `json:"mode,omitempty"`
	SigAt   int64      `json:"sig"` // signatures updated (unix ms)
	Other   []string   `json:"other,omitempty"`
	Running bool       `json:"running"`
	Threats []avThreat `json:"threats"` // what Defender found in the last 30 days
	Error   string     `json:"error,omitempty"`
	Engine  string     `json:"engine"` // defender, clam, or "" (ClamAV is not installed yet)
	Clam    bool       `json:"clam"`   // ClamAV is installed
	ClamDB  int64      `json:"clamDb"` // its database updated (unix ms)
}

var avRun struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

const avStateScript = `$s = Get-MpComputerStatus -ErrorAction SilentlyContinue
$ms = { param($d) if ($d -and $d.Year -gt 2000) { [int64](($d.ToUniversalTime() - [datetime]'1970-01-01').TotalMilliseconds) } else { 0 } }
$o = [ordered]@{ on = [bool]($s -and $s.AntivirusEnabled -and "$($s.AMRunningMode)" -notmatch 'Passive|Not running'); rt = [bool]($s -and $s.RealTimeProtectionEnabled); mode = "$($s.AMRunningMode)"; sig = (& $ms $s.AntivirusSignatureLastUpdated) }
$o.other = @(Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntiVirusProduct -ErrorAction SilentlyContinue | ForEach-Object displayName | Where-Object { $_ -and $_ -notmatch 'Defender' })
$o | ConvertTo-Json -Compress`

// avThreatsScript lists Defender's detections since %d (unix ms), with names.
const avThreatsScript = `$since = ([datetime]'1970-01-01').AddMilliseconds(%d)
$th = @{}; Get-MpThreat -ErrorAction SilentlyContinue | ForEach-Object { $th[[string]$_.ThreatID] = $_ }
@(Get-MpThreatDetection -ErrorAction SilentlyContinue | Where-Object { $_.InitialDetectionTime.ToUniversalTime() -ge $since } | ForEach-Object {
  $x = $th[[string]$_.ThreatID]
  [pscustomobject]@{ id = [string]$_.ThreatID; name = $(if ($x) { $x.ThreatName } else { '' }); sev = $(if ($x) { [int]$x.SeverityID } else { 0 });
    status = [int]$_.ThreatStatusID; active = $(if ($x) { [bool]$x.IsActive } else { $false }); files = @($_.Resources);
    at = [int64](($_.InitialDetectionTime.ToUniversalTime() - [datetime]'1970-01-01').TotalMilliseconds) } }) | ConvertTo-Json -Compress -Depth 3`

func avThreatsSince(ms int64) []avThreat {
	out, _, _ := runPS(60*time.Second, fmt.Sprintf(avThreatsScript, ms))
	return parseAVThreats(out)
}

func avStateRead() avState {
	var st avState
	out, _, err := runPS(60*time.Second, avStateScript)
	if err != nil || json.Unmarshal([]byte(out), &st) != nil {
		st.Error = T("av.err.read")
	}
	avRun.mu.Lock()
	st.Running = avRun.cancel != nil
	avRun.mu.Unlock()
	avClam.defOn.Store(st.On && fileExists(mpCmdRun()))
	st.Engine, st.Clam, st.ClamDB = avEngine(), clamExe("clamscan.exe") != "", clamDBTime()
	if st.Engine == "defender" {
		st.Threats = avThreatsSince(time.Now().Add(-30 * 24 * time.Hour).UnixMilli())
	} else {
		avClam.mu.Lock()
		st.Threats = append([]avThreat{}, avClam.threats...)
		avClam.mu.Unlock()
	}
	return st
}

// avEngine: Defender while it works; ClamAV when Defender is gone or off.
func avEngine() string {
	switch {
	case avClam.defOn.Load():
		return "defender"
	case clamExe("clamscan.exe") != "":
		return "clam"
	}
	return ""
}

func mpCmdRun() string {
	return filepath.Join(os.Getenv("ProgramFiles"), "Windows Defender", "MpCmdRun.exe")
}

// avScan: kind "quick", "full" or "folder" (with path). The page gets "av"
// events: run (seconds so far), then end with the detections of this scan.
func avScan(kind, path string) bool {
	eng := avEngine()
	if !app.admin || eng == "" {
		return false
	}
	args := []string{"-Scan", "-ScanType", "1"}
	switch kind {
	case "full":
		args[2] = "2"
	case "folder":
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			return false
		}
		args = []string{"-Scan", "-ScanType", "3", "-File", filepath.Clean(path)}
	}
	avRun.mu.Lock()
	if avRun.cancel != nil {
		avRun.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	avRun.cancel = cancel
	avRun.mu.Unlock()
	app.working.Store(true)
	if eng == "clam" {
		go clamScan(ctx, cancel, kind, path)
		return true
	}
	go func() {
		defer recoverLog("antivirus")
		defer app.working.Store(false)
		op := beginOp("antivirus", "", T("log.avScan", T("av.k."+kind)), map[string]any{"kind": kind, "path": path})
		t0 := time.Now()
		tick := time.NewTicker(time.Second)
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					emit("av", map[string]any{"state": "run", "secs": int(time.Since(t0).Seconds())})
				}
			}
		}()
		code, err := runStreamCtx(ctx, mpCmdRun(), args, func(string, bool) {})
		tick.Stop()
		close(done)
		stopped := ctx.Err() != nil
		avRun.mu.Lock()
		avRun.cancel = nil
		avRun.mu.Unlock()
		cancel()
		found := avThreatsSince(t0.Add(-5 * time.Second).UnixMilli())
		ev := map[string]any{"state": "end", "threats": found, "secs": int(time.Since(t0).Seconds())}
		switch {
		case stopped:
			ev["stopped"] = true
		case err != nil && code != 2: // 2: threats were found
			ev["error"] = T("av.err.scan", errText(err, code))
			op.fail("", ev["error"].(string), nil)
		}
		op.end(T("log.avDone", len(found)), map[string]any{"exit": code})
		emit("av", ev)
	}()
	return true
}

func avStop() bool {
	avRun.mu.Lock()
	c := avRun.cancel
	avRun.mu.Unlock()
	if c != nil {
		c()
		if avEngine() != "defender" {
			return true
		}
		_, _, _ = runQuiet(30*time.Second, mpCmdRun(), "-Scan", "-Cancel")
	}
	return true
}

// avRemove asks Defender to remove every threat that is still active.
func avRemove() avState {
	if avEngine() == "clam" {
		clamRemove()
		return avStateRead()
	}
	op := beginOp("antivirus", "", T("log.avRemove"), nil)
	_, code, err := runPS(10*time.Minute, "Remove-MpThreat -ErrorAction Stop")
	if err != nil || code != 0 {
		op.fail("", errText(err, code), nil)
	}
	op.end("", nil)
	return avStateRead()
}
