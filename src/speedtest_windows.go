//go:build windows

package main

// speedRun is the page's binding: it reports progress and logs the result.
func speedRun() speedResult {
	speedProgress = func(phase string, mbps float64, pct int) {
		emit("speed-progress", map[string]any{"phase": phase, "mbps": mbps, "pct": pct})
	}
	op := beginOp("speed", "", T("log.speed"), nil)
	r := speedTest()
	if r.Error == "busy" {
		r.Error = T("speed.err.busy")
	} else if r.Error != "" && r.Down == 0 {
		op.fail("", r.Error, nil)
		r.Error = T("speed.err.net", r.Error)
	}
	op.end(T("log.speedDone", r.Down, r.Up, r.Ping), map[string]any{"down": r.Down, "up": r.Up, "ping": r.Ping})
	return r
}
