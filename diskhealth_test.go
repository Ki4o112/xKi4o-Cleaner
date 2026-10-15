package main

import (
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
	}
	for _, c := range cases {
		d := c.d
		assessDisk(&d)
		if d.Verdict != c.verdict || !reflect.DeepEqual(d.Reasons, c.reasons) {
			t.Errorf("%s: got %s %v, want %s %v", c.name, d.Verdict, d.Reasons, c.verdict, c.reasons)
		}
	}
}
