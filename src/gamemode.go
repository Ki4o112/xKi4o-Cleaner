package main

// Game mode: close the programs the user chose, free memory, and start them
// again when the game is over. Nothing else changes: no power plan, no
// services, no Windows settings.

import (
	"sort"
	"strings"
)

type gmProc struct {
	PID     uint32
	PPID    uint32
	Created int64
	Exe     string // lower case
	Path    string
	Cmd     string
}

// gmClosed is a program closed by game mode, with what is needed to start it again.
type gmClosed struct {
	Exe  string `json:"exe"`
	Name string `json:"name"`
	Path string `json:"path"`
	Cmd  string `json:"cmd,omitempty"` // its command line ("" = just the program)
}

type gmState struct {
	Active bool       `json:"active"`
	Since  int64      `json:"since"` // unix ms
	Closed []gmClosed `json:"closed"`
	Left   []string   `json:"left,omitempty"` // chosen but still running (refused to close)
	Freed  int64      `json:"freed"`          // memory freed at the start
	// the power plan or power mode game mode switched to, and the user's own
	Plan        string `json:"plan,omitempty"`
	PrevPlan    string `json:"prevPlan,omitempty"`
	Overlay     string `json:"overlay,omitempty"`
	PrevOverlay string `json:"prevOverlay,omitempty"`
}

// gmRoots: the first process of each chosen program (not the helpers it
// started itself), one per distinct command line, at most 3 per program.
func gmRoots(procs []gmProc, want map[string]bool) []gmProc {
	byPID := map[uint32]gmProc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	var out []gmProc
	seen := map[string]bool{}
	per := map[string]int{}
	sort.SliceStable(procs, func(i, j int) bool { return procs[i].Created < procs[j].Created })
	for _, p := range procs {
		if !want[p.Exe] {
			continue
		}
		if par, ok := byPID[p.PPID]; ok && par.Exe == p.Exe && par.Created <= p.Created {
			continue // a helper of the same program
		}
		k := p.Exe + "|" + strings.ToLower(strings.TrimSpace(p.Cmd))
		if seen[k] || per[p.Exe] >= 3 {
			continue
		}
		seen[k] = true
		per[p.Exe]++
		out = append(out, p)
	}
	return out
}

// gmToStart: the closed programs that are not running again by now.
func gmToStart(st gmState, running map[string]bool) []gmClosed {
	var out []gmClosed
	for _, c := range st.Closed {
		if !running[strings.ToLower(c.Exe)] && c.Path != "" {
			out = append(out, c)
		}
	}
	return out
}

// gmCommand: what to start — the command line it had, or the program alone.
// A command line must start with the program itself (anything else is not used).
func gmCommand(c gmClosed) string {
	q := `"` + c.Path + `"`
	cmd := strings.TrimSpace(c.Cmd)
	if cmd == "" {
		return q
	}
	lc, lp := strings.ToLower(cmd), strings.ToLower(c.Path)
	switch {
	case strings.HasPrefix(lc, `"`+lp+`"`):
		return q + cmd[len(c.Path)+2:]
	case strings.HasPrefix(lc, lp+" ") || lc == lp:
		return q + cmd[len(c.Path):]
	}
	return q
}

// gmRow: a running process that game mode could close.
type gmRow struct {
	PID, PPID uint32
	Start     int64 // start time: a parent started before its child (PIDs are reused)
	Exe, Path string
}

// gmHelpers finds programs that are only helpers of another listed program:
// every process of theirs was started by that program and lives in its
// folder (Steam's steamwebhelper.exe, a launcher's own tools). They are not
// offered on their own; they close with their program. A program that also
// runs on its own, or was started by one that merely opened it (a browser
// opened from a chat app), stays on its own. generic says when a folder is
// too general to count as a program's own (Program Files, the drive...).
// The result maps the helper's exe to its program's exe.
func gmHelpers(rows []gmRow, generic func(dir string) bool) map[string]string {
	by := make(map[uint32]gmRow, len(rows))
	for _, r := range rows {
		by[r.PID] = r
	}
	top := func(r gmRow) string {
		for i := 0; i < 16; i++ {
			p, ok := by[r.PPID]
			if !ok || p.PID == r.PID || p.Start > r.Start {
				break
			}
			if p.Exe != r.Exe {
				dir := parentDir(p.Path)
				if p.Path == "" || r.Path == "" || generic(dir) || !under(r.Path, dir) {
					break
				}
			}
			r = p
		}
		return r.Exe
	}
	target := map[string]string{}
	alone := map[string]bool{}
	for _, r := range rows {
		t := top(r)
		if t == r.Exe {
			alone[r.Exe] = true
			continue
		}
		if prev, ok := target[r.Exe]; ok && prev != t {
			alone[r.Exe] = true
		}
		target[r.Exe] = t
	}
	for e := range alone {
		delete(target, e) // (a program at the top is always "alone": it is never a helper itself)
	}
	return target
}

// parentDir: the folder of a Windows path (either slash).
func parentDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	switch {
	case i < 0:
		return ""
	case i == 2 && p[1] == ':':
		return p[:3]
	}
	return p[:i]
}
