//go:build windows

package main

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetLastInputInfo        = user32.NewProc("GetLastInputInfo")
	procNtSetInformationProcess = ntdll.NewProc("NtSetInformationProcess")
	procGetTickCount            = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount")
)

const (
	idlePriority   = 0x40 // IDLE_PRIORITY_CLASS
	processIoPrio  = 33   // ProcessIoPriority
	ioPrioVeryLow  = 0
	ioPrioNormal   = 2
	bgTameInterval = 10 * time.Second
)

// bgOff: the option is turned off in Settings.
var bgOff atomic.Bool

var bg struct {
	mu    sync.Mutex
	tamed map[uint32]bgProc // pid → what it had
	on    bool              // holding back now
}

type bgProc struct {
	Name    string `json:"name"`
	PID     uint32 `json:"pid"`
	created int64
	prio    uint32
}

func idleSeconds() int {
	li := struct{ size, time uint32 }{size: 8}
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0
	}
	now, _, _ := procGetTickCount.Call()
	return int((uint32(now) - li.time) / 1000) // both wrap together
}

func setIoPriority(h windows.Handle, v uint32) {
	_, _, _ = procNtSetInformationProcess.Call(uintptr(h), processIoPrio, uintptr(unsafe.Pointer(&v)), 4)
}

func bgTameLoop() {
	defer recoverLog("background")
	t := time.NewTicker(bgTameInterval)
	defer t.Stop()
	for range t.C {
		bgTameStep(!bgOff.Load() && bgShouldTame(idleSeconds(), gamePlaying()))
	}
}

// bgTameStep holds the heavy processes back (want) or gives them their
// priority back.
func bgTameStep(want bool) {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	if bg.tamed == nil {
		bg.tamed = map[uint32]bgProc{}
	}
	raw, err := queryProcesses()
	if err != nil {
		return
	}
	alive := map[uint32]rawProc{}
	for _, p := range raw {
		alive[p.pid] = p
	}
	for pid, p := range bg.tamed {
		if a, ok := alive[pid]; !ok || a.created != p.created {
			delete(bg.tamed, pid) // ended (or the PID is someone else's now)
		}
	}
	bg.on = want
	if !want {
		for pid, p := range bg.tamed {
			if h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, pid); err == nil {
				_ = windows.SetPriorityClass(h, p.prio)
				setIoPriority(h, ioPrioNormal)
				windows.CloseHandle(h)
			}
			delete(bg.tamed, pid)
		}
		return
	}
	for _, p := range raw {
		if !isBgHeavy(p.name) {
			continue
		}
		if _, done := bg.tamed[p.pid]; done {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p.pid)
		if err != nil {
			continue // protected: left as it is
		}
		prio, err := windows.GetPriorityClass(h)
		if err == nil && prio != idlePriority && windows.SetPriorityClass(h, idlePriority) == nil {
			setIoPriority(h, ioPrioVeryLow)
			bg.tamed[p.pid] = bgProc{Name: p.name, PID: p.pid, created: p.created, prio: prio}
		}
		windows.CloseHandle(h)
	}
}

type bgView struct {
	On    bool     `json:"on"`    // the option
	Now   bool     `json:"now"`   // holding back right now
	Tamed []string `json:"tamed"` // the processes held back
}

func bgTameState() bgView {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	v := bgView{On: !bgOff.Load(), Now: bg.on, Tamed: []string{}}
	seen := map[string]bool{}
	for _, p := range bg.tamed {
		if !seen[p.Name] {
			seen[p.Name] = true
			v.Tamed = append(v.Tamed, p.Name)
		}
	}
	sort.Strings(v.Tamed)
	return v
}
