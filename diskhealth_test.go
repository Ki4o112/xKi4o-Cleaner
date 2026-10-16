package main

import (
	"encoding/binary"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestAssessDisk(t *testing.T) {
	i := func(v int) *int { return &v }
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name    string
		d       diskHealth
		verdict string
		reasons []string
	}{
		{"healthy nvme", diskHealth{Media: "SSD", Bus: "NVMe", Health: "Healthy", Temp: i(41), Wear: i(3), Hours: i(1200)}, "good", nil},
		{"warm nvme is fine", diskHealth{Media: "SSD", Bus: "NVMe", Health: "Healthy", Temp: i(62)}, "good", nil},
		{"hot hdd", diskHealth{Media: "HDD", Bus: "SATA", Health: "Healthy", Temp: i(57)}, "warn", []string{"dh.r.warm"}},
		{"very hot hdd", diskHealth{Media: "HDD", Health: "Healthy", Temp: i(61)}, "bad", []string{"dh.r.hot"}},
		{"worn ssd", diskHealth{Media: "SSD", Health: "Healthy", Wear: i(75)}, "warn", []string{"dh.r.wear"}},
		{"worn out ssd", diskHealth{Media: "SSD", Health: "Healthy", Wear: i(95), Temp: i(72)}, "bad", []string{"dh.r.wearBad", "dh.r.warm"}},
		{"windows says unhealthy", diskHealth{Health: "Unhealthy", Status: []string{"Predictive Failure"}}, "bad", []string{"dh.r.unhealthy", "dh.r.predictive"}},
		{"uncorrected errors", diskHealth{Media: "HDD", Health: "Healthy", ReadErr: i64(3), ReadUnc: i64(2)}, "warn", []string{"dh.r.errors"}},
		{"corrected errors only", diskHealth{Media: "HDD", Health: "Healthy", ReadErr: i64(30), ReadUnc: i64(0)}, "good", nil},
		{"usb stick with no data", diskHealth{Media: "Unspecified", Bus: "USB"}, "unknown", nil},
		{"zero temperature means not reported", diskHealth{Media: "HDD", Health: "Healthy", Temp: i(0)}, "good", nil},
		{"nvme says its spare space is low", diskHealth{Media: "SSD", Bus: "NVMe", Health: "Healthy", Critical: 0x01}, "bad", []string{"dh.r.predictive"}},
		{"nvme too hot only", diskHealth{Media: "SSD", Bus: "NVMe", Health: "Healthy", Critical: 0x02, Temp: i(71)}, "warn", []string{"dh.r.warm"}},
		{"one reason once", diskHealth{Health: "Healthy", Status: []string{"Predictive Failure"}, Critical: 0x04}, "bad", []string{"dh.r.predictive"}},
	}
	for _, c := range cases {
		d := c.d
		assessDisk(&d)
		if d.Verdict != c.verdict || !reflect.DeepEqual(d.Reasons, c.reasons) {
			t.Errorf("%s: got %s %v, want %s %v", c.name, d.Verdict, d.Reasons, c.verdict, c.reasons)
		}
	}
}

