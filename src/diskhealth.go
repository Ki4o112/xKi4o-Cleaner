package main

// Disk health: what the drives report about themselves (S.M.A.R.T. through
// Windows' storage counters) and a plain verdict with the reasons.

import (
	"encoding/binary"
	"regexp"
	"sort"
	"strconv"
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
	FreePct  *int     `json:"freePct,omitempty"`  // the fullest of its drive letters
	LowFree  bool     `json:"lowFree,omitempty"`  // so full it slows down
}

// lowFree: an SSD slows down under about 15% free space (it has no room left
// to sort its cells), a hard disk under 10% (the files get scattered).
func lowFree(solid bool, pct int) bool {
	if solid {
		return pct < 15
	}
	return pct < 10
}

// trimState reads "fsutil behavior query DisableDeleteNotify": "on" when
// Windows tells the SSD which space is free (TRIM), "off", or "" (unknown).
func trimState(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "ReFS") || !strings.Contains(l, "DisableDeleteNotify") || !strings.Contains(l, "=") {
			continue
		}
		v := strings.TrimSpace(l[strings.Index(l, "=")+1:])
		switch {
		case strings.HasPrefix(v, "0"):
			return "on"
		case strings.HasPrefix(v, "1"):
			return "off"
		}
	}
	return ""
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
	SpareMin int    // the drive's own limit for the spare, %
	Used     int    // percentage of the rated life used
	Written  uint64 // data units written (1 unit = 1000 × 512 bytes)
	Cycles   uint64
	Hours    uint64
	Unsafe   uint64 // unsafe shutdowns
	MediaErr uint64 // media and data integrity errors (not recovered)
	ErrLog   uint64 // entries in the drive's error log
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
		Critical: int(b[0]), TempK: int(le.Uint16(b[1:])), Spare: int(b[3]), SpareMin: int(b[4]), Used: int(b[5]),
		Written: n(48), Cycles: n(112), Hours: n(128), Unsafe: n(144), MediaErr: n(160), ErrLog: n(176),
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

// ---------------------------------------------------------------- the details of one disk

// smartAttr: one S.M.A.R.T. attribute of an ATA/SATA drive.
type smartAttr struct {
	ID    int   `json:"id"`
	Cur   int   `json:"cur"`   // normalized value (higher is better)
	Worst int   `json:"worst"` // the lowest it has been
	Thr   int   `json:"thr"`   // the drive fails the attribute at or below this
	Raw   int64 `json:"raw"`   // the counter as the drive keeps it
	Bad   bool  `json:"bad,omitempty"`
}

// parseSmart reads the 512-byte attribute table (and the table of limits)
// that Windows passes on from the drive: 30 entries of 12 bytes from byte 2.
func parseSmart(data, thr []byte) []smartAttr {
	th := map[int]int{}
	for i := 2; i+12 <= len(thr) && i < 2+30*12; i += 12 {
		if thr[i] != 0 {
			th[int(thr[i])] = int(thr[i+1])
		}
	}
	var out []smartAttr
	for i := 2; i+12 <= len(data) && i < 2+30*12; i += 12 {
		id := int(data[i])
		if id == 0 {
			continue
		}
		var raw int64
		for k := 5; k >= 0; k-- {
			raw = raw<<8 | int64(data[i+5+k])
		}
		a := smartAttr{ID: id, Cur: int(data[i+3]), Worst: int(data[i+4]), Thr: th[id], Raw: raw}
		a.Bad = a.Thr > 0 && a.Cur > 0 && a.Cur <= a.Thr
		out = append(out, a)
	}
	return out
}

// smartCount: the count an attribute's raw value stands for (some drives
// keep extra numbers in the upper bytes).
func smartCount(a smartAttr) int64 {
	switch a.ID {
	case 188, 190, 194:
		return a.Raw & 0xFFFF
	}
	return a.Raw & 0xFFFFFFFF
}

func smartValue(as []smartAttr, id int) int64 {
	for _, a := range as {
		if a.ID == id {
			return smartCount(a)
		}
	}
	return 0
}

// rawEvent: an entry of Windows' System log, as read.
type rawEvent struct {
	P  string   `json:"p"`
	ID int      `json:"id"`
	T  int64    `json:"t"` // unix ms
	M  string   `json:"m"`
	A  []string `json:"a"`
}

// diskEvent: the entries of one kind for one disk.
type diskEvent struct {
	Provider string `json:"provider"`
	ID       int    `json:"id"`
	Count    int    `json:"count"`
	First    int64  `json:"first"`
	Last     int64  `json:"last"`
	Msg      string `json:"msg"` // Windows' own text of the newest one
	Key      string `json:"key"` // what it means (a text key)
}

// diskEventKey: what an entry of the log means.
func diskEventKey(provider string, id int) string {
	switch strings.ToLower(provider) {
	case "disk":
		switch id {
		case 7:
			return "dh.e.badblock"
		case 11:
			return "dh.e.controller"
		case 15:
			return "dh.e.notready"
		case 51:
			return "dh.e.paging"
		case 52:
			return "dh.e.predict"
		case 153:
			return "dh.e.retried"
		case 154:
			return "dh.e.hwfail"
		case 157:
			return "dh.e.removed"
		}
	case "ntfs", "microsoft-windows-ntfs":
		switch id {
		case 55, 57, 98, 130, 137, 140:
			return "dh.e.fs"
		}
	}
	return "dh.e.other"
}

