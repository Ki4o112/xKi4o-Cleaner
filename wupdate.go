package main

// The Updates page: Windows' own updates and driver updates, searched and
// installed through Windows Update itself (its COM interface, the same one
// Settings uses). This file reads what the search script prints; the
// Windows side is in wupdate_windows.go.

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

type wuUpdate struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	KB         string   `json:"kb,omitempty"`
	Kind       string   `json:"kind"` // software, driver
	Size       int64    `json:"size"`
	Downloaded bool     `json:"downloaded"`
	Optional   bool     `json:"optional"` // Windows offers it, does not install it by itself
	Reboot     bool     `json:"reboot"`   // may need a restart
	Severity   string   `json:"severity,omitempty"`
	Cats       []string `json:"cats"`
	Date       string   `json:"date,omitempty"`
	Desc       string   `json:"desc,omitempty"`
	DrvClass   string   `json:"drvClass,omitempty"`
	DrvVer     string   `json:"drvVer,omitempty"`
	Display    bool     `json:"display,omitempty"` // a graphics driver
}

type wuHist struct {
	Title  string `json:"title"`
	Date   string `json:"date"`
	Result int    `json:"result"` // 2 done, 3 done with errors, 4 failed, 5 stopped
	Op     int    `json:"op"`     // 1 install, 2 uninstall
}

type wuState struct {
	Updates  []wuUpdate `json:"updates"`
	History  []wuHist   `json:"history"`
	Reboot   bool       `json:"reboot"` // Windows waits for a restart
	Searched int64      `json:"searched"`
	Took     int64      `json:"took"`
	Error    string     `json:"error,omitempty"`
	Admin    bool       `json:"admin"`
	Busy     bool       `json:"busy"`
}

type wuResult struct {
	OK        bool     `json:"ok"`
	Installed int      `json:"installed"`
	Failed    []string `json:"failed,omitempty"`
	Reboot    bool     `json:"reboot"`
	Error     string   `json:"error,omitempty"`
}

var reWuID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// wuIDsOK: only update IDs (GUIDs) go into the install script.
func wuIDsOK(ids []string) bool {
	if len(ids) == 0 || len(ids) > 200 {
		return false
	}
	for _, id := range ids {
		if !reWuID.MatchString(id) {
			return false
		}
	}
	return true
}

// jsonList: Windows PowerShell 5.1 writes a one-item array as the item itself.
func jsonList[T any](raw json.RawMessage) []T {
	var out []T
	if len(raw) == 0 || string(raw) == "null" {
		return out
	}
	if err := json.Unmarshal(raw, &out); err == nil {
		return out
	}
	var one T
	if json.Unmarshal(raw, &one) == nil {
		out = append(out, one)
	}
	return out
}

// wuParse reads the search script's answer: a line "ERR|<hresult>|<text>"
// or one JSON object.
func wuParse(out string) (wuState, error) {
	st := wuState{Updates: []wuUpdate{}, History: []wuHist{}}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "ERR|") {
			p := strings.SplitN(l, "|", 3)
			msg := ""
			if len(p) == 3 {
				msg = strings.TrimSpace(p[2])
			}
			if len(p) >= 2 && p[1] != "" {
				msg = strings.TrimSpace(wuHResult(p[1]) + " " + msg)
			}
			return st, errors.New(msg)
		}
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var raw struct {
			Updates json.RawMessage `json:"updates"`
			History json.RawMessage `json:"history"`
			Reboot  bool            `json:"reboot"`
		}
		if err := json.Unmarshal([]byte(l), &raw); err != nil {
			return st, err
		}
		type rawUp struct {
			ID, Title, KB, Severity, Cats, Date, Desc, DrvClass, DrvVer string
			Type                                                        int
			Size                                                        float64
			Downloaded, BrowseOnly, Reboot                              bool
		}
		for _, u := range jsonList[rawUp](raw.Updates) {
			if !reWuID.MatchString(u.ID) || strings.TrimSpace(u.Title) == "" {
				continue
			}
			w := wuUpdate{ID: u.ID, Title: strings.TrimSpace(u.Title), KB: u.KB, Size: int64(u.Size), Downloaded: u.Downloaded, Optional: u.BrowseOnly,
				Reboot: u.Reboot, Severity: u.Severity, Date: u.Date, Desc: strings.TrimSpace(u.Desc), DrvClass: u.DrvClass, DrvVer: u.DrvVer, Kind: "software", Cats: []string{}}
			if u.Type == 2 {
				w.Kind = "driver"
				w.Display = strings.EqualFold(u.DrvClass, "Display")
			}
			for _, c := range strings.Split(u.Cats, "|") {
				if c = strings.TrimSpace(c); c != "" {
					w.Cats = append(w.Cats, c)
				}
			}
			if w.KB != "" && !strings.HasPrefix(w.KB, "KB") {
				w.KB = "KB" + w.KB
			}
			st.Updates = append(st.Updates, w)
		}
		st.History = append(st.History, jsonList[wuHist](raw.History)...)
		st.Reboot = raw.Reboot
		return st, nil
	}
	return st, errors.New(T("wu.err.noAnswer"))
}

// wuHResult: the usual Windows Update error codes in words.
func wuHResult(hr string) string {
	v, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(hr)), "0x"), 16, 64)
	if err != nil {
		if n, err2 := strconv.ParseInt(strings.TrimSpace(hr), 10, 64); err2 == nil {
			v = n
		} else {
			return ""
		}
	}
	code := uint32(v)
	switch code {
	case 0x80240438, 0x8024402C, 0x80072EE7, 0x80072EFD, 0x80072EE2:
		return T("wu.hr.net")
	case 0x8024002E, 0x80070422:
		return T("wu.hr.off")
	case 0x80240016:
		return T("wu.hr.busy")
	case 0x8024001E:
		return T("wu.hr.stopped")
	case 0x80240044:
		return T("wu.hr.policy")
	}
	return "0x" + strings.ToUpper(strconv.FormatUint(uint64(code), 16))
}

// wuLine reads one line of the install script: STEP|i|phase|id, DONE|i|id|result|hresult|reboot.
type wuLineInfo struct {
	Kind   string
	I      int
	Phase  string
	ID     string
	Result int
	HR     string
	Reboot bool
	Count  int
}

func wuLine(l string) (wuLineInfo, bool) {
	p := strings.Split(strings.TrimSpace(l), "|")
	atoi := func(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
	switch {
	case len(p) == 2 && p[0] == "COUNT":
		return wuLineInfo{Kind: "count", Count: atoi(p[1])}, true
	case len(p) == 4 && p[0] == "STEP":
		return wuLineInfo{Kind: "step", I: atoi(p[1]), Phase: p[2], ID: p[3]}, true
	case len(p) == 6 && p[0] == "DONE":
		return wuLineInfo{Kind: "done", I: atoi(p[1]), ID: p[2], Result: atoi(p[3]), HR: p[4], Reboot: p[5] == "1" || strings.EqualFold(p[5], "true")}, true
	case len(p) >= 2 && p[0] == "ERR":
		return wuLineInfo{Kind: "err", HR: p[1]}, true
	}
	return wuLineInfo{}, false
}
