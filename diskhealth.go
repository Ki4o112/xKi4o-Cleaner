package main

// Disk health: what the drives report about themselves (S.M.A.R.T. through
// Windows' storage counters) and a plain verdict with the reasons.

import (
	"encoding/binary"
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
	Critical int      `json:"critical,omitempty"` // NVMe critical warning bits (from the drive's own log)
	Verdict  string   `json:"verdict"`            // good, warn, bad, unknown
	Reasons  []string `json:"reasons"`            // text keys; the first one is the main reason
	NoData   bool     `json:"noData"`             // the drive gives no counters (USB enclosures often)
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
	add := func(key string, bad bool) {
		for _, r := range rs {
			if r.key == key {
				return
			}
		}
		rs = append(rs, reason{key, bad})
	}
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
	// NVMe: spare space below the limit, reliability degraded, read-only,
	// backup of the volatile memory failed
	if d.Critical&0x1D != 0 {
		add("dh.r.predictive", true)
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

// nvmeLog: what an NVMe drive says about itself (its SMART / health log,
// log page 2 of the NVMe standard).
type nvmeLog struct {
	Critical int
	TempK    int    // composite temperature, kelvin
	Spare    int    // available spare, %
	Used     int    // percentage of the rated life used
	Written  uint64 // data units written (1 unit = 1000 × 512 bytes)
	Cycles   uint64
	Hours    uint64
	Unsafe   uint64 // unsafe shutdowns
	MediaErr uint64 // media and data integrity errors (not recovered)
}

// parseNVMeLog reads the 512-byte health log. An empty or short log is no log.
func parseNVMeLog(b []byte) (nvmeLog, bool) {
	if len(b) < 192 {
		return nvmeLog{}, false
	}
	empty := true
	for _, c := range b[:192] {
		if c != 0 {
			empty = false
			break
		}
	}
	if empty {
		return nvmeLog{}, false
	}
	le := binary.LittleEndian
	// the counters are 128-bit; the high half is 0 for any real drive
	n := func(at int) uint64 {
		if le.Uint64(b[at+8:]) != 0 {
			return 1<<63 - 1
		}
		return le.Uint64(b[at:])
	}
	return nvmeLog{
		Critical: int(b[0]), TempK: int(le.Uint16(b[1:])), Spare: int(b[3]), Used: int(b[5]),
		Written: n(48), Cycles: n(112), Hours: n(128), Unsafe: n(144), MediaErr: n(160),
	}, true
}

// useNVMe takes the drive's own counters: they are what Windows reads too,
// but Windows does not give all of them for every drive.
func (d *diskHealth) useNVMe(l nvmeLog) {
	ip := func(v int) *int { return &v }
	clamp := func(v uint64) int {
		if v > 1<<31-1 {
			return 1<<31 - 1
		}
		return int(v)
	}
	if l.TempK > 273 && l.TempK < 273+150 {
		d.Temp = ip(l.TempK - 273)
	}
	d.Wear = ip(l.Used)
	if l.Hours > 0 || d.Hours == nil {
		d.Hours = ip(clamp(l.Hours))
	}
	if l.Cycles > 0 || d.Starts == nil {
		d.Starts = ip(clamp(l.Cycles))
	}
	if d.ReadUnc == nil {
		m := int64(clamp(l.MediaErr))
		d.ReadUnc = &m
	}
	d.Critical = l.Critical
}
