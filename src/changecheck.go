package main

// The Check page: are the tweaks and Debloat changes made from the app
// working as they should, and has anything gone wrong since they were made?
// It looks at what Windows changed back, at the services Windows needs, at
// crashes, hangs, blue screens and failed services in the event logs (and
// whether they began after the changes), and at things the changes can break
// (updates, the Store, protection, the network). This file is the logic,
// free of Windows calls; changecheck_windows.go reads the PC.

import (
	"encoding/xml"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ckRef is one change made from the app: a tweak or a Debloat change.
type ckRef struct {
	Kind string `json:"kind"` // tweak, debloat
	ID   string `json:"id"`
	At   int64  `json:"at,omitempty"` // unix seconds; 0 = not known
}

// ckEvent is one entry of the event logs that matters here.
type ckEvent struct {
	At      int64  // unix seconds
	Kind    string // crash, hang, svc, bsod, power, gpu
	Subject string // the program (lower case) or the service, or "" for the PC
}

// ckFinding is one thing the page shows: a problem, or a check that passed.
type ckFinding struct {
	ID       string   `json:"id"`  // what was checked (the page has its texts)
	Sev      string   `json:"sev"` // bad, warn, info, ok
	Args     []string `json:"args,omitempty"`
	Count    int      `json:"count,omitempty"`  // how many times (events)
	Before   int      `json:"before,omitempty"` // as many days before the changes
	First    int64    `json:"first,omitempty"`  // the first time (unix seconds)
	Suspects []ckRef  `json:"suspects,omitempty"`
	Fix      string   `json:"fix,omitempty"` // reapply, undo, svc, page
	FixArg   string   `json:"fixArg,omitempty"`
}

type ckReport struct {
	At       int64       `json:"at"`
	Changes  []ckRef     `json:"changes"`
	Findings []ckFinding `json:"findings"`
	Took     int64       `json:"took"`  // milliseconds
	Steps    []int64     `json:"steps"` // milliseconds each step took
	Error    string      `json:"error,omitempty"`
}

var ckSevRank = map[string]int{"bad": 0, "warn": 1, "info": 2, "ok": 3}

// sortCkFindings: the worst first, then by name; the passed checks last.
func sortCkFindings(fs []ckFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := ckSevRank[fs[i].Sev], ckSevRank[fs[j].Sev]
		if a != b {
			return a < b
		}
		return fs[i].ID < fs[j].ID
	})
}

// The parts of Windows a crash belongs to, and the changes that touch them.
var ckShell = map[string]bool{
	"explorer.exe": true, "startmenuexperiencehost.exe": true, "shellexperiencehost.exe": true, "searchhost.exe": true,
	"searchapp.exe": true, "searchui.exe": true, "textinputhost.exe": true, "ctfmon.exe": true, "sihost.exe": true,
	"runtimebroker.exe": true, "widgets.exe": true, "applicationframehost.exe": true, "systemsettings.exe": true,
	"lockapp.exe": true, "searchindexer.exe": true, "taskmgr.exe": true,
}

var ckSuspectTweaks = map[string][]string{
	"shell": {"classic-menu", "start-recs-off", "taskbar-search-off", "taskview-widgets-off", "bing-off", "compact-view", "home-gallery-off",
		"multi-select-menu", "folder-discovery-off", "explorer-ads-off", "icon-cache-large", "copilot-off", "recall-off", "svc-manual",
		"animations-off", "end-task", "taskbar-labels", "taskbar-left", "clock-seconds", "spotlight-off", "search-low-priority",
		"recent-files-off", "bg-apps-off", "store-openwith-off", "debloat", "edge-remove", "onedrive-remove", "transparency-off"},
	"gpu": {"hags", "gpu-tdr-longer", "timer-res-global", "nvidia-opt", "transparency-off", "animations-off", "windowed-opt", "games-priority",
		"ultimate-power", "dynamic-tick", "core-isolation-off", "mpo-off"},
	"system": {"core-isolation-off", "dynamic-tick", "timer-res-global", "hags", "win32-priority", "ultimate-power", "paging-exec-off",
		"gpu-tdr-longer", "fast-startup-off", "hibernation-off", "wpbt-off", "games-priority", "power-throttling-off", "mpo-off"},
	"svc": {"svc-manual", "printer-manual", "do-off", "sysmain-off"}, // the tweaks that change services
	"boot": {"svc-manual", "startup-delay-off", "fast-startup-off", "hibernation-off", "sysmain-off", "ntfs-8dot3-off", "paging-exec-off",
		"core-isolation-off", "search-low-priority", "bg-apps-off", "power-throttling-off", "debloat", "onedrive-remove", "edge-remove"},
	"app": {"games-priority", "hags", "windowed-opt", "game-mode", "gamebar-off", "nvidia-opt", "timer-res-global", "win32-priority",
		"ultimate-power", "paging-exec-off", "core-isolation-off", "dynamic-tick", "mpo-off", "power-throttling-off"},
}