// eventsForDisk picks the log entries about one disk: the disk driver's
// that name it (\Device\Harddisk1\..., "Disk 1"), and the file system's
// that name one of its volumes (E:). Newest kinds first.
func eventsForDisk(evs []rawEvent, disk string, letters []string) []diskEvent {
	if disk == "" {
		return nil
	}
	// \b would not work next to Cyrillic letters: the edges are spelled out
	reDisk := regexp.MustCompile(`(?i)\\harddisk` + regexp.QuoteMeta(disk) + `\\|(?:^|[^\pL\pN])(?:disk|диск)\s+` + regexp.QuoteMeta(disk) + `(?:[^\pN]|$)`)
	var reVol *regexp.Regexp
	if len(letters) > 0 {
		q := make([]string, len(letters))
		for i, l := range letters {
			q[i] = regexp.QuoteMeta(strings.ToUpper(l))
		}
		reVol = regexp.MustCompile(`(?i)(?:^|[^A-Za-z\\])(?:` + strings.Join(q, "|") + `):(?:[^\\]|$)`)
	}
	groups := map[string]*diskEvent{}
	var order []string
	for _, e := range evs {
		text := e.M + "\n" + strings.Join(e.A, "\n")
		p := strings.ToLower(e.P)
		var match bool
		if p == "ntfs" || p == "microsoft-windows-ntfs" {
			match = reVol != nil && reVol.MatchString(text)
		} else {
			match = reDisk.MatchString(text)
		}
		if !match {
			continue
		}
		k := p + "|" + strconv.Itoa(e.ID)
		g := groups[k]
		if g == nil {
			g = &diskEvent{Provider: e.P, ID: e.ID, First: e.T, Last: e.T, Msg: e.M, Key: diskEventKey(e.P, e.ID)}
			groups[k] = g
			order = append(order, k)
		}
		g.Count++
		if e.T < g.First {
			g.First = e.T
		}
		if e.T > g.Last {
			g.Last, g.Msg = e.T, e.M
		}
	}
	out := make([]diskEvent, 0, len(order))
	for _, k := range order {
		g := *groups[k]
		if r := []rune(g.Msg); len(r) > 400 {
			g.Msg = string(r[:400]) + "…"
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out
}

// diskAdvice: one thing that may help (a text key, a number for it, and the
// action the page offers for it).
type diskAdvice struct {
	Key    string `json:"key"`
	N      int64  `json:"n,omitempty"`
	Action string `json:"action,omitempty"` // backup (open the drive), scan (read-only check), fix (bad sector check)
}

// adviseDisk: what the counters and the log mean for this disk, and what to do.
func adviseDisk(d diskHealth, smart []smartAttr, evs []diskEvent, nv *nvmeLog, letters []string, system bool) []diskAdvice {
	var out []diskAdvice
	add := func(key string, n int64, action string) {
		out = append(out, diskAdvice{Key: key, N: n, Action: action})
	}
	count := func(key string) int64 {
		var n int64
		for _, e := range evs {
			if e.Key == key {
				n += int64(e.Count)
			}
		}
		return n
	}
	sectors := smartValue(smart, 197) + smartValue(smart, 198) // pending + offline uncorrectable
	reported := smartValue(smart, 187)
	var unc int64
	if d.ReadUnc != nil {
		unc += *d.ReadUnc
	}
	if d.WriteUnc != nil {
		unc += *d.WriteUnc
	}
	logged := count("dh.e.badblock") + count("dh.e.hwfail") + count("dh.e.paging")
	failing := count("dh.e.predict") > 0 || d.Verdict == "bad"
	for _, a := range smart {
		failing = failing || a.Bad
	}
	if nv != nil {
		failing = failing || nv.Critical&0x1D != 0 || nv.SpareMin > 0 && nv.Spare <= nv.SpareMin
		unc += int64(min(nv.MediaErr, 1<<40))
	}
	if sectors+reported+unc+logged > 0 || failing {
		add("dh.a.backup", 0, "backup")
		if sectors > 0 {
			add("dh.a.sectors", sectors, "")
		}
		if n := max(reported, unc); n > 0 {
			add("dh.a.unc", n, "")
		}
		if logged > 0 {
			add("dh.a.logged", logged, "")
		}
		switch {
		case system:
			add("dh.a.fixSys", 0, "")
		case len(letters) > 0:
			add("dh.a.fix", 0, "fix")
		}
		add("dh.a.watch", 0, "")
	}
	if n := smartValue(smart, 5); n > 0 {
		add("dh.a.realloc", n, "")
	}
	if n := smartValue(smart, 199); n > 0 {
		add("dh.a.cable", n, "")
	}
	if n := smartValue(smart, 188) + count("dh.e.controller") + count("dh.e.retried") + count("dh.e.notready"); n > 0 {
		add("dh.a.retry", n, "")
	}
	if n := count("dh.e.fs"); n > 0 && len(letters) > 0 {
		add("dh.a.fs", n, "scan")
	}
	for _, r := range d.Reasons {
		switch r {
		case "dh.r.hot", "dh.r.warm":
			add("dh.a.cool", 0, "")
		case "dh.r.wear", "dh.r.wearBad":
			add("dh.a.wear", 0, "backup")
		}
	}
	if len(out) == 0 {
		add("dh.a.ok", 0, "")
	}
	return out
}
