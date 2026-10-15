package main

// Reading what winget (the Windows Package Manager) prints. "winget upgrade"
// has no machine-readable output, so its table is read by the columns of its
// header (the header words are translated, so only their places are used).
// Long names and ids are cut with "…"; the full id comes from "winget export".

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

type wgUpdate struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	Version   string `json:"version"`
	Available string `json:"available"`
	Source    string `json:"source,omitempty"`
	Explicit  bool   `json:"explicit,omitempty"` // pinned or needs to be named: not in "update all"
	Cut       bool   `json:"cut,omitempty"`      // the id was cut and no full id was found: cannot be updated from here
}

// lastScreen keeps what the terminal would show of a line: the text after
// the last carriage return (winget draws its spinner and bars that way).
func lastScreen(line string) string {
	line = strings.TrimRight(line, "\r")
	if i := strings.LastIndex(line, "\r"); i >= 0 {
		line = line[i+1:]
	}
	return strings.TrimRight(line, " ")
}

func isDashLine(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) >= 10 && strings.Trim(s, "-") == ""
}

// colStarts: where each word of the header starts (in runes).
func colStarts(header string) []int {
	var out []int
	prev := ' '
	for i, r := range []rune(header) {
		if r != ' ' && prev == ' ' {
			out = append(out, i)
		}
		prev = r
	}
	return out
}

func cutCols(line string, starts []int) []string {
	rs := []rune(line)
	out := make([]string, len(starts))
	for i, s := range starts {
		if s >= len(rs) {
			break
		}
		e := len(rs)
		if i+1 < len(starts) && starts[i+1] < e {
			e = starts[i+1]
		}
		out[i] = strings.TrimSpace(string(rs[s:e]))
	}
	return out
}

// parseWingetUpgrade reads the tables of "winget upgrade". The first table
// is what "upgrade --all" would update; a second one (after a line of text)
// lists packages that must be named (pinned, or of unknown version).
func parseWingetUpgrade(out string) []wgUpdate {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = lastScreen(lines[i])
	}
	var res []wgUpdate
	table := 0
	for i := 1; i < len(lines); i++ {
		if !isDashLine(lines[i]) {
			continue
		}
		starts := colStarts(lines[i-1])
		if len(starts) < 4 || len(starts) > 5 {
			continue
		}
		table++
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" || utf8.RuneCountInString(l) < starts[len(starts)-1] {
				// the end of the table: a blank line or a line of text ("5 upgrades available.")
				i = j
				break
			}
			c := cutCols(l, starts)
			u := wgUpdate{Name: c[0], ID: c[1], Version: c[2], Available: c[3], Explicit: table > 1}
			if len(c) > 4 {
				u.Source = c[4]
			}
			if u.ID == "" || u.Available == "" || strings.ContainsRune(u.ID, ' ') || !looksVersion(u.Available) {
				i = j
				break
			}
			res = append(res, u)
			i = j
		}
	}
	return res
}

func looksVersion(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

type wgExport struct {
	Sources []struct {
		Packages []struct {
			ID      string `json:"PackageIdentifier"`
			Version string `json:"Version"`
		} `json:"Packages"`
		SourceDetails struct {
			Name string `json:"Name"`
		} `json:"SourceDetails"`
	} `json:"Sources"`
}

// fullIDs puts back the ids the table cut ("Microsoft.VisualStudio…") from
// the list of installed packages that winget export wrote.
func fullIDs(ups []wgUpdate, exportJSON []byte) []wgUpdate {
	var ex wgExport
	_ = json.Unmarshal(exportJSON, &ex)
	var ids []string
	for _, s := range ex.Sources {
		for _, p := range s.Packages {
			ids = append(ids, p.ID)
		}
	}
	for i := range ups {
		id := ups[i].ID
		if !strings.HasSuffix(id, "…") && !strings.HasSuffix(id, "...") {
			continue
		}
		prefix := strings.TrimSuffix(strings.TrimSuffix(id, "…"), "...")
		var hit []string
		for _, f := range ids {
			if strings.HasPrefix(strings.ToLower(f), strings.ToLower(prefix)) {
				hit = append(hit, f)
			}
		}
		if len(hit) == 1 {
			ups[i].ID = hit[0]
		} else {
			ups[i].Cut = true
		}
	}
	return ups
}

// wingetMessage picks the line worth showing from winget's output (the
// last line that is not a progress bar).
func wingetMessage(out string) string {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lastScreen(lines[i]))
		if l == "" || strings.Trim(l, `-\|/ █▒░`) == "" || strings.ContainsAny(l, "█▒") {
			continue
		}
		if len([]rune(l)) > 200 {
			l = string([]rune(l)[:200]) + "…"
		}
		return l
	}
	return ""
}

// validWingetID: what may be passed to winget as an id.
func validWingetID(id string) bool {
	if id == "" || len(id) > 128 || strings.HasPrefix(id, "-") {
		return false
	}
	for _, r := range id {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(".-_+{}", r) {
			return false
		}
	}
	return true
}
