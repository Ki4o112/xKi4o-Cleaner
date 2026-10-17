//go:build windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// The search: what Windows Update offers this PC (Windows and drivers),
// the last installs, and whether Windows waits for a restart. Read only.
const wuSearchScript = `
try {
  $s = New-Object -ComObject Microsoft.Update.Session
  $s.ClientApplicationID = 'xKi4o Cleaner'
  $sr = $s.CreateUpdateSearcher()
  $r = $sr.Search("IsInstalled=0 and IsHidden=0")
  $ups = @(foreach ($u in $r.Updates) {
    $kb = ($u.KBArticleIDs | Select-Object -First 1)
    $d = ''; try { $d = $u.LastDeploymentChangeTime.ToString('yyyy-MM-dd') } catch {}
    $dc = ''; $dv = ''
    if ($u.Type -eq 2) { try { $dc = [string]$u.DriverClass; $dv = [string]$u.DriverVerDate.ToString('yyyy-MM-dd') } catch {} }
    $rb = $false; try { $rb = ($u.InstallationBehavior.RebootBehavior -ne 0) } catch {}
    [pscustomobject]@{ ID = [string]$u.Identity.UpdateID; Title = [string]$u.Title; KB = [string]$kb; Type = [int]$u.Type;
      Size = [double]$u.MaxDownloadSize; Downloaded = [bool]$u.IsDownloaded; BrowseOnly = [bool]$u.BrowseOnly; Reboot = $rb;
      Severity = [string]$u.MsrcSeverity; Cats = (@($u.Categories | ForEach-Object { $_.Name }) -join '|'); Date = $d;
      Desc = [string]$u.Description; DrvClass = $dc; DrvVer = $dv }
  })
  $hist = @()
  try {
    $n = $sr.GetTotalHistoryCount()
    if ($n -gt 0) {
      $hist = @(foreach ($h in $sr.QueryHistory(0, [Math]::Min(25, $n))) {
        if ([string]$h.Title) { [pscustomobject]@{ title = [string]$h.Title; date = $h.Date.ToString('o'); result = [int]$h.ResultCode; op = [int]$h.Operation } }
      })
    }
  } catch {}
  $rb = $false; try { $rb = [bool](New-Object -ComObject Microsoft.Update.SystemInfo).RebootRequired } catch {}
  ConvertTo-Json -InputObject ([pscustomobject]@{ updates = $ups; history = $hist; reboot = $rb }) -Depth 4 -Compress
} catch {
  "ERR|0x{0:X8}|{1}" -f $_.Exception.HResult, ($_.Exception.Message -replace '[\r\n|]', ' ')
}`

var wu struct {
	sync.Mutex
	last   *wuState
	busy   bool
	cancel context.CancelFunc
}

// wuStatus: what the page shows on opening (the last search, nothing new).
func wuStatus() wuState {
	wu.Lock()
	defer wu.Unlock()
	if wu.last != nil {
		s := *wu.last
		s.Busy, s.Admin = wu.busy, app.admin
		return s
	}
	return wuState{Updates: []wuUpdate{}, History: []wuHist{}, Admin: app.admin, Busy: wu.busy}
}

// wuSearch asks Windows Update (can take a few minutes).
func wuSearch() wuState {
	wu.Lock()
	if wu.busy {
		wu.Unlock()
		return wuState{Updates: []wuUpdate{}, History: []wuHist{}, Admin: app.admin, Busy: true, Error: T("wu.err.busy")}
	}
	wu.busy = true
	wu.Unlock()
	defer func() { wu.Lock(); wu.busy = false; wu.Unlock() }()
	start := time.Now()
	out, _, err := runPS(10*time.Minute, wuSearchScript)
	st, perr := wuParse(out)
	switch {
	case perr != nil:
		st.Error = perr.Error()
	case err != nil && len(st.Updates) == 0 && strings.TrimSpace(out) == "":
		st.Error = T("wu.err.run", err.Error())
	}
	st.Searched, st.Took, st.Admin = time.Now().UnixMilli(), time.Since(start).Milliseconds(), app.admin
	if st.Error != "" {
		logEvent("wu", "warn", "", T("log.wuSearchFail", st.Error), nil)
	} else {
		logEvent("wu", "info", "", T("log.wuSearched", len(st.Updates)), map[string]any{"took": st.Took})
	}
	wu.Lock()
	if st.Error == "" || wu.last == nil {
		c := st
		wu.last = &c
	}
	wu.Unlock()
	return st
}

type wuOpts struct {
	IDs          []string `json:"ids"`
	RestorePoint bool     `json:"restorePoint"`
}

