package main

import "testing"

func TestBatteryMath(t *testing.T) {
	b := batDetail{Remaining: 30000, Full: 45000, Design: 60000, Draw: 10000}
	b.finish()
	if b.Mins != 180 || b.Health != 75 || b.Verdict != "worn" {
		t.Fatalf("%+v", b)
	}
	c := batDetail{OnAC: true, Charging: true, Remaining: 30000, Full: 40000, Design: 41000, Gets: 20000}
	c.finish()
	if c.Mins != 30 || c.Verdict != "good" {
		t.Fatalf("%+v", c)
	}
	if batteryVerdict(55) != "replace" || batteryVerdict(0) != "" {
		t.Fatal("verdicts")
	}
	if (batDetail{OnAC: true, Remaining: 5, Draw: 9}).Mins != 0 || batteryMinutes(batDetail{OnAC: true, Draw: 9, Remaining: 5}) != 0 {
		t.Fatal("on the charger nothing is drawn")
	}
}
