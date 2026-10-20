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

// wingetHint: a plain explanation (a text key) for the codes winget ends
// with when an update cannot be done from here.
func wingetHint(code uint32) string {
	switch {
	case code == 0x80190193:
		return "wg.h.403" // the vendor's server refuses winget's download
	case code == 0x80190194:
		return "wg.h.404"
	case code&0xFFFFF000 == 0x80190000:
		return "wg.h.http"
	case code == 0x8A15008E:
		return "wg.h.tech" // installed another way than winget's package
	case code == 0x8A150101 || code == 0x8A150103 || code == 0x8A150111:
		return "wg.h.inUse"
	case code == 0x8A150109 || code == 0x8A15010A:
		return "wg.h.reboot"
	case code == 0x8A150107:
		return "wg.h.net"
	case code == 0x8A150105:
		return "wg.h.disk"
	case code == 0x8A15010F:
		return "wg.h.policy"
	case code == 0x8A150011:
		return "wg.h.hash"
	}
	return ""
}

// wingetSiteHint: hints after which the program's own site is the way to update.
func wingetSiteHint(h string) bool {
	return h == "wg.h.403" || h == "wg.h.404" || h == "wg.h.http" || h == "wg.h.tech" || h == "wg.h.hash" || h == "wg.h.gone"
}

// wingetSelfUpdating: programs that update themselves every time they start
// and that winget's update can break (the Epic Games Launcher's update
// removed it). They are not offered for updating.
func wingetSelfUpdating(id string) bool {
	l := strings.ToLower(id)
	switch l {
	case "epicgames.epicgameslauncher", "valve.steam", "blizzard.battlenet", "electronicarts.eadesktop", "electronicarts.origin",
		"ubisoft.connect", "gog.galaxy", "discord.discord", "discord.discord.ptb", "discord.discord.canary", "spotify.spotify",
		"rockstargames.launcher":
		return true
	}
	return strings.HasPrefix(l, "riotgames.")
}

// selfUpdater: browsers that update themselves (Omaha updaters). winget
// cannot update them when they were installed by their own setup ("install
// technology is different"); their updater can, the way their scheduled
// task does it.
func selfUpdater(id string) string {
	switch strings.ToLower(id) {
	case "microsoft.edge", "microsoft.edge.beta", "microsoft.edge.dev":
		return "edge"
	case "google.chrome", "google.chrome.beta", "google.chrome.dev":
		return "chrome"
	case "brave.brave", "brave.brave.beta":
		return "brave"
	}
	return ""
}

// wingetShowURL picks the program's page from "winget show": the homepage
// or the publisher's page, never the installer's download link.
func wingetShowURL(out string) string {
	var pick, any string
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(lastScreen(l))
		i := strings.Index(l, "https://")
		if i < 0 {
			continue
		}
		u := strings.Fields(l[i:])[0]
		key := strings.ToLower(l[:i])
		low := strings.ToLower(u)
		if strings.Contains(key, "installer") || strings.HasSuffix(low, ".exe") || strings.HasSuffix(low, ".msi") || strings.HasSuffix(low, ".zip") || strings.HasSuffix(low, ".msix") {
			continue
		}
		if pick == "" && (strings.Contains(key, "homepage") || strings.Contains(key, "publisher url") || strings.Contains(key, "package url")) {
			pick = u
		}
		if any == "" {
			any = u
		}
	}
	if pick != "" {
		return pick
	}
	return any
}

// sameProgram: a name in winget's list and an installed program's name are
// the same program ("7-Zip 24.08 (x64)" / "7-Zip 24.09 (x64)").
func sameProgram(a, b string) bool {
	x, y := normName(cleanAppName(a)), normName(cleanAppName(b))
	if x == "" || y == "" {
		return false
	}
	return x == y // the upgrade list shows the installed name, so the cleaned names are equal
}

// wingetSource: Microsoft Store ids ("9" and 11 more, or "XP" and 12 more
// capitals and digits) install from the msstore source, the rest from winget's own.
func wingetSource(id string) string {
	if ((len(id) == 12 && id[0] == '9') || (len(id) == 14 && strings.HasPrefix(id, "XP"))) && strings.ToUpper(id) == id && validWingetID(id) && !strings.ContainsAny(id, ".-_+{}") {
		return "msstore"
	}
	return "winget"
}
