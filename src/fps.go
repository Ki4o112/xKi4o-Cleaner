package main

// The frame counter reads PresentMon's CSV (one line per frame a program
// shows: its name, process id and the milliseconds since its last frame) and
// keeps the last 10 seconds of each program. The game is the program in
// front, or else the one showing the most frames that is not part of Windows.
// Free of Windows calls.

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const fpsWindow = 10 * time.Second

type fpsNow struct {
	App   string  `json:"app"`
	PID   uint32  `json:"pid"`
	FPS   float64 `json:"fps"`  // the last second
	Low1  float64 `json:"low1"` // 1% low over the last 10 seconds
	MsAvg float64 `json:"ms"`   // average frame time, the last second
}

type fpsSample struct {
	at time.Time
	ms float64
}

type fpsBook struct {
	app, pid, ms int // column indexes; -1 until the header is read
	procs        map[uint32]*fpsProc
}

type fpsProc struct {
	name string
	s    []fpsSample
}

func newFpsBook() *fpsBook { return &fpsBook{app: -1, pid: -1, ms: -1, procs: map[uint32]*fpsProc{}} }

// fpsSkip: Windows' own windows and this app, which show frames too.
var fpsSkip = map[string]bool{"dwm.exe": true, "explorer.exe": true, "msedgewebview2.exe": true, "searchhost.exe": true,
	"startmenuexperiencehost.exe": true, "shellexperiencehost.exe": true, "textinputhost.exe": true,
	"applicationframehost.exe": true, "lockapp.exe": true, "xki4ocleaner.exe": true, "presentmon.exe": true}

// line takes one line of PresentMon's output: the header, a frame, or a
// message (ignored).
func (b *fpsBook) line(s string, now time.Time) {
	f := strings.Split(strings.TrimSpace(s), ",")
	if b.ms < 0 || f[0] == "Application" {
		for i, c := range f {
			switch strings.TrimSpace(c) {
			case "Application":
				b.app = i
			case "ProcessID":
				b.pid = i
			case "MsBetweenPresents":
				b.ms = i
			}
		}
		return
	}
	if len(f) <= max(b.app, b.pid, b.ms) || b.app < 0 || b.pid < 0 {
		return
	}
	pid, err1 := strconv.ParseUint(f[b.pid], 10, 32)
	ms, err2 := strconv.ParseFloat(f[b.ms], 64)
	if err1 != nil || err2 != nil || ms <= 0 || ms > 5000 {
		return
	}
	p := b.procs[uint32(pid)]
	if p == nil {
		p = &fpsProc{name: f[b.app]}
		b.procs[uint32(pid)] = p
	}
	p.s = append(p.s, fpsSample{now, ms})
	if len(p.s) > 64 && now.Sub(p.s[0].at) > fpsWindow {
		cut := sort.Search(len(p.s), func(i int) bool { return now.Sub(p.s[i].at) <= fpsWindow })
		p.s = append(p.s[:0], p.s[cut:]...)
	}
}

func (p *fpsProc) lastSecond(now time.Time) []fpsSample {
	i := sort.Search(len(p.s), func(i int) bool { return now.Sub(p.s[i].at) <= time.Second })
	return p.s[i:]
}

// now gives the game's numbers; fg is the process in front (0: unknown).
func (b *fpsBook) now(fg uint32, now time.Time) (fpsNow, bool) {
	pick, best := uint32(0), 0
	for pid, p := range b.procs {
		n := len(p.lastSecond(now))
		if n == 0 {
			if now.Sub(p.s[len(p.s)-1].at) > fpsWindow {
				delete(b.procs, pid) // closed
			}
			continue
		}
		if fpsSkip[strings.ToLower(p.name)] {
			continue
		}
		if pid == fg {
			pick, best = pid, 1<<30
			break
		}
		if n > best {
			pick, best = pid, n
		}
	}
	if pick == 0 {
		return fpsNow{}, false
	}
	p := b.procs[pick]
	sec := p.lastSecond(now)
	var sum float64
	for _, s := range sec {
		sum += s.ms
	}
	all := make([]float64, 0, len(p.s))
	for _, s := range p.s {
		if now.Sub(s.at) <= fpsWindow {
			all = append(all, s.ms)
		}
	}
	sort.Float64s(all)
	worst := all[min(len(all)-1, len(all)*99/100)] // the frame time 99% of frames beat
	return fpsNow{App: p.name, PID: pick, FPS: 1000 * float64(len(sec)) / sum, Low1: 1000 / worst, MsAvg: sum / float64(len(sec))}, true
}
