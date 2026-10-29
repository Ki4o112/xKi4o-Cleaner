package main

// Processes page: what a process may get besides closing — a priority
// (now, or remembered for the program), Efficiency mode (like Task
// Manager's) and a pause. Free of Windows calls.

import "strings"

// prioLevels: the page's names for Windows' priority classes.
var prioLevels = map[string]uint32{"idle": 0x40, "below": 0x4000, "normal": 0x20, "above": 0x8000, "high": 0x80}

// ifeoPrio: the same levels as Windows remembers them for a program
// (Image File Execution Options\<exe>\PerfOptions\CpuPriorityClass).
var ifeoPrio = map[string]uint32{"idle": 1, "normal": 2, "high": 3, "below": 5, "above": 6}

func prioName(class uint32) string {
	for k, v := range prioLevels {
		if v == class {
			return k
		}
	}
	return ""
}

func ifeoPrioName(v uint32) string {
	for k, x := range ifeoPrio {
		if x == v {
			return k
		}
	}
	return ""
}

// prioRule: a priority the app asked Windows to give a program every time.
type prioRule struct {
	Exe   string `json:"exe"`
	Level string `json:"level"`
}

// setRule puts or replaces the rule for exe ("" level removes it).
func setRule(rules []prioRule, exe, level string) []prioRule {
	out := make([]prioRule, 0, len(rules)+1)
	for _, r := range rules {
		if !strings.EqualFold(r.Exe, exe) {
			out = append(out, r)
		}
	}
	if level != "" {
		out = append(out, prioRule{Exe: exe, Level: level})
	}
	return out
}

type procCtlResult struct {
	Done   int    `json:"done"`
	Failed int    `json:"failed"`
	Error  string `json:"error,omitempty"`
}

type procCtlInfo struct {
	Prio   string `json:"prio"`           // now ("" = unknown)
	Eco    bool   `json:"eco"`            // Efficiency mode
	Paused bool   `json:"paused"`         // paused by the app
	Rule   string `json:"rule,omitempty"` // remembered level for the program
	Can    bool   `json:"can"`            // the app may change it
	Why    string `json:"why,omitempty"`  // why not
	Game   bool   `json:"game,omitempty"` // a game: never opened (anti-cheats watch that), only remembered
}