// ckArea is the part of Windows an event belongs to.
func ckArea(e ckEvent) string {
	switch e.Kind {
	case "bsod", "power":
		return "system"
	case "gpu":
		return "gpu"
	case "svc":
		return "svc"
	}
	if e.Subject == "dwm.exe" {
		return "gpu"
	}
	if ckShell[e.Subject] {
		return "shell"
	}
	return "app"
}

// ckSuspects: the changes that may be the cause of trouble that began at
// first: made in the week before it, the ones known to touch that part of
// Windows first (when none is, all of that week). A change made long before
// the trouble began is not blamed.
func ckSuspects(area string, first int64, changes []ckRef) []ckRef {
	known := map[string]bool{}
	for _, id := range ckSuspectTweaks[area] {
		known[id] = true
	}
	var byArea, byTime []ckRef
	for _, c := range changes {
		if c.At == 0 || c.At > first || first-c.At > 7*86400 {
			continue
		}
		if (c.Kind == "tweak" && known[c.ID]) || (c.Kind == "debloat" && area == "shell") {
			byArea = append(byArea, c)
		}
		byTime = append(byTime, c)
	}
	out := byArea
	if len(out) == 0 {
		out = byTime
	}
	// the most recent first: the likeliest
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// eventFindings: crashes, hangs, blue screens and failed services, each
// told when it began after the changes (more often than in as many days
// before them), or when it keeps happening anyway.
func eventFindings(changes []ckRef, evs []ckEvent, now int64) []ckFinding {
	var t0 int64 // the first change of the last 30 days
	for _, c := range changes {
		if c.At > 0 && now-c.At <= 30*86400 && (t0 == 0 || c.At < t0) {
			t0 = c.At
		}
	}
	type key struct{ kind, subj string }
	groups := map[key][]int64{}
	for _, e := range evs {
		k := key{e.Kind, e.Subject}
		groups[k] = append(groups[k], e.At)
	}
	var out []ckFinding
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].kind+keys[i].subj < keys[j].kind+keys[j].subj })
	for _, k := range keys {
		ts := groups[k]
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		area := ckArea(ckEvent{Kind: k.kind, Subject: k.subj})
		heavy := k.kind == "bsod" || k.kind == "power"
		if t0 > 0 {
			span := now - t0
			if span < 86400 {
				span = 86400
			}
			after, before := 0, 0
			var first int64
			for _, t := range ts {
				switch {
				case t >= t0:
					if first == 0 {
						first = t
					}
					after++
				case t >= t0-span:
					before++
				}
			}
			need := 2
			if heavy {
				need = 1
			}
			if area == "app" {
				need = 3 // programs crash for many reasons: only a clear change counts
			}
			if after >= need && after > 2*before {
				sev := "warn"
				if heavy || (area == "shell" && after >= 3) || (area == "gpu" && after >= 3) {
					sev = "bad"
				}
				sus := ckSuspects(area, first, changes)
				id := "ev-" + k.kind
				if len(sus) == 0 {
					// it began well after the last change: new, but not theirs
					id += "-new"
					if !heavy {
						sev = "info"
					} else {
						sev = "warn"
					}
				}
				out = append(out, ckFinding{ID: id, Sev: sev, Args: []string{k.subj}, Count: after, Before: before, First: first, Suspects: sus})
				continue
			}
		}
		// not tied to the changes, but it keeps happening: the last 7 days
		recent := 0
		var first int64
		for _, t := range ts {
			if now-t <= 7*86400 {
				if first == 0 {
					first = t
				}
				recent++
			}
		}
		limit := 3
		if heavy {
			limit = 1
		}
		if area == "app" {
			limit = 5
		}
		if recent >= limit {
			out = append(out, ckFinding{ID: "ev-" + k.kind + "-old", Sev: "info", Args: []string{k.subj}, Count: recent, First: first})
		}
	}
	return out
}

// ckWant is a service Windows needs: Auto ones run all the time. Def is its
// Windows default start type (2 automatic, 3 manual), used to repair it.
type ckWant struct {
	Name    string
	Auto    bool
	Area    string // what stops working without it
	Def     uint32
	Delayed bool
}

