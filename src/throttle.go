package main

// The processor slowing itself down: Windows' "% Performance Limit" counter
// says how much of its speed the processor may use (100 = all of it). A
// limit while the processor is busy is throttling; the reason is told from
// the temperature and the power source. Free of Windows calls.

type thSample struct {
	At    int64   `json:"at"` // unix ms
	CPU   float64 `json:"cpu"`
	Limit float64 `json:"limit"` // %; 100 = not limited
	Perf  float64 `json:"perf"`  // % of the base speed it runs at (over 100 = boost)
	TempC float64 `json:"temp"`  // the hottest thermal zone; 0 = unknown
	Bat   bool    `json:"bat"`   // on battery
}

type thEvent struct {
	At     int64   `json:"at"`
	Secs   int     `json:"secs"`
	Limit  float64 `json:"limit"` // the lowest limit seen
	TempC  float64 `json:"temp"`
	Reason string  `json:"reason"` // heat, battery, power
}

const (
	thBusy     = 30 // % CPU: busy enough for a limit to matter
	thLimited  = 90 // % limit under which the processor is held back
	thHot      = 90 // °C: the limit comes from heat
	thWindow   = 60 // samples kept (5 minutes at 5 s)
	thShare    = 0.4
	thMinBusy  = 12
	thNoticeMs = 3600 * 1000 // one notification an hour at most
)

func (s thSample) busy() bool    { return s.CPU >= thBusy }
func (s thSample) limited() bool { return s.busy() && s.Limit > 0 && s.Limit < thLimited }

// thReason: why the processor is held back.
func thReason(s thSample) string {
	switch {
	case s.TempC >= thHot:
		return "heat"
	case s.Bat:
		return "battery"
	}
	return "power"
}

// thShareOf: of the busy samples, the part that was held back.
func thShareOf(ss []thSample) (share float64, busy int) {
	lim := 0
	for _, s := range ss {
		if s.busy() {
			busy++
			if s.limited() {
				lim++
			}
		}
	}
	if busy == 0 {
		return 0, 0
	}
	return float64(lim) / float64(busy), busy
}

// thWorth: the last minutes were held back often enough to tell the user.
func thWorth(ss []thSample) bool {
	share, busy := thShareOf(ss)
	return busy >= thMinBusy && share >= thShare
}
