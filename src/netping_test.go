package main

import "testing"

func TestPingStats(t *testing.T) {
	r := pingStats([]float64{10, 12, -1, 14, 10})
	if r.Sent != 5 || r.Lost != 1 || r.Loss != 20 {
		t.Errorf("counts: %+v", r)
	}
	if r.Min != 10 || r.Max != 14 || r.Avg != 11.5 {
		t.Errorf("times: %+v", r)
	}
	// answers in a row: 10→12→14→10: 2, 2, 4 → 2.7
	if r.Jitter != 2.7 {
		t.Errorf("jitter %v", r.Jitter)
	}
	if z := pingStats([]float64{-1, -1}); z.Loss != 100 || z.Avg != 0 || z.Jitter != 0 {
		t.Errorf("all lost: %+v", z)
	}
	if one := pingStats([]float64{7}); one.Avg != 7 || one.Jitter != 0 {
		t.Errorf("one: %+v", one)
	}
	if e := pingStats(nil); e.Sent != 0 || e.Loss != 0 {
		t.Errorf("none: %+v", e)
	}
}
