//go:build windows

package main

// RAM cleaner in the style of Mem Reduct. It uses the same Windows calls
// (NtSetSystemInformation memory-list commands, the documented
// SetSystemFileCacheSize flush, FlushFileBuffers on volumes) and the same
// safe defaults: the full standby and modified lists are opt-in because
// purging them can cause short stutters.

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	ntdll                        = windows.NewLazySystemDLL("ntdll.dll")
	procGlobalMemoryStatusEx     = kernel32.NewProc("GlobalMemoryStatusEx")
	procSetSystemFileCacheSize   = kernel32.NewProc("SetSystemFileCacheSize")
	procGetSystemInfo            = kernel32.NewProc("GetSystemInfo")
	procNtSetSystemInformation   = ntdll.NewProc("NtSetSystemInformation")
	procNtQuerySystemInformation = ntdll.NewProc("NtQuerySystemInformation")
	procOpenClipboard            = user32.NewProc("OpenClipboard")
	procEmptyClipboard           = user32.NewProc("EmptyClipboard")
	procCloseClipboard           = user32.NewProc("CloseClipboard")
)

const (
	infoMemoryList                          = 80
	systemCombinePhysicalMemoryInformation  = 130
	systemRegistryReconciliationInformation = 155

	memoryEmptyWorkingSets            = 2
	memoryFlushModifiedList           = 3
	memoryPurgeStandbyList            = 4
	memoryPurgeLowPriorityStandbyList = 5
)

type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

type systemMemoryListInformation struct {
	zeroPageCount             uintptr
	freePageCount             uintptr
	modifiedPageCount         uintptr
	modifiedNoWritePageCount  uintptr
	badPageCount              uintptr
	pageCountByPriority       [8]uintptr
	repurposedPagesByPriority [8]uintptr
	modifiedPageCountPageFile uintptr
}

type memoryCombineInformationEx struct {
	handle        uintptr
	pagesCombined uintptr
	flags         uint32
	_             uint32
}

type memStats struct {
	Total       uint64 `json:"total"`
	Avail       uint64 `json:"avail"`
	Used        uint64 `json:"used"`
	Load        uint32 `json:"load"`
	CommitTotal uint64 `json:"commitTotal"`
	CommitUsed  uint64 `json:"commitUsed"`
	Standby     uint64 `json:"standby"`
	Modified    uint64 `json:"modified"`
	Free        uint64 `json:"free"`
	Lists       bool   `json:"lists"` // standby/modified/free are known
	LastFreed   int64  `json:"lastFreed"`
	LastAt      int64  `json:"lastAt"` // unix ms of the last clean
	Cleans      int64  `json:"cleans"` // cleans this session
}

var (
	privOnce   sync.Once
	pageSize   uintptr    = 4096
	memMu      sync.Mutex // one memory clean at a time
	lastFreed  atomic.Int64
	lastCleanT atomic.Int64
	cleanCount atomic.Int64
)

func initMemoryPrivileges() {
	privOnce.Do(func() {
		var si struct {
			arch     uint16
			_        uint16
			pageSize uint32
			_        [40]byte
		}
		if procGetSystemInfo.Find() == nil {
			_, _, _ = procGetSystemInfo.Call(uintptr(unsafe.Pointer(&si)))
			if si.pageSize >= 4096 {
				pageSize = uintptr(si.pageSize)
			}
		}
		enablePrivilege("SeProfileSingleProcessPrivilege")
		enablePrivilege("SeIncreaseQuotaPrivilege")
	})
}

func enablePrivilege(name string) {
	var tok windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok) != nil {
		return
	}
	defer tok.Close()
	var luid windows.LUID
	n, _ := windows.UTF16PtrFromString(name)
	if windows.LookupPrivilegeValue(nil, n, &luid) != nil {
		return
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	_ = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}

func getMemory() memStats {
	initMemoryPrivileges()
	var s memStats
	ms := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		s.Total, s.Avail, s.Load = ms.totalPhys, ms.availPhys, ms.memoryLoad
		s.Used = ms.totalPhys - ms.availPhys
		s.CommitTotal = ms.totalPageFile
		s.CommitUsed = ms.totalPageFile - ms.availPageFile
	}
	var li systemMemoryListInformation
	var ret uint32
	st, _, _ := procNtQuerySystemInformation.Call(infoMemoryList,
		uintptr(unsafe.Pointer(&li)), unsafe.Sizeof(li), uintptr(unsafe.Pointer(&ret)))
	if st == 0 {
		var standby uintptr
		for _, p := range li.pageCountByPriority {
			standby += p
		}
		s.Standby = uint64(standby) * uint64(pageSize)
		s.Modified = uint64(li.modifiedPageCount) * uint64(pageSize)
		s.Free = uint64(li.zeroPageCount+li.freePageCount) * uint64(pageSize)
		s.Lists = true
	}
	s.LastFreed = lastFreed.Load()
	s.LastAt = lastCleanT.Load()
	s.Cleans = cleanCount.Load()
	return s
}

func memCommand(cmd uint32) bool {
	st, _, _ := procNtSetSystemInformation.Call(infoMemoryList, uintptr(unsafe.Pointer(&cmd)), 4)
	return st == 0
}

