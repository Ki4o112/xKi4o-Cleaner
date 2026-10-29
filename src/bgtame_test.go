package main

import "testing"

func TestBgTameRules(t *testing.T) {
	if !isBgHeavy("SearchIndexer.exe") || !isBgHeavy("TiWorker.exe") || isBgHeavy("explorer.exe") || isBgHeavy("cod.exe") {
		t.Fatal("which processes")
	}
	if !bgShouldTame(10, false) || !bgShouldTame(9999, true) || bgShouldTame(bgIdleAfter, false) {
		t.Fatal("when")
	}
}
