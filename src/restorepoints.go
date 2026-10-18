package main

// Windows' restore points as the Restore Center lists them, and the plan for
// deleting them (the Windows call itself is in restore_windows.go).

import (
	"sort"
	"strconv"
	"time"
)

type restorePoint struct {
	Seq  int    `json:"seq"`
	Desc string `json:"desc"`
	Type int    `json:"type"`
	Time string `json:"time"` // ISO 8601, UTC
}

type rpList struct {
	Points []restorePoint `json:"points"`
	Error  string         `json:"error,omitempty"`
}

type rpDelResult struct {
	Total   int    `json:"total"`   // restore points found
	Deleted int    `json:"deleted"` // removed
	Failed  int    `json:"failed"`  // Windows refused
	Kept    int    `json:"kept"`    // left on purpose (the newest)
	Error   string `json:"error,omitempty"`
}

// rpDeletePlan returns the restore points to remove, newest first: all of
// them, or all but the newest one.
func rpDeletePlan(pts []restorePoint, keepNewest bool) []restorePoint {
	out := append([]restorePoint(nil), pts...)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if keepNewest && len(out) > 0 {
		out = out[1:]
	}
	return out
}

// removeRestorePoints removes the planned points one by one and reports
// each in the operation's log.
func removeRestorePoints(pts []restorePoint, keepNewest bool, remove func(seq int) error, op *opHandle) rpDelResult {
	plan := rpDeletePlan(pts, keepNewest)
	res := rpDelResult{Total: len(pts), Kept: len(pts) - len(plan)}
	for _, p := range plan {
		what := rpLabel(p)
		if err := remove(p.Seq); err != nil {
			res.Failed++
			op.fail(what, err.Error(), map[string]any{"seq": p.Seq})
			continue
		}
		res.Deleted++
		op.info(what, T("log.rpDeleted"), map[string]any{"seq": p.Seq})
	}
	return res
}

// rpOwnDesc: the name the app gives the restore points it makes.
const rpOwnDesc = "xKi4o Cleaner"

// rpTrimPlan: the app's own restore points beyond the newest keep, oldest
// last. Points Windows or other programs made are never in it.
func rpTrimPlan(pts []restorePoint, keep int) []restorePoint {
	if keep < 1 {
		return nil
	}
	var own []restorePoint
	for _, p := range pts {
		if p.Desc == rpOwnDesc {
			own = append(own, p)
		}
	}
	sort.Slice(own, func(i, j int) bool { return own[i].Seq > own[j].Seq })
	if len(own) <= keep {
		return nil
	}
	return own[keep:]
}

func rpLabel(p restorePoint) string {
	s := p.Desc
	if s == "" {
		s = "#" + strconv.Itoa(p.Seq)
	}
	if t, err := time.Parse(time.RFC3339Nano, p.Time); err == nil {
		s += " (" + t.Local().Format("2006-01-02 15:04") + ")"
	}
	return s
}
