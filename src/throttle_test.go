package main

import "testing"

func TestThrottleRules(t *testing.T) {
	hot := thSample{CPU: 80, Limit: 55, TempC: 96}
	if !hot.limited() || thReason(hot) != "heat" {
		t.Fatal("hot and held back")
	}
	if (thSample{CPU: 10, Limit: 40}).limited() {
		t.Fatal("an idle processor that clocks down is no throttling")
	}
	if (thSample{CPU: 90, Limit: 0}).limited() {
		t.Fatal("no counter is no throttling")
	}
	if thReason(thSample{CPU: 90, Limit: 60, Bat: true}) != "battery" || thReason(thSample{CPU: 90, Limit: 60}) != "power" {
		t.Fatal("reasons")
	}
	var ss []thSample
	for i := 0; i < 20; i++ {
		ss = append(ss, thSample{CPU: 70, Limit: 100})
	}
	if thWorth(ss) {
		t.Fatal("never held back")
	}
	for i := 0; i < 10; i++ {
		ss[i].Limit = 60
	}
	if share, busy := thShareOf(ss); share != 0.5 || busy != 20 || !thWorth(ss) {
		t.Fatalf("share %v busy %d", share, busy)
	}
	if thWorth(ss[:8]) {
		t.Fatal("too few samples to tell")
	}
}
