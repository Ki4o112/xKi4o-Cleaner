//go:build windows

package main

import "time"

const (
	bsodQueryCrash = "*[System[Provider[@Name='Microsoft-Windows-WER-SystemErrorReporting'] and EventID=1001]]"
	bsodQueryPower = "*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and EventID=41]]"
)

// bsodRecent reads the last crashes and sudden power-offs (newest first).
func bsodRecent() bsodList {
	res := bsodList{Events: []bsodEvent{}}
	for _, q := range []struct {
		query string
		power bool
	}{{bsodQueryCrash, false}, {bsodQueryPower, true}} {
		out, code, err := runQuiet(30*time.Second, sys32("wevtutil.exe"), "qe", "System", "/q:"+q.query, "/f:xml", "/rd:true", "/c:40")
		if err != nil || code != 0 {
			if !q.power {
				res.Error = T("bs.err.read", errText(err, code))
			}
			continue
		}
		res.Events = append(res.Events, parseBugcheckEvents(out, q.power)...)
	}
	sortBsod(res.Events)
	if len(res.Events) > 40 {
		res.Events = res.Events[:40]
	}
	return res
}

// memTest starts Windows' own memory test (it asks to restart now or later).
func memTest() bool {
	shellOpen(sys32("mdsched.exe"))
	return true
}
