package main

// Disk health: what the drives report about themselves (S.M.A.R.T. through
// Windows' storage counters) and a plain verdict with the reasons.

import (
	"sort"
	"strings"
)

type diskHealth struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Media    string   `json:"media"` // SSD, HDD, Unspecified
	Bus      string   `json:"bus"`   // NVMe, SATA, USB, ...
	Size     int64    `json:"size"`
	Letters  []string `json:"letters"`
	Health   string   `json:"health"` // what Windows says: Healthy, Warning, Unhealthy
	Status   []string `json:"status"` // OperationalStatus
	Temp     *int     `json:"temp"`   // °C (nil: not reported)
	TempMax  *int     `json:"tempMax"`
	Wear     *int     `json:"wear"`  // % of the rated life used (SSD)
	Hours    *int     `json:"hours"` // power-on hours
	ReadErr  *int64   `json:"readErr"`
	ReadUnc  *int64   `json:"readUnc"` // uncorrected
	WriteErr *int64   `json:"writeErr"`
	WriteUnc *int64   `json:"writeUnc"`
	Starts   *int     `json:"starts"`
	Verdict  string   `json:"verdict"` // good, warn, bad, unknown
	Reasons  []string `json:"reasons"` // text keys; the first one is the main reason
	NoData   bool     `json:"noData"`  // the drive gives no counters (USB enclosures often)
}

func (d diskHealth) solid() bool {
	m := strings.ToLower(d.Media)
	return m == "ssd" || strings.EqualFold(d.Bus, "NVMe")
}

// assessDisk fills Verdict and Reasons. Thresholds are on the careful side:
// a warning means "back up and watch", not "it is failing".
func assessDisk(d *diskHealth) {
	type reason struct {
		key string
		bad bool
	}
	var rs []reason
	add := func(key string, bad bool) { rs = append(rs, reason{key, bad}) }
	switch strings.ToLower(d.Health) {
	case "unhealthy":
		add("dh.r.unhealthy", true)
	case "warning":
		add("dh.r.warning", false)
	}
	for _, s := range d.Status {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "predictive failure":
			add("dh.r.predictive", true)
		case "lost communication", "no media", "stale metadata", "io error", "transient error":
			add("dh.r.status", false)
		}
	}
	if d.Wear != nil {
		switch w := *d.Wear; {
		case w >= 90:
			add("dh.r.wearBad", true)
		case w >= 70:
			add("dh.r.wear", false)
		}
	}
	if d.Temp != nil && *d.Temp > 0 {
		warn, bad := 55, 60
		if d.solid() {
			warn, bad = 70, 80
		}
		switch t := *d.Temp; {
		case t >= bad:
			add("dh.r.hot", true)
		case t >= warn:
			add("dh.r.warm", false)
		}
	}
	if (d.ReadUnc != nil && *d.ReadUnc > 0) || (d.WriteUnc != nil && *d.WriteUnc > 0) {
		add("dh.r.errors", false)
	}
	d.NoData = d.Temp == nil && d.Wear == nil && d.Hours == nil && d.ReadErr == nil && d.WriteErr == nil
	d.Reasons = nil
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].bad && !rs[j].bad })
	d.Verdict = "good"
	for i, r := range rs {
		if i == 0 {
			d.Verdict = "warn"
			if r.bad {
				d.Verdict = "bad"
			}
		}
		d.Reasons = append(d.Reasons, r.key)
	}
	if d.Verdict == "good" && d.NoData && d.Health == "" {
		d.Verdict = "unknown"
	}
}