func flushVolumes() bool {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return false
	}
	ok := false
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(rp) != windows.DRIVE_FIXED {
			continue
		}
		dev, _ := windows.UTF16PtrFromString(`\\.\` + root[:2])
		h, err := windows.CreateFile(dev, windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		if windows.FlushFileBuffers(h) == nil {
			ok = true
		}
		windows.CloseHandle(h)
	}
	return ok
}

var memRegionsDefault = []string{"workingset", "filecache", "volumecache", "standbylow", "combine", "registry"}

type memResult struct {
	Freed   int64           `json:"freed"`
	Before  uint64          `json:"before"`
	After   uint64          `json:"after"`
	Regions map[string]bool `json:"regions"`
	Stats   memStats        `json:"stats"`
}

// cleanMemory runs the chosen regions in Mem Reduct's order.
func cleanMemory(regions []string) memResult {
	memMu.Lock()
	defer memMu.Unlock()
	initMemoryPrivileges()
	want := map[string]bool{}
	for _, r := range regions {
		want[r] = true
	}
	res := memResult{Regions: map[string]bool{}}
	before := getMemory()
	build := windows.RtlGetVersion().BuildNumber

	if want["workingset"] {
		res.Regions["workingset"] = memCommand(memoryEmptyWorkingSets)
	}
	if want["filecache"] {
		max := ^uintptr(0)
		r, _, _ := procSetSystemFileCacheSize.Call(max, max, 0)
		res.Regions["filecache"] = r != 0
	}
	if want["volumecache"] {
		res.Regions["volumecache"] = flushVolumes()
	}
	if want["modified"] {
		res.Regions["modified"] = memCommand(memoryFlushModifiedList)
	}
	if want["standby"] {
		res.Regions["standby"] = memCommand(memoryPurgeStandbyList)
	}
	if want["standbylow"] && !want["standby"] {
		res.Regions["standbylow"] = memCommand(memoryPurgeLowPriorityStandbyList)
	}
	if want["registry"] && build >= 9600 {
		st, _, _ := procNtSetSystemInformation.Call(systemRegistryReconciliationInformation, 0, 0)
		res.Regions["registry"] = st == 0
	}
	if want["combine"] && build >= 10240 {
		var info memoryCombineInformationEx
		st, _, _ := procNtSetSystemInformation.Call(systemCombinePhysicalMemoryInformation,
			uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		res.Regions["combine"] = st == 0
	}

	after := getMemory()
	res.Before, res.After = before.Used, after.Used
	if before.Used > after.Used {
		res.Freed = int64(before.Used - after.Used)
	}
	lastFreed.Store(res.Freed)
	lastCleanT.Store(time.Now().UnixMilli())
	cleanCount.Add(1)
	after.LastFreed, after.LastAt, after.Cleans = res.Freed, lastCleanT.Load(), cleanCount.Load()
	res.Stats = after
	return res
}

// ---------------------------------------------------------------- auto clean

type memConfig struct {
	Auto      bool     `json:"auto"`
	Threshold int      `json:"threshold"` // percent, 0 = off
	Interval  int      `json:"interval"`  // minutes, 0 = off
	Regions   []string `json:"regions"`
}

var (
	memCfgMu sync.Mutex
	memCfg   = memConfig{Threshold: 90, Regions: memRegionsDefault}
)

func setMemConfig(c memConfig) {
	if c.Threshold != 0 && (c.Threshold < 50 || c.Threshold > 99) {
		c.Threshold = 90
	}
	if c.Interval < 0 || c.Interval > 1440 {
		c.Interval = 0
	}
	if len(c.Regions) == 0 {
		c.Regions = memRegionsDefault
	}
	memCfgMu.Lock()
	memCfg = c
	memCfgMu.Unlock()
}

func currentMemConfig() memConfig {
	memCfgMu.Lock()
	defer memCfgMu.Unlock()
	c := memCfg
	c.Regions = append([]string(nil), memCfg.Regions...)
	return c
}

const autoCooldown = 30 * time.Second // same as Mem Reduct

func memAutoLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	base := time.Now() // interval timer starts with the app, not at 1970
	for range t.C {
		c := currentMemConfig()
		if !c.Auto || (c.Threshold == 0 && c.Interval == 0) {
			base = time.Now()
			continue
		}
		last := time.UnixMilli(lastCleanT.Load())
		if last.After(base) {
			base = last
		}
		since := time.Since(last)
		reason := ""
		if c.Interval > 0 && time.Since(base) >= time.Duration(c.Interval)*time.Minute {
			reason = "interval"
		}
		if reason == "" && c.Threshold > 0 && since >= autoCooldown {
			if m := getMemory(); m.Load >= uint32(c.Threshold) {
				reason = "threshold"
			}
		}
		if reason == "" {
			continue
		}
		res := cleanMemory(c.Regions)
		emit("mem-auto", map[string]any{"reason": reason, "result": res})
	}
}

// ---------------------------------------------------------------- clipboard

func clearClipboard() bool {
	ok := false
	withCOM(func() {
		for i := 0; i < 5; i++ {
			if r, _, _ := procOpenClipboard.Call(0); r != 0 {
				r2, _, _ := procEmptyClipboard.Call()
				_, _, _ = procCloseClipboard.Call()
				ok = r2 != 0
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return ok
}