// wuInstall downloads and installs the chosen updates one by one through
// Windows Update, telling the page each step.
func wuInstall(o wuOpts) (res wuResult) {
	if !app.admin {
		return wuResult{Error: T("blocked.admin")}
	}
	if !wuIDsOK(o.IDs) {
		return wuResult{Error: T("wu.err.ids")}
	}
	wu.Lock()
	if wu.busy {
		wu.Unlock()
		return wuResult{Error: T("wu.err.busy")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	wu.busy, wu.cancel = true, cancel
	titles := map[string]string{}
	if wu.last != nil {
		for _, u := range wu.last.Updates {
			titles[strings.ToLower(u.ID)] = u.Title
		}
	}
	wu.Unlock()
	defer func() { cancel(); wu.Lock(); wu.busy, wu.cancel = false, nil; wu.Unlock() }()
	app.working.Store(true)
	defer app.working.Store(false)
	op := beginOp("wu", "", T("log.wuInstall", len(o.IDs)), map[string]any{"ids": o.IDs, "restorePoint": o.RestorePoint})
	defer func() {
		if r := recover(); r != nil {
			res = wuResult{Error: T("err.unexpected", r)}
			op.panicked(r)
		}
		op.end(T("log.wuInstalled", res.Installed, len(res.Failed)), map[string]any{"reboot": res.Reboot})
	}()
	if o.RestorePoint {
		emit("wu-progress", map[string]any{"phase": "rp"})
		app.critical.Store(true)
		ok := restorePointTicking(func(sec int) { emit("wu-progress", map[string]any{"phase": "rp", "secs": sec}) })
		app.critical.Store(false)
		if ok {
			op.info("", T("log.rpDone"), nil)
		} else {
			op.warnf("", T("log.rpFail"), nil)
		}
	}
	quoted := make([]string, len(o.IDs))
	for i, id := range o.IDs {
		quoted[i] = "'" + strings.ToLower(id) + "'"
	}
	script := `
try {
  $ids = @(` + strings.Join(quoted, ",") + `)
  $s = New-Object -ComObject Microsoft.Update.Session
  $s.ClientApplicationID = 'xKi4o Cleaner'
  $r = $s.CreateUpdateSearcher().Search("IsInstalled=0")
  $list = @($r.Updates | Where-Object { $ids -contains ([string]$_.Identity.UpdateID).ToLower() })
  "COUNT|$($list.Count)"
  $i = 0
  foreach ($u in $list) {
    $i++
    $id = [string]$u.Identity.UpdateID
    try {
      if (-not $u.EulaAccepted) { $u.AcceptEula() }
      $c = New-Object -ComObject Microsoft.Update.UpdateColl
      [void]$c.Add($u)
      if (-not $u.IsDownloaded) {
        "STEP|$i|download|$id"
        $d = $s.CreateUpdateDownloader(); $d.Updates = $c; [void]$d.Download()
      }
      "STEP|$i|install|$id"
      $in = $s.CreateUpdateInstaller(); $in.Updates = $c
      $ir = $in.Install()
      "DONE|$i|$id|$($ir.ResultCode)|0x{0:X8}|$([int]$ir.RebootRequired)" -f $ir.HResult
    } catch {
      "DONE|$i|$id|4|0x{0:X8}|0" -f $_.Exception.HResult
    }
  }
} catch {
  "ERR|0x{0:X8}|{1}" -f $_.Exception.HResult, ($_.Exception.Message -replace '[\r\n|]', ' ')
}`
	app.critical.Store(true)
	defer app.critical.Store(false)
	count := len(o.IDs)
	got := 0
	err := runPSLines(ctx, 3*time.Hour, script, func(line string) {
		li, ok := wuLine(line)
		if !ok {
			return
		}
		switch li.Kind {
		case "count":
			count = li.Count
		case "step":
			emit("wu-progress", map[string]any{"phase": li.Phase, "i": li.I, "n": count, "id": li.ID, "title": titles[strings.ToLower(li.ID)]})
		case "done":
			got++
			title := titles[strings.ToLower(li.ID)]
			if title == "" {
				title = li.ID
			}
			okRes := li.Result == 2 || li.Result == 3
			if okRes {
				res.Installed++
				op.info(title, T("log.wuOne"), map[string]any{"result": li.Result, "reboot": li.Reboot})
			} else {
				res.Failed = append(res.Failed, title)
				op.fail(title, T("log.wuOneFail", wuHResult(li.HR)), map[string]any{"result": li.Result, "hresult": li.HR})
			}
			res.Reboot = res.Reboot || li.Reboot
			emit("wu-progress", map[string]any{"phase": "done", "i": li.I, "n": count, "id": li.ID, "title": title, "ok": okRes, "error": map[bool]string{true: "", false: wuHResult(li.HR)}[okRes]})
		case "err":
			res.Error = strings.TrimSpace(wuHResult(li.HR) + " " + strings.TrimPrefix(line, "ERR|"+li.HR+"|"))
		}
	})
	if res.Error == "" && err != nil && got == 0 {
		res.Error = T("wu.err.run", err.Error())
	}
	if res.Error == "" && count == 0 {
		res.Error = T("wu.err.gone")
	}
	res.OK = res.Error == "" && res.Installed > 0
	if res.Error != "" {
		op.fail("", res.Error, nil)
	}
	wu.Lock()
	if wu.last != nil && res.Installed > 0 {
		done := map[string]bool{}
		for _, id := range o.IDs {
			done[strings.ToLower(id)] = true
		}
		keep := wu.last.Updates[:0:0]
		for _, u := range wu.last.Updates {
			if !done[strings.ToLower(u.ID)] {
				keep = append(keep, u)
			}
		}
		wu.last.Updates = keep
		wu.last.Reboot = wu.last.Reboot || res.Reboot
	}
	wu.Unlock()
	return res
}

func wuCancel() bool {
	wu.Lock()
	defer wu.Unlock()
	if wu.cancel == nil {
		return false
	}
	wu.cancel()
	return true
}

// wuOpenSettings opens Settings → Windows Update.
func wuOpenSettings() bool {
	shellOpen("ms-settings:windowsupdate")
	return true
}

// runPSLines runs a PowerShell script hidden and hands over each line it
// prints as it comes.
func runPSLines(parent context.Context, timeout time.Duration, script string, line func(string)) error {
	ps := sys32(`WindowsPowerShell\v1.0\powershell.exe`)
	full := "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ProgressPreference='SilentlyContinue'; " + script
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := hiddenCmd(ctx, ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", full)
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line(strings.TrimPrefix(sc.Text(), "\ufeff"))
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w", ctx.Err())
		}
		return err
	}
	return nil
}