var ckServices = []ckWant{
	{"RpcSs", true, "core", 2, false}, {"DcomLaunch", true, "core", 2, false}, {"RpcEptMapper", true, "core", 2, false},
	{"LSM", true, "core", 2, false}, {"BrokerInfrastructure", true, "core", 2, false}, {"SystemEventsBroker", true, "core", 2, false},
	{"Power", true, "core", 2, false}, {"ProfSvc", true, "core", 2, false}, {"UserManager", true, "core", 2, false},
	{"CoreMessagingRegistrar", true, "core", 2, false}, {"Schedule", true, "core", 2, false}, {"EventLog", true, "core", 2, false},
	{"Winmgmt", true, "core", 2, false}, {"CryptSvc", true, "core", 2, false}, {"StateRepository", false, "start", 3, false},
	{"Dhcp", true, "net", 2, false}, {"Dnscache", true, "net", 2, false}, {"nsi", true, "net", 2, false},
	{"NlaSvc", false, "net", 2, false}, {"netprofm", false, "net", 3, false},
	{"BFE", true, "security", 2, false}, {"mpssvc", true, "security", 2, false}, {"WinDefend", false, "security", 2, false},
	{"SecurityHealthService", false, "security", 3, false},
	{"AudioSrv", true, "audio", 2, false}, {"AudioEndpointBuilder", true, "audio", 2, false},
	{"Themes", true, "look", 2, false}, {"FontCache", true, "look", 2, false},
	{"wuauserv", false, "update", 3, false}, {"BITS", false, "update", 3, false}, {"TrustedInstaller", false, "update", 3, false},
	{"UsoSvc", false, "update", 2, true}, {"msiserver", false, "install", 3, false},
	{"AppXSvc", false, "store", 3, false}, {"ClipSVC", false, "store", 3, false}, {"InstallService", false, "store", 3, false},
	{"TokenBroker", false, "start", 3, false}, {"camsvc", false, "start", 3, false}, {"TextInputManagementService", true, "lang", 2, false},
	{"W32Time", false, "time", 3, false}, {"WSearch", false, "search", 2, true},
}

// ckSvcSev: how bad it is when a service is turned off.
func ckSvcSev(area string) string {
	switch area {
	case "core", "net", "security", "audio":
		return "bad"
	case "time", "search", "look":
		return "info"
	}
	return "warn"
}

// services that are only put back when asked (the spooler is off on
// purpose on many PCs without a printer)
var ckFixOnly = []ckWant{{"Spooler", false, "print", 2, false}}

func ckWanted(name string) *ckWant {
	for _, list := range [][]ckWant{ckServices, ckFixOnly} {
		for i := range list {
			if strings.EqualFold(list[i].Name, name) {
				return &list[i]
			}
		}
	}
	return nil
}

// ckServiceCauses: the changes from the app that set this service.
func ckServiceCauses(name string, changes []ckRef, svcOf map[string][]string) []ckRef {
	var out []ckRef
	for _, c := range changes {
		for _, s := range svcOf[c.Kind+":"+c.ID] {
			if strings.EqualFold(s, name) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// the addresses Windows Update and the Store need
var ckUpdateDomains = []string{"windowsupdate.com", "update.microsoft.com", "windowsupdate.microsoft.com", "delivery.mp.microsoft.com",
	"download.microsoft.com", "storeedgefd.dsx.mp.microsoft.com", "displaycatalog.mp.microsoft.com", "login.live.com"}

// hostsBlockedUpdateDomains: the update and Store addresses a hosts file
// sends to nowhere (0.0.0.0, 127.0.0.1, ::).
func hostsBlockedUpdateDomains(hosts string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(hosts, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "0.0.0.0", "127.0.0.1", "::", "::1", "0.0.0.1":
		default:
			continue
		}
		for _, h := range f[1:] {
			h = strings.ToLower(strings.TrimSuffix(h, "."))
			for _, d := range ckUpdateDomains {
				if (h == d || strings.HasSuffix(h, "."+d)) && !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
			}
		}
	}
	return out
}

// pagingOff: the PagingFiles value says there is no page file.
func pagingOff(v []string) bool {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return false
		}
	}
	return true
}

