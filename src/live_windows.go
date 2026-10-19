//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var live struct {
	sync.Mutex
	meter    cpuMeter // its own: the sidebar's meter keeps its pace
	prev     netSample
	adapters []netAdapter
	adAt     time.Time
}

// liveStats: one reading for the dashboard (asked for about every 1.5 s
// while PC health is open).
func liveStats() liveData {
	live.Lock()
	defer live.Unlock()
	s := liveData{CPU: live.meter.read()}
	ms := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		s.MemLoad, s.MemUsed, s.MemTotal = ms.memoryLoad, ms.totalPhys-ms.availPhys, ms.totalPhys
	}
	s.GPU = gpuReading()
	s.Net = netReading()
	return s
}

// ---------------------------------------------------------------- network

func netReading() netLive {
	if time.Since(live.adAt) > 15*time.Second || live.adapters == nil {
		live.adapters, live.adAt = inUse(listAdapters()), time.Now()
	}
	n := netLive{}
	cur := netSample{at: time.Now()}
	for i, a := range live.adapters {
		row := windows.MibIfRow2{InterfaceIndex: uint32(a.Index)}
		if !ifEntry(&row) {
			continue
		}
		cur.rx += row.InOctets
		cur.tx += row.OutOctets
		if i == 0 {
			n.Name, n.WiFi, n.Link = a.Name, a.WiFi, int64(row.ReceiveLinkSpeed)
		}
		n.OK = true
	}
	if n.OK {
		n.Rx, n.Tx = netRate(live.prev, cur)
		live.prev = cur
	}
	return n
}

// GetIfEntry2Ex is only in Windows 10 1703 and newer; older Windows (7, 8,
// 8.1, early 10) have GetIfEntry2, which reads the same row. Calling a missing
// function would panic, so each is looked up first.
var (
	iphlp          = windows.NewLazySystemDLL("iphlpapi.dll")
	procIfEntry2Ex = iphlp.NewProc("GetIfEntry2Ex")
	procIfEntry2   = iphlp.NewProc("GetIfEntry2")
)

func ifEntry(row *windows.MibIfRow2) bool {
	if procIfEntry2Ex.Find() == nil {
		return windows.GetIfEntry2Ex(0, row) == nil // MibIfEntryNormal: with the counters
	}
	if procIfEntry2.Find() == nil {
		r, _, _ := procIfEntry2.Call(uintptr(unsafe.Pointer(row)))
		return r == 0
	}
	return false
}

// ---------------------------------------------------------------- graphics

// NVIDIA's own library (it comes with the driver): load, temperature and
// memory of the card. Read only.
var nvml struct {
	once    sync.Once
	ok      bool
	lib     *windows.LazyDLL // for the Graphics card page's other readings
	dev     uintptr
	name    string
	temp    *windows.LazyProc
	util    *windows.LazyProc
	memInfo *windows.LazyProc
}

func nvmlStart() {
	dlls := []*windows.LazyDLL{windows.NewLazySystemDLL("nvml.dll")}
	if pf := strings.TrimSpace(os.Getenv("ProgramFiles")); pf != "" {
		dlls = append(dlls, windows.NewLazyDLL(filepath.Join(pf, `NVIDIA Corporation\NVSMI\nvml.dll`)))
	}
	for _, d := range dlls {
		if d.Load() != nil {
			continue
		}
		initP, count, byIndex, nameP := d.NewProc("nvmlInit_v2"), d.NewProc("nvmlDeviceGetCount_v2"), d.NewProc("nvmlDeviceGetHandleByIndex_v2"), d.NewProc("nvmlDeviceGetName")
		if initP.Find() != nil || count.Find() != nil || byIndex.Find() != nil {
			continue
		}
		if r, _, _ := initP.Call(); r != 0 {
			continue
		}
		var n uint32
		if r, _, _ := count.Call(uintptr(unsafe.Pointer(&n))); r != 0 || n == 0 {
			continue
		}
		var h uintptr
		if r, _, _ := byIndex.Call(0, uintptr(unsafe.Pointer(&h))); r != 0 || h == 0 {
			continue
		}
		if nameP.Find() == nil {
			buf := make([]byte, 96)
			if r, _, _ := nameP.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); r == 0 {
				nvml.name = windows.ByteSliceToString(buf)
			}
		}
		nvml.dev, nvml.ok, nvml.lib = h, true, d
		nvml.temp, nvml.util, nvml.memInfo = d.NewProc("nvmlDeviceGetTemperature"), d.NewProc("nvmlDeviceGetUtilizationRates"), d.NewProc("nvmlDeviceGetMemoryInfo")
		return
	}
}

