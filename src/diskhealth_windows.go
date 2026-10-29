//go:build windows

package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
	Trim    string       `json:"trim,omitempty"` // on, off ("" = unknown or no SSD)
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
		for _, l := range d.Letters {
			var free, total, all uint64
			root, _ := windows.UTF16PtrFromString(l + `:\`)
			if windows.GetDiskFreeSpaceEx(root, &free, &total, &all) == nil && total > 0 {
				p := int(all * 100 / total)
				if d.FreePct == nil || p < *d.FreePct {
					d.FreePct = &p
				}
			}
		}
		if d.FreePct != nil {
			d.LowFree = lowFree(d.solid(), *d.FreePct)
		}
	}
	for _, d := range ds {
		if d.solid() {
			out, _, _ := runQuiet(15*time.Second, sys32("fsutil.exe"), "behavior", "query", "DisableDeleteNotify")
			v.Trim = trimState(out)
			break
		}
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

// ---------------------------------------------------------------- the details of one disk

// Read-only: the drive's S.M.A.R.T. table (ATA/SATA drives, through
// Windows' storage driver) and the System log's entries of the last 60 days
// from the disk driver and the file system.
const diskDetailScript = `$o = [ordered]@{}
try {
  $dd = Get-CimInstance Win32_DiskDrive -Filter "Index=%s" -ErrorAction Stop
  $pnp = ([string]$dd.PNPDeviceID).ToUpper()
  $fp = @(Get-CimInstance -Namespace root\wmi -ClassName MSStorageDriver_FailurePredictData -ErrorAction Stop | Where-Object { ([string]$_.InstanceName).ToUpper().StartsWith($pnp) })
  $th = @(Get-CimInstance -Namespace root\wmi -ClassName MSStorageDriver_FailurePredictThresholds -ErrorAction SilentlyContinue | Where-Object { ([string]$_.InstanceName).ToUpper().StartsWith($pnp) })
  if ($fp.Count) { $o.data = [Convert]::ToBase64String([byte[]]$fp[0].VendorSpecific) }
  if ($th.Count) { $o.thr = [Convert]::ToBase64String([byte[]]$th[0].VendorSpecific) }
} catch { $o.smartErr = [string]$_.Exception.Message }
try {
  $x = "*[System[(Provider[@Name='disk'] or Provider[@Name='Ntfs'] or Provider[@Name='Microsoft-Windows-Ntfs']) and (Level=1 or Level=2 or Level=3) and TimeCreated[timediff(@SystemTime) <= 5184000000]]]"
  $ev = Get-WinEvent -LogName System -FilterXPath $x -MaxEvents 400 -ErrorAction Stop
  $o.events = @($ev | ForEach-Object { [pscustomobject]@{ p=[string]$_.ProviderName; id=[int]$_.Id; t=[int64]((($_.TimeCreated).ToUniversalTime() - [datetime]'1970-01-01').TotalMilliseconds); m=[string]$_.Message; a=@($_.Properties | ForEach-Object { [string]$_.Value }) } })
} catch { if ([string]$_.FullyQualifiedErrorId -notmatch 'NoMatchingEventsFound') { $o.evErr = [string]$_.Exception.Message } }
ConvertTo-Json -InputObject ([pscustomobject]$o) -Compress -Depth 4`

// nvmeView: an NVMe drive's own log, for the page.
type nvmeView struct {
	Critical int     `json:"critical"`
	Temp     int     `json:"temp"`
	Spare    int     `json:"spare"`
	SpareMin int     `json:"spareMin"`
	Used     int     `json:"used"`
	Written  float64 `json:"written"` // TB
	Hours    uint64  `json:"hours"`
	Cycles   uint64  `json:"cycles"`
	Unsafe   uint64  `json:"unsafe"`
	MediaErr uint64  `json:"mediaErr"`
	ErrLog   uint64  `json:"errLog"`
}

type diskDetail struct {
	ID        string       `json:"id"`
	Disk      *diskHealth  `json:"disk"`
	Letters   []string     `json:"letters"`
	System    bool         `json:"system"`
	Smart     []smartAttr  `json:"smart"`
	SmartNote string       `json:"smartNote,omitempty"`
	NVMe      *nvmeView    `json:"nvme,omitempty"`
	Events    []diskEvent  `json:"events"`
	EventNote string       `json:"eventNote,omitempty"`
	Advice    []diskAdvice `json:"advice"`
}

// diskDetails reads everything known about one disk and what may help.
func diskDetails(id string) diskDetail {
	res := diskDetail{ID: id, Letters: []string{}, Smart: []smartAttr{}, Events: []diskEvent{}, Advice: []diskAdvice{}}
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return res
	}
	for _, d := range diskHealthList(false).Disks {
		if d.ID == id {
			c := d
			res.Disk = &c
		}
	}
	for _, d := range getDrives() {
		if d.Disk == id {
			res.Letters = append(res.Letters, d.Letter)
			res.System = res.System || d.System
		}
	}
	if res.Disk != nil && len(res.Letters) == 0 {
		res.Letters = append(res.Letters, res.Disk.Letters...)
		for _, l := range res.Letters {
			res.System = res.System || strings.EqualFold(l+":", systemDrive())
		}
	}
	sort.Strings(res.Letters)
	out, _, err := runPS(2*time.Minute, fmt.Sprintf(diskDetailScript, id))
	var r struct {
		Data     string     `json:"data"`
		Thr      string     `json:"thr"`
		SmartErr string     `json:"smartErr"`
		Events   []rawEvent `json:"events"`
		EvErr    string     `json:"evErr"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); jerr != nil {
		res.SmartNote, res.EventNote = T("dh.err.read"), T("dh.err.read")
		if err != nil {
			res.EventNote += " (" + err.Error() + ")"
		}
	}
	var nv *nvmeLog
	if res.Disk != nil && strings.EqualFold(res.Disk.Bus, "NVMe") {
		if b, err := nvmeHealthLog(id); err == nil {
			if l, ok := parseNVMeLog(b); ok {
				nv = &l
				res.NVMe = &nvmeView{Critical: l.Critical, Spare: l.Spare, SpareMin: l.SpareMin, Used: l.Used, Hours: l.Hours, Cycles: l.Cycles,
					Unsafe: l.Unsafe, MediaErr: l.MediaErr, ErrLog: l.ErrLog, Written: float64(l.Written) * 512000 / 1e12}
				if l.TempK > 273 {
					res.NVMe.Temp = l.TempK - 273
				}
			}
		}
	}
	if data, err := base64.StdEncoding.DecodeString(r.Data); err == nil && len(data) > 0 {
		thr, _ := base64.StdEncoding.DecodeString(r.Thr)
		if s := parseSmart(data, thr); s != nil {
			res.Smart = s
		}
	}
	if len(res.Smart) == 0 && res.NVMe == nil && res.SmartNote == "" {
		res.SmartNote = T("dh.noSmart")
	}
	if r.EvErr != "" {
		res.EventNote = T("dh.err.events")
	}
	res.Events = eventsForDisk(r.Events, id, res.Letters)
	if res.Events == nil {
		res.Events = []diskEvent{}
	}
	d := diskHealth{}
	if res.Disk != nil {
		d = *res.Disk
	}
	res.Advice = adviseDisk(d, res.Smart, res.Events, nv, res.Letters, res.System)
	return res
}

// openDrive opens a drive in File Explorer (to copy what matters off it).
func openDrive(letter string) bool {
	if !validLetter(letter) {
		return false
	}
	shellOpen(strings.ToUpper(letter) + `:\`)
	return true
}

// trimEnable turns TRIM back on (Windows' default for SSDs).
func trimEnable() bool {
	op := beginOp("optimize", "", T("log.trimOn"), nil)
	_, code, err := runQuiet(15*time.Second, sys32("fsutil.exe"), "behavior", "set", "DisableDeleteNotify", "0")
	ok := err == nil && code == 0
	if !ok {
		op.fail("", T("log.trimFail"), nil)
	}
	op.end("", nil)
	dhCache.mu.Lock()
	dhCache.v.Checked = 0
	dhCache.mu.Unlock()
	return ok
}
