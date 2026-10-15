//go:build windows

package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// Read-only: Get-PhysicalDisk and Get-StorageReliabilityCounter only read
// what the drives report. Nothing is written to a disk.
const diskHealthScript = `$out=@(); foreach($d in @(Get-PhysicalDisk)){ $r=$null; try { $r = $d | Get-StorageReliabilityCounter -ErrorAction Stop } catch {}
 $l=@(); try { $l=@(Get-Partition -DiskNumber ([int]$d.DeviceId) -ErrorAction Stop | Where-Object { $_.DriveLetter } | ForEach-Object { [string]$_.DriveLetter }) } catch {}
 $out += [pscustomobject]@{ id=[string]$d.DeviceId; name=[string]$d.FriendlyName; media=[string]$d.MediaType; bus=[string]$d.BusType; size=[int64]$d.Size;
  health=[string]$d.HealthStatus; status=@($d.OperationalStatus | ForEach-Object { [string]$_ }); letters=$l;
  temp=$r.Temperature; tempMax=$r.TemperatureMax; wear=$r.Wear; hours=$r.PowerOnHours; readErr=$r.ReadErrorsTotal; readUnc=$r.ReadErrorsUncorrected;
  writeErr=$r.WriteErrorsTotal; writeUnc=$r.WriteErrorsUncorrected; starts=$r.StartStopCycleCount } }
ConvertTo-Json -InputObject @($out) -Compress -Depth 3`

type dhView struct {
	Disks   []diskHealth `json:"disks"`
	Error   string       `json:"error,omitempty"`
	Checked int64        `json:"checked"`
}

var dhCache struct {
	mu sync.Mutex
	v  dhView
}

// diskHealthList reads the drives (cached for 10 minutes unless fresh).
func diskHealthList(fresh bool) dhView {
	dhCache.mu.Lock()
	defer dhCache.mu.Unlock()
	if !fresh && dhCache.v.Checked > 0 && time.Since(time.UnixMilli(dhCache.v.Checked)) < 10*time.Minute {
		return dhCache.v
	}
	v := dhView{Checked: time.Now().UnixMilli(), Disks: []diskHealth{}}
	out, _, err := runPS(90*time.Second, diskHealthScript)
	var ds []diskHealth
	if jerr := json.Unmarshal([]byte(out), &ds); jerr != nil {
		v.Error = T("dh.err.read")
		if err != nil {
			v.Error += " (" + err.Error() + ")"
		}
	}
	for i := range ds {
		d := &ds[i]
		d.Name = strings.TrimSpace(d.Name)
		if d.Letters == nil {
			d.Letters = []string{}
		}
		sort.Strings(d.Letters)
		assessDisk(d)
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	if ds != nil {
		v.Disks = ds
	}
	dhCache.v = v
	return v
}
