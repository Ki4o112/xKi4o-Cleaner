//go:build windows

package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// the last 400 events of Windows' start measurements (about two months)
const bootPerfScript = `$ErrorActionPreference = 'SilentlyContinue'
$ev = Get-WinEvent -FilterHashtable @{ LogName = 'Microsoft-Windows-Diagnostics-Performance/Operational'; Id = 100,101,102,103 } -MaxEvents 400
$epoch = [datetime]'1970-01-01'
$out = foreach ($e in $ev) {
  $x = [xml]$e.ToXml(); $d = @{}
  foreach ($n in $x.Event.EventData.Data) { $d[[string]$n.Name] = [string]$n.'#text' }
  [pscustomobject]@{ id = $e.Id; t = [int64]($e.TimeCreated.ToUniversalTime() - $epoch).TotalMilliseconds; d = $d }
}
ConvertTo-Json -InputObject @($out) -Depth 4 -Compress`

var bootPerfCache struct {
	mu sync.Mutex
	v  bootPerf
}

func bootPerfRead(fresh bool) bootPerf {
	bootPerfCache.mu.Lock()
	defer bootPerfCache.mu.Unlock()
	if !fresh && bootPerfCache.v.Checked > 0 && time.Since(time.UnixMilli(bootPerfCache.v.Checked)) < 10*time.Minute {
		return bootPerfCache.v
	}
	out, _, err := runPS(60*time.Second, bootPerfScript)
	var recs []bootRec
	jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &recs)
	v := bootSummary(recs)
	if jerr != nil && strings.TrimSpace(out) != "" {
		v.Error = T("bp.err")
		if err != nil {
			v.Error += " (" + err.Error() + ")"
		}
	}
	v.Checked = time.Now().UnixMilli()
	bootPerfCache.v = v
	return v
}
