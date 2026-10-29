package main

// The battery in more detail than System information has: charge cycles,
// what it draws or gets now, and how long it lasts at that rate. Free of
// Windows calls.

type batDetail struct {
	OK        bool   `json:"ok"` // there is a battery
	Charge    int    `json:"charge"`
	OnAC      bool   `json:"onAC"`
	Charging  bool   `json:"charging"`
	Cycles    int    `json:"cycles"`    // 0 = not reported
	Remaining int64  `json:"remaining"` // mWh
	Full      int64  `json:"full"`
	Design    int64  `json:"design"`
	Health    int    `json:"health"`
	Draw      int64  `json:"draw"` // mW it gives now (on battery)
	Gets      int64  `json:"gets"` // mW it charges with
	Mins      int    `json:"mins"` // left at this rate (on battery), or to full (charging); 0 = unknown
	Verdict   string `json:"verdict"`
	Error     string `json:"error,omitempty"`
}

// batteryMinutes: minutes left at the rate it is drawn, or to full when
// charging.
func batteryMinutes(b batDetail) int {
	switch {
	case !b.OnAC && b.Draw > 0 && b.Remaining > 0:
		return int(b.Remaining * 60 / b.Draw)
	case b.Charging && b.Gets > 0 && b.Full > b.Remaining:
		return int((b.Full - b.Remaining) * 60 / b.Gets)
	}
	return 0
}

// batteryVerdict: good, worn (under 80% of new), replace (under 60%), or
// "" when the health is unknown.
func batteryVerdict(health int) string {
	switch {
	case health <= 0:
		return ""
	case health < 60:
		return "replace"
	case health < 80:
		return "worn"
	}
	return "good"
}

func (b *batDetail) finish() {
	b.Health = batteryHealth(b.Full, b.Design)
	b.Verdict = batteryVerdict(b.Health)
	b.Mins = batteryMinutes(*b)
}
