package main

// Windows Update control on the Updates page: a pause of 1 to 5 weeks (the
// same values Settings → Windows Update writes) and "security only", which
// defers the big feature updates for a year through Windows Update for
// Business. Updates are never turned off. Free of Windows calls.

import "time"

const wuTimeFmt = "2006-01-02T15:04:05Z"

// wuPauseValues: the registry values of a pause of weeks (1-5) from now.
func wuPauseValues(now time.Time, weeks int) map[string]string {
	weeks = min(5, max(1, weeks))
	start, end := now.UTC().Format(wuTimeFmt), now.UTC().Add(time.Duration(weeks)*7*24*time.Hour).Format(wuTimeFmt)
	return map[string]string{
		"PauseFeatureUpdatesStartTime": start, "PauseFeatureUpdatesEndTime": end,
		"PauseQualityUpdatesStartTime": start, "PauseQualityUpdatesEndTime": end,
		"PauseUpdatesStartTime": start, "PauseUpdatesExpiryTime": end,
	}
}

// wuPausedUntil reads the end of a pause (zero: none, or over).
func wuPausedUntil(expiry string, now time.Time) time.Time {
	t, err := time.Parse(wuTimeFmt, expiry)
	if err != nil || !t.After(now) {
		return time.Time{}
	}
	return t
}
