//go:build windows

package main

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var th struct {
	once    sync.Once
	ok      bool
	query   uintptr
	limit   uintptr
	perf    uintptr
	temp    uintptr
	value   *windows.LazyProc
	coll    *windows.LazyProc
	get     *windows.LazyProc
	meter   cpuMeter
	mu      sync.Mutex
	samples []thSample
	events  []thEvent
	cur     *thEvent
	noticed int64
}

func thStart() {
	d := windows.NewLazySystemDLL("pdh.dll")
	open, add := d.NewProc("PdhOpenQueryW"), d.NewProc("PdhAddEnglishCounterW")
	th.value, th.coll, th.get = d.NewProc("PdhGetFormattedCounterValue"), d.NewProc("PdhCollectQueryData"), d.NewProc("PdhGetFormattedCounterArrayW")
	if open.Find() != nil || add.Find() != nil || th.value.Find() != nil || th.coll.Find() != nil {
		return
	}
	if r, _, _ := open.Call(0, 0, uintptr(unsafe.Pointer(&th.query))); r != 0 {
		return
	}
	addOne := func(path string, to *uintptr) bool {
		p, _ := windows.UTF16PtrFromString(path)
		r, _, _ := add.Call(th.query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(to)))
		return r == 0
	}
	if !addOne(`\Processor Information(_Total)\% Performance Limit`, &th.limit) {
		return
	}
	addOne(`\Processor Information(_Total)\% Processor Performance`, &th.perf)
	addOne(`\Thermal Zone Information(*)\Temperature`, &th.temp) // a wildcard: read as an array
	if th.get.Find() != nil {
		th.temp = 0
	}
	_, _, _ = th.coll.Call(th.query)
	th.ok = true
}

func thValue(ctr uintptr) float64 {
	if ctr == 0 {
		return 0
	}
	var v struct {
		status uint32
		_      uint32
		value  float64
	}
	if r, _, _ := th.value.Call(ctr, 0x200, 0, uintptr(unsafe.Pointer(&v))); r != 0 || v.status > 1 {
		return 0
	}
	return v.value
}

func onBattery() bool {
	var st struct {
		acLine, flag, percent, saver uint8
		life, full                   uint32
	}
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return false
	}
	return st.acLine == 0
}

func thRead() (thSample, bool) {
	th.once.Do(thStart)
	if !th.ok {
		return thSample{}, false
	}
	if r, _, _ := th.coll.Call(th.query); r != 0 {
		return thSample{}, false
	}
	s := thSample{At: time.Now().UnixMilli(), CPU: th.meter.read(), Limit: thValue(th.limit), Perf: thValue(th.perf), Bat: onBattery()}
	if th.temp != 0 {
		if vals, _, ok := pdhArray(th.get, th.temp); ok {
			for _, k := range vals {
				if c := k - 273.15; c > s.TempC && c < 150 {
					s.TempC = c
				}
			}
		}
	}
	return s, true
}

// throttleLoop samples every 5 seconds; a stretch of throttling becomes an
// event, and a notification at most once an hour.
func throttleLoop() {
	defer recoverLog("throttle")
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		s, ok := thRead()
		if !ok {
			return // no counter on this PC
		}
		th.mu.Lock()
		th.samples = append(th.samples, s)
		if len(th.samples) > thWindow {
			th.samples = th.samples[len(th.samples)-thWindow:]
		}
		if s.limited() {
			if th.cur == nil {
				th.cur = &thEvent{At: s.At, Limit: s.Limit, Reason: thReason(s)}
			}
			th.cur.Secs = int((s.At - th.cur.At) / 1000)
			if s.Limit < th.cur.Limit {
				th.cur.Limit = s.Limit
			}
			if s.TempC > th.cur.TempC {
				th.cur.TempC = s.TempC
			}
			if thReason(s) == "heat" {
				th.cur.Reason = "heat"
			}
		} else if th.cur != nil && s.busy() {
			if th.cur.Secs >= 30 {
				th.events = append(th.events, *th.cur)
				if len(th.events) > 20 {
					th.events = th.events[1:]
				}
			}
			th.cur = nil
		}
		notify := thWorth(th.samples) && s.At-th.noticed > thNoticeMs
		var ev thEvent
		if notify {
			th.noticed = s.At
			if th.cur != nil {
				ev = *th.cur
			} else {
				ev = thEvent{At: s.At, Limit: s.Limit, TempC: s.TempC, Reason: thReason(s)}
			}
		}
		th.mu.Unlock()
		if notify {
			logEvent("optimize", "warn", "", T("th.log", int(ev.Limit), T("th.r."+ev.Reason)), map[string]any{"limit": ev.Limit, "temp": ev.TempC, "reason": ev.Reason})
			emit("throttle", ev)
			if app.w != nil {
				app.w.Dispatch(func() { trayNotify(T("th.nTitle", int(ev.Limit)), T("th.tip."+ev.Reason)) })
			}
		}
	}
}

type thView struct {
	OK      bool       `json:"ok"`
	Now     *thSample  `json:"now,omitempty"`
	Share   float64    `json:"share"` // of the last 5 minutes' busy time
	Samples []thSample `json:"samples"`
	Events  []thEvent  `json:"events"` // newest last
}

func throttleState() thView {
	th.mu.Lock()
	defer th.mu.Unlock()
	v := thView{OK: th.ok, Samples: append([]thSample{}, th.samples...), Events: append([]thEvent{}, th.events...)}
	if th.cur != nil && th.cur.Secs >= 30 {
		v.Events = append(v.Events, *th.cur)
	}
	if n := len(th.samples); n > 0 {
		s := th.samples[n-1]
		v.Now = &s
	}
	v.Share, _ = thShareOf(th.samples)
	return v
}