func gpuReading() gpuLive {
	nvml.once.Do(nvmlStart)
	if nvml.ok {
		g := gpuLive{OK: true, Name: nvml.name, Util: -1, Src: "nvml"}
		var t uint32
		if nvml.temp.Find() == nil {
			if r, _, _ := nvml.temp.Call(nvml.dev, 0, uintptr(unsafe.Pointer(&t))); r == 0 && t < 150 { // NVML_TEMPERATURE_GPU
				g.Temp = int(t)
			}
		}
		var u struct{ gpu, mem uint32 }
		if nvml.util.Find() == nil {
			if r, _, _ := nvml.util.Call(nvml.dev, uintptr(unsafe.Pointer(&u))); r == 0 && u.gpu <= 100 {
				g.Util = int(u.gpu)
			}
		}
		var m struct{ total, free, used uint64 }
		if nvml.memInfo.Find() == nil {
			if r, _, _ := nvml.memInfo.Call(nvml.dev, uintptr(unsafe.Pointer(&m))); r == 0 {
				g.MemTotal, g.MemUsed = m.total, m.used
			}
		}
		return g
	}
	if u, ok := pdhGPU(); ok {
		return gpuLive{OK: true, Util: u, Src: "pdh"}
	}
	return gpuLive{Util: -1}
}

// Windows' own counter of the graphics engines (any card maker): the 3D
// engine's load of every program, added up.
var pdh struct {
	once   sync.Once
	mu     sync.Mutex // the dashboard and the Graphics card page read it
	ok     bool
	query  uintptr
	ctr    uintptr
	ctrMem uintptr // the graphics memory in use (any card maker)
	get    *windows.LazyProc
	coll   *windows.LazyProc
}

func pdhStart() {
	d := windows.NewLazySystemDLL("pdh.dll")
	open, add := d.NewProc("PdhOpenQueryW"), d.NewProc("PdhAddEnglishCounterW")
	pdh.coll, pdh.get = d.NewProc("PdhCollectQueryData"), d.NewProc("PdhGetFormattedCounterArrayW")
	if open.Find() != nil || add.Find() != nil || pdh.coll.Find() != nil || pdh.get.Find() != nil {
		return
	}
	if r, _, _ := open.Call(0, 0, uintptr(unsafe.Pointer(&pdh.query))); r != 0 {
		return
	}
	path, _ := windows.UTF16PtrFromString(`\GPU Engine(*engtype_3D)\Utilization Percentage`)
	if r, _, _ := add.Call(pdh.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&pdh.ctr))); r != 0 {
		return
	}
	if mp, _ := windows.UTF16PtrFromString(`\GPU Adapter Memory(*)\Dedicated Usage`); mp != nil {
		if r, _, _ := add.Call(pdh.query, uintptr(unsafe.Pointer(mp)), 0, uintptr(unsafe.Pointer(&pdh.ctrMem))); r != 0 {
			pdh.ctrMem = 0
		}
	}
	_, _, _ = pdh.coll.Call(pdh.query) // the first reading only starts the count
	pdh.ok = true
}

// pdhItem is PDH_FMT_COUNTERVALUE_ITEM_W with a double value.
type pdhItem struct {
	name   *uint16
	status uint32
	_      uint32
	value  float64
}

func pdhGPU() (int, bool) {
	u, _, ok := pdhRead()
	return u, ok
}

// pdhRead: the 3D load (all programs added up) and the most graphics memory
// in use on one card.
func pdhRead() (util int, mem uint64, ok bool) {
	pdh.once.Do(pdhStart)
	if !pdh.ok {
		return 0, 0, false
	}
	pdh.mu.Lock()
	defer pdh.mu.Unlock()
	if r, _, _ := pdh.coll.Call(pdh.query); r != 0 {
		return 0, 0, false
	}
	vals, valid := pdhValues(pdh.ctr)
	if !valid {
		return 0, 0, false
	}
	if pdh.ctrMem != 0 {
		if m, ok := pdhValues(pdh.ctrMem); ok {
			for _, v := range m {
				if v > 0 && uint64(v) > mem {
					mem = uint64(v)
				}
			}
		}
	}
	return gpuSum(vals), mem, true
}

func pdhValues(ctr uintptr) ([]float64, bool) {
	const pdhFmtDouble, pdhMoreData = 0x200, 0x800007D2
	var size, count uint32
	r, _, _ := pdh.get.Call(ctr, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 {
		return nil, uint32(r) == 0 // nothing is drawing: 0%
	}
	buf := make([]byte, size)
	if r, _, _ := pdh.get.Call(ctr, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return nil, false
	}
	items := unsafe.Slice((*pdhItem)(unsafe.Pointer(&buf[0])), count)
	vals := make([]float64, 0, count)
	for _, it := range items {
		if it.status == 0 || it.status == 1 { // PDH_CSTATUS_VALID_DATA, NEW_DATA
			vals = append(vals, it.value)
		}
	}
	return vals, true
}
