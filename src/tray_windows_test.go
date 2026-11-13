//go:build windows

package main

import "testing"

// The tray menu's quick actions run without a window (no page, no
// notification) and write what they did to the log.
func TestTrayQuickWithoutWindow(t *testing.T) {
	logInit(t.TempDir())
	trayQuick("ram")
	trayQuick("game") // on (nothing chosen to close) ...
	if !gameState().Active {
		t.Fatal("game mode did not turn on from the tray")
	}
	trayQuick("game") // ... and off again
	if gameState().Active {
		t.Fatal("game mode did not turn off from the tray")
	}
	found := false
	for _, o := range logOps(logFilter{Limit: 50}).Ops {
		if o.Kind == "memory" {
			found = true
		}
	}
	if !found {
		t.Fatal("freeing RAM from the tray is not in the log")
	}
}