func TestParseNVMeLog(t *testing.T) {
	if _, ok := parseNVMeLog(make([]byte, 512)); ok {
		t.Error("an all-zero log was taken")
	}
	if _, ok := parseNVMeLog([]byte{1, 2, 3}); ok {
		t.Error("a short log was taken")
	}
	b := make([]byte, 512)
	b[0] = 0x00
	binary.LittleEndian.PutUint16(b[1:], 301) // 28 °C
	b[3], b[5] = 100, 4
	binary.LittleEndian.PutUint64(b[48:], 52_000_000) // ~26.6 TB written
	binary.LittleEndian.PutUint64(b[112:], 1830)
	binary.LittleEndian.PutUint64(b[128:], 12_345)
	binary.LittleEndian.PutUint64(b[144:], 61)
	binary.LittleEndian.PutUint64(b[160:], 0)
	l, ok := parseNVMeLog(b)
	if !ok || l.TempK != 301 || l.Used != 4 || l.Spare != 100 || l.Hours != 12345 || l.Cycles != 1830 || l.Unsafe != 61 || l.Written != 52_000_000 {
		t.Fatalf("%+v %v", l, ok)
	}
	// Windows gave temperature and wear, no hours (as on a FireCuda 530)
	i := func(v int) *int { return &v }
	d := diskHealth{Media: "SSD", Bus: "NVMe", Health: "Healthy", Temp: i(28), Wear: i(0)}
	d.useNVMe(l)
	assessDisk(&d)
	if *d.Temp != 28 || *d.Wear != 4 || d.Hours == nil || *d.Hours != 12345 || *d.Starts != 1830 || d.ReadUnc == nil || *d.ReadUnc != 0 || d.Verdict != "good" {
		t.Errorf("%+v", d)
	}
	// counters Windows already had are kept when the log has none
	d = diskHealth{Bus: "NVMe", Hours: i(99), ReadUnc: new(int64)}
	*d.ReadUnc = 5
	d.useNVMe(nvmeLog{TempK: 0, Used: 1})
	if d.Temp != nil || *d.Hours != 99 || *d.ReadUnc != 5 {
		t.Errorf("kept: %+v", d)
	}
	// a 128-bit counter that does not fit
	binary.LittleEndian.PutUint64(b[136:], 1)
	if l, _ := parseNVMeLog(b); l.Hours != 1<<63-1 {
		t.Errorf("huge: %d", l.Hours)
	}
}

func smartTable(attrs [][4]int64) []byte { // id, cur, worst, raw
	b := make([]byte, 512)
	for i, a := range attrs {
		o := 2 + 12*i
		b[o], b[o+3], b[o+4] = byte(a[0]), byte(a[1]), byte(a[2])
		for k := 0; k < 6; k++ {
			b[o+5+k] = byte(a[3] >> (8 * k))
		}
	}
	return b
}

func TestParseSmart(t *testing.T) {
	data := smartTable([][4]int64{{1, 50, 48, 0}, {5, 200, 200, 0}, {9, 45, 45, 40292}, {194, 112, 98, 30 | 20<<16 | 45<<32}, {197, 200, 200, 2}, {198, 200, 200, 2}, {199, 200, 200, 0}})
	thr := make([]byte, 512)
	for i, p := range [][2]byte{{1, 51}, {5, 140}, {197, 0}} {
		thr[2+12*i], thr[3+12*i] = p[0], p[1]
	}
	as := parseSmart(data, thr)
	if len(as) != 7 {
		t.Fatalf("%d attributes: %+v", len(as), as)
	}
	if !as[0].Bad || as[0].Thr != 51 || as[1].Bad || as[1].Thr != 140 {
		t.Errorf("thresholds: %+v %+v", as[0], as[1])
	}
	if smartValue(as, 9) != 40292 || smartValue(as, 194) != 30 || smartValue(as, 197) != 2 || smartValue(as, 4) != 0 {
		t.Errorf("values: hours %d temp %d pending %d", smartValue(as, 9), smartValue(as, 194), smartValue(as, 197))
	}
	if parseSmart(nil, nil) != nil || parseSmart(make([]byte, 512), nil) != nil {
		t.Error("an empty table gives attributes")
	}
}