type ckEvXML struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     int `xml:"EventID"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	Data []string `xml:"EventData>Data"`
}

// ckParseEvents reads wevtutil's XML (events one after the other).
func ckParseEvents(out string) []ckEvent {
	dec := xml.NewDecoder(strings.NewReader("<E>" + out + "</E>"))
	dec.Strict = false
	var evs []ckEvent
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Event" {
			continue
		}
		var x ckEvXML
		if dec.DecodeElement(&x, &se) != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, x.System.TimeCreated.SystemTime)
		if err != nil {
			continue
		}
		e := ckEvent{At: at.Unix()}
		prov := strings.ToLower(x.System.Provider.Name)
		first := ""
		if len(x.Data) > 0 {
			first = strings.TrimSpace(x.Data[0])
		}
		switch {
		case x.System.EventID == 41 && strings.Contains(prov, "kernel-power"):
			e.Kind = "power"
		case x.System.EventID == 1001 && strings.Contains(prov, "wer-systemerrorreporting"):
			e.Kind = "bsod"
		case x.System.EventID == 4101 && prov == "display":
			e.Kind = "gpu"
		case (x.System.EventID == 7031 || x.System.EventID == 7034 || x.System.EventID == 7000 || x.System.EventID == 7023 ||
			x.System.EventID == 7024) && strings.Contains(prov, "service control manager"):
			e.Kind, e.Subject = "svc", first
		case x.System.EventID == 1000 && prov == "application error":
			e.Kind, e.Subject = "crash", strings.ToLower(first)
		case x.System.EventID == 1002 && prov == "application hang":
			e.Kind, e.Subject = "hang", strings.ToLower(first)
		default:
			continue
		}
		if e.Subject == "xki4ocleaner.exe" {
			continue
		}
		evs = append(evs, e)
	}
	return evs
}

// svcNameOfKey: the service a registry key of the form
// ...\CurrentControlSet\Services\<name> belongs to.
func svcNameOfKey(key string) string {
	k := strings.ToLower(key)
	i := strings.Index(k, `\services\`)
	if i < 0 || !strings.Contains(k, "currentcontrolset") {
		return ""
	}
	rest := key[i+len(`\services\`):]
	if j := strings.IndexByte(rest, '\\'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// hostsWithoutBlocks: the hosts file without the lines that send update and
// Store addresses nowhere; a line that also names other hosts keeps them.
// The second result says whether anything changed.
func hostsWithoutBlocks(hosts string) (string, bool) {
	lines := strings.Split(hosts, "\n")
	changed := false
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		body, comment := l, ""
		if i := strings.IndexByte(l, '#'); i >= 0 {
			body, comment = l[:i], l[i:]
		}
		f := strings.Fields(body)
		if len(f) < 2 || len(hostsBlockedUpdateDomains(l)) == 0 {
			out = append(out, l)
			continue
		}
		keep := []string{f[0]}
		for _, h := range f[1:] {
			if len(hostsBlockedUpdateDomains(f[0]+" "+h)) == 0 {
				keep = append(keep, h)
			}
		}
		changed = true
		if len(keep) > 1 {
			nl := strings.Join(keep, " ")
			if comment != "" {
				nl += " " + comment
			} else if strings.HasSuffix(l, "\r") {
				nl += "\r" // the file's line ends stay as they were
			}
			out = append(out, nl)
		}
	}
	return strings.Join(out, "\n"), changed
}

// the update policies the Check page may take away (only these)
var ckUpdatePolicies = map[string]string{
	"NoAutoUpdate":                                 `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU`,
	"DisableWindowsUpdateAccess":                   `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`,
	"DoNotConnectToWindowsUpdateInternetLocations": `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`,
}

// bootFinding: does Windows start slower since the changes? The median
// start (until the desktop is ready) after the first change of the last 60
// days against the one before it; at least 2 starts on each side. Starts
// are boot times in ms with their time in unix ms.
func bootFinding(changes []ckRef, boots [][2]int64, now int64) *ckFinding {
	var t0 int64
	for _, c := range changes {
		if c.At > 0 && now-c.At <= 60*86400 && (t0 == 0 || c.At < t0) {
			t0 = c.At
		}
	}
	if t0 == 0 {
		return nil
	}
	var before, after []int64
	var first int64
	for _, b := range boots {
		at := b[0] / 1000
		switch {
		case at >= t0:
			after = append(after, b[1])
			if first == 0 || at < first {
				first = at
			}
		case at >= t0-60*86400:
			before = append(before, b[1])
		}
	}
	if len(before) < 2 || len(after) < 2 {
		return nil
	}
	med := func(xs []int64) int64 {
		s := append([]int64(nil), xs...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[len(s)/2]
	}
	b, a := med(before), med(after)
	secs := func(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) }
	switch {
	case a > b*13/10 && a-b >= 5000:
		return &ckFinding{ID: "boot-slower", Sev: "warn", Args: []string{secs(b), secs(a)}, Count: len(after), First: first, Suspects: ckSuspects("boot", first, changes), Fix: "page", FixArg: "startup"}
	case b > a*13/10 && b-a >= 3000:
		return &ckFinding{ID: "ok-boot-faster", Sev: "ok", Args: []string{secs(b), secs(a)}}
	}
	return &ckFinding{ID: "ok-boot", Sev: "ok", Args: []string{secs(a)}}
}

// ctfTaskOff: schtasks' CSV line says the task is turned off ("Disabled";
// other languages of Windows write their own word, so the English one and
// the usual ones count).
func ctfTaskOff(csv string) bool {
	l := strings.ToLower(csv)
	for _, w := range []string{"disabled", "деактивирано", "забранено", "deaktiviert", "désactivé", "deshabilitado", "disabilitato", "wyłączone", "отключено"} {
		if strings.Contains(l, `"`+w+`"`) {
			return true
		}
	}
	return false
}
