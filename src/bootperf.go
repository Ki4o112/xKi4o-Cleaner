package main

// How long Windows takes to start, from Windows' own measurements (the
// Diagnostics-Performance log: event 100 for every start, 101-103 for an
// app, driver or service that made one slower). Free of Windows calls.

import (
	"sort"
	"strconv"
	"strings"
)

// bootRec: one event as the script prints it (the event's data as text).
type bootRec struct {
	ID int               `json:"id"`
	T  int64             `json:"t"` // unix ms
	D  map[string]string `json:"d"`
}

type bootStart struct {
	At    int64 `json:"at"`
	Total int64 `json:"total"` // ms, until the desktop is ready
	Main  int64 `json:"main"`  // until the sign-in screen and the desktop
	Post  int64 `json:"post"`  // after the desktop: the startup programs
}

type bootSlow struct {
	Kind  string `json:"kind"` // app, driver, service
	Name  string `json:"name"`
	Exe   string `json:"exe,omitempty"` // an app's file name (lower case)
	Times int    `json:"times"`
	Delay int64  `json:"delay"` // ms it added, all starts together
	Last  int64  `json:"last"`
}

type bootPerf struct {
	Boots   []bootStart `json:"boots"` // newest first
	Slow    []bootSlow  `json:"slow"`  // the most delay first
	Error   string      `json:"error,omitempty"`
	Checked int64       `json:"checked"`
}

func bootNum(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// bootSummary turns the events into the last starts and what slowed them.
func bootSummary(recs []bootRec) bootPerf {
	p := bootPerf{Boots: []bootStart{}, Slow: []bootSlow{}}
	by := map[string]*bootSlow{}
	for _, r := range recs {
		d := r.D
		switch r.ID {
		case 100:
			if t := bootNum(d["BootTime"]); t > 0 {
				p.Boots = append(p.Boots, bootStart{At: r.T, Total: t, Main: bootNum(d["MainPathBootTime"]), Post: bootNum(d["BootPostBootTime"])})
			}
		case 101, 102, 103:
			kind := map[int]string{101: "app", 102: "driver", 103: "service"}[r.ID]
			name := strings.TrimSpace(d["FriendlyName"])
			file := strings.TrimSpace(d["Name"])
			if name == "" {
				name = file
			}
			if name == "" {
				continue
			}
			k := kind + "|" + strings.ToLower(name)
			s := by[k]
			if s == nil {
				s = &bootSlow{Kind: kind, Name: name}
				if kind == "app" && strings.HasSuffix(strings.ToLower(file), ".exe") {
					s.Exe = strings.ToLower(file)
				}
				by[k] = s
			}
			s.Times++
			delay := bootNum(d["DegradationTime"])
			if delay <= 0 {
				delay = bootNum(d["TotalTime"])
			}
			s.Delay += delay
			if r.T > s.Last {
				s.Last = r.T
			}
		}
	}
	sort.SliceStable(p.Boots, func(i, j int) bool { return p.Boots[i].At > p.Boots[j].At })
	if len(p.Boots) > 10 {
		p.Boots = p.Boots[:10]
	}
	for _, s := range by {
		p.Slow = append(p.Slow, *s)
	}
	sort.SliceStable(p.Slow, func(i, j int) bool {
		if p.Slow[i].Delay != p.Slow[j].Delay {
			return p.Slow[i].Delay > p.Slow[j].Delay
		}
		return p.Slow[i].Name < p.Slow[j].Name
	})
	if len(p.Slow) > 12 {
		p.Slow = p.Slow[:12]
	}
	return p
}