func TestEventsForDisk(t *testing.T) {
	day := int64(86400e3)
	evs := []rawEvent{
		{P: "disk", ID: 7, T: 10 * day, M: `The device, \Device\Harddisk1\DR1, has a bad block.`, A: []string{`\Device\Harddisk1\DR1`}},
		{P: "disk", ID: 7, T: 12 * day, M: `The device, \Device\Harddisk1\DR1, has a bad block.`},
		{P: "disk", ID: 153, T: 11 * day, M: "The IO operation at logical block address 0x9 for Disk 1 (PDO name: \\Device\\00000034) was retried."},
		{P: "disk", ID: 153, T: 11 * day, M: "Операцията за вход/изход на логически адрес 0x9 за Диск 1 беше повторена."},
		{P: "disk", ID: 7, T: 13 * day, M: `The device, \Device\Harddisk10\DR10, has a bad block.`},
		{P: "disk", ID: 153, T: 13 * day, M: "The IO operation ... for Disk 10 was retried."},
		{P: "disk", ID: 7, T: 13 * day, M: `The device, \Device\Harddisk0\DR0, has a bad block.`},
		{P: "Ntfs", ID: 55, T: 9 * day, M: "A corruption was discovered in the file system structure on volume E:."},
		{P: "Ntfs", ID: 55, T: 9 * day, M: "A corruption was discovered in the file system structure on volume C:."},
		{P: "Ntfs", ID: 98, T: 9 * day, M: `Volume D: (\Device\HarddiskVolume3) needs to be taken offline.`},
		{P: "Ntfs", ID: 140, T: 8 * day, M: `The system failed to flush data. Path E:\Games\x.dat`},
	}
	got := eventsForDisk(evs, "1", []string{"E"})
	if len(got) != 3 {
		t.Fatalf("%d kinds: %+v", len(got), got)
	}
	if got[0].ID != 7 || got[0].Count != 2 || got[0].First != 10*day || got[0].Last != 12*day || got[0].Key != "dh.e.badblock" {
		t.Errorf("bad blocks: %+v", got[0])
	}
	if got[1].ID != 153 || got[1].Count != 2 || got[1].Key != "dh.e.retried" {
		t.Errorf("retried (English and Bulgarian): %+v", got[1])
	}
	if got[2].Provider != "Ntfs" || got[2].ID != 55 || got[2].Count != 1 || got[2].Key != "dh.e.fs" {
		t.Errorf("file system of E: %+v", got[2])
	}
	if eventsForDisk(evs, "", nil) != nil {
		t.Error("no disk number")
	}
}

func TestAdviseDisk(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	keys := func(as []diskAdvice) string {
		var k []string
		for _, a := range as {
			k = append(k, a.Key+"/"+a.Action+"/"+strconv.FormatInt(a.N, 10))
		}
		return strings.Join(k, " ")
	}
	// the WD drive of the report: 4 uncorrected reads, 2 pending sectors
	wd := diskHealth{Media: "HDD", Health: "Healthy", ReadUnc: i64(4), Verdict: "warn"}
	smart := parseSmart(smartTable([][4]int64{{197, 200, 200, 2}, {199, 200, 200, 0}}), nil)
	got := keys(adviseDisk(wd, smart, nil, nil, []string{"E"}, false))
	if got != "dh.a.backup/backup/0 dh.a.sectors//2 dh.a.unc//4 dh.a.fix/fix/0 dh.a.watch//0" {
		t.Errorf("WD: %s", got)
	}
	// the same on the system drive: no bad sector check offered here (it needs a restart)
	if got := keys(adviseDisk(wd, smart, nil, nil, []string{"C"}, true)); !strings.Contains(got, "dh.a.fixSys") || strings.Contains(got, "/fix/") {
		t.Errorf("system: %s", got)
	}
	// a cable: transfer errors only
	cable := parseSmart(smartTable([][4]int64{{199, 200, 200, 37}}), nil)
	if got := keys(adviseDisk(diskHealth{Media: "HDD"}, cable, nil, nil, []string{"D"}, false)); got != "dh.a.cable//37" {
		t.Errorf("cable: %s", got)
	}
	// the file system's errors in the log: a read-only check first
	evs := []diskEvent{{Provider: "Ntfs", ID: 55, Count: 3, Key: "dh.e.fs"}}
	if got := keys(adviseDisk(diskHealth{Media: "HDD"}, nil, evs, nil, []string{"D"}, false)); got != "dh.a.fs/scan/3" {
		t.Errorf("file system: %s", got)
	}
	// NVMe: spare space at its limit
	nv := &nvmeLog{Spare: 9, SpareMin: 10}
	if got := keys(adviseDisk(diskHealth{Bus: "NVMe"}, nil, nil, nv, []string{"C"}, true)); !strings.HasPrefix(got, "dh.a.backup") {
		t.Errorf("nvme: %s", got)
	}
	if got := keys(adviseDisk(diskHealth{Media: "SSD", Verdict: "good"}, nil, nil, &nvmeLog{Spare: 100, SpareMin: 10}, []string{"C"}, true)); got != "dh.a.ok//0" {
		t.Errorf("healthy: %s", got)
	}
}
