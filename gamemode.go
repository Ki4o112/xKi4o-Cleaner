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
