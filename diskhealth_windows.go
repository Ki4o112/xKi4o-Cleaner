//go:build windows

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
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
		if strings.EqualFold(d.Bus, "NVMe") {
			if b, err := nvmeHealthLog(d.ID); err == nil {
				if l, ok := parseNVMeLog(b); ok {
					d.useNVMe(l)
				}
			}
		}
		assessDisk(d)
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	if ds != nil {
		v.Disks = ds
	}
	dhCache.v = v
	return v
}

// nvmeHealthLog asks an NVMe drive for its health log (log page 2) through
// Windows' storage driver. Read-only: the drive is opened with no read or
// write access at all, which is all this question needs.
func nvmeHealthLog(disk string) ([]byte, error) {
	if disk == "" || strings.Trim(disk, "0123456789") != "" {
		return nil, errors.New("not a disk number")
	}
	name, err := windows.UTF16PtrFromString(`\\.\PhysicalDrive` + disk)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	const (
		ioctlStorageQueryProperty              = 0x2D1400
		storageAdapterProtocolSpecificProperty = 49
		storageDeviceProtocolSpecificProperty  = 50
		protocolTypeNvme                       = 3
		nvmeDataTypeLogPage                    = 2
		nvmeLogHealth                          = 2
		specificSize                           = 40 // STORAGE_PROTOCOL_SPECIFIC_DATA
		logSize                                = 512
	)
	le := binary.LittleEndian
	// STORAGE_PROPERTY_QUERY (property, query type) with the protocol data in
	// its AdditionalParameters, and room for the log. Asked of the drive
	// first, then of its controller (some drivers answer only that).
	buf := make([]byte, 8+specificSize+logSize)
	for _, prop := range []uint32{storageDeviceProtocolSpecificProperty, storageAdapterProtocolSpecificProperty} {
		clear(buf)
		le.PutUint32(buf[0:], prop)
		sp := buf[8:]
		le.PutUint32(sp[0:], protocolTypeNvme)
		le.PutUint32(sp[4:], nvmeDataTypeLogPage)
		le.PutUint32(sp[8:], nvmeLogHealth)
		le.PutUint32(sp[16:], specificSize)
		le.PutUint32(sp[20:], logSize)
		var n uint32
		if err = windows.DeviceIoControl(h, ioctlStorageQueryProperty, &buf[0], uint32(len(buf)), &buf[0], uint32(len(buf)), &n, nil); err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	// the answer: STORAGE_PROTOCOL_DATA_DESCRIPTOR (version, size, the protocol data)
	if le.Uint32(buf[0:]) != 8+specificSize || le.Uint32(buf[4:]) != 8+specificSize {
		return nil, errors.New("unexpected answer")
	}
	off, ln := int(le.Uint32(buf[8+16:])), int(le.Uint32(buf[8+20:]))
	if off < specificSize || ln < 192 || 8+off+ln > len(buf) {
		return nil, errors.New("unexpected answer")
	}
	return buf[8+off : 8+off+ln], nil
}
