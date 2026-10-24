package main

// The virus scanner page drives Microsoft Defender's own engine (MpCmdRun and
// the Defender PowerShell module): its signatures are updated by Windows every
// day, which no engine of this app could match. Defender acts on what it finds
// as its settings say (usually quarantine); anything left active is listed for
// the user to remove. Free of Windows calls.

import (
	"encoding/json"
	"strings"
)

type avThreat struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Sev    int      `json:"sev"` // 1 low, 2 moderate, 4 high, 5 severe
	Status string   `json:"status"`
	Active bool     `json:"active"`
	Files  []string `json:"files"`
	At     int64    `json:"at"` // unix ms
}

// avStatus: Defender's ThreatStatusID in plain terms.
func avStatus(id int) string {
	switch id {
	case 2, 3, 4, 6:
		return "done" // cleaned, quarantined, removed, blocked
	case 5:
		return "allowed"
	}
	return "active" // detected, or the action failed
}

// parseAVThreats reads the detection list (ConvertTo-Json: one object or an array).
func parseAVThreats(out string) []avThreat {
	out = strings.TrimSpace(out)
	if out == "" {
		return []avThreat{}
	}
	if strings.HasPrefix(out, "{") {
		out = "[" + out + "]"
	}
	var raw []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Sev    int    `json:"sev"`
		Status int    `json:"status"`
		Active bool   `json:"active"`
		Files  any    `json:"files"` // a string when there is one
		At     int64  `json:"at"`
	}
	if json.Unmarshal([]byte(out), &raw) != nil {
		return []avThreat{}
	}
	res := make([]avThreat, 0, len(raw))
	for _, r := range raw {
		t := avThreat{ID: r.ID, Name: r.Name, Sev: r.Sev, Status: avStatus(r.Status), At: r.At}
		t.Active = r.Active || t.Status == "active"
		switch f := r.Files.(type) {
		case string:
			t.Files = []string{avResource(f)}
		case []any:
			for _, x := range f {
				if s, ok := x.(string); ok {
					t.Files = append(t.Files, avResource(s))
				}
			}
		}
		res = append(res, t)
	}
	return res
}

// avResource: "file:_C:\x.exe" → "C:\x.exe" (Defender's own prefix).
func avResource(s string) string {
	if i := strings.Index(s, ":_"); i > 0 && i < 12 {
		return s[i+2:]
	}
	return s
}
