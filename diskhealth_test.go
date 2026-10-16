package main

import (
	"encoding/binary"
	"reflect"
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
