package main

import (
	"testing"
	"time"
)

func TestNetRate(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	a := netSample{at: t0, rx: 1000, tx: 500}
	b := netSample{at: t0.Add(2 * time.Second), rx: 5000, tx: 1500}
	if rx, tx := netRate(a, b); rx != 2000 || tx != 500 {
		t.Errorf("rate %v %v", rx, tx)
	}
	if rx, tx := netRate(netSample{}, b); rx != 0 || tx != 0 {
		t.Error("the first sample has no rate")
	}
	if rx, tx := netRate(b, netSample{at: t0.Add(3 * time.Second), rx: 10, tx: 2000}); rx != 0 || tx != 500 {
		t.Errorf("a counter that went back: %v %v", rx, tx)
	}
	if rx, _ := netRate(a, netSample{at: t0.Add(time.Millisecond), rx: 9000}); rx != 0 {
		t.Error("two samples at once")
	}
	if gpuSum([]float64{30.4, 20.2, -1}) != 51 || gpuSum([]float64{80, 70}) != 100 || gpuSum(nil) != 0 {
		t.Error("gpuSum")
	}
}
