package main

// Browser extensions: a read-only list of the extensions installed in
// Chrome, Edge, Brave, Vivaldi, Opera and Firefox, with what each one is
// allowed to do. Extensions that can read every site AND change what you
// see or send (cookies, web requests, other extensions, a proxy, programs on
// the PC) are marked, as are ones that did not come from a store. Nothing is
// removed or turned off here: a button opens the browser's own extensions
// page for that one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type browserExt struct {
	Browser  string   `json:"browser"` // chrome, edge, brave, vivaldi, opera, operagx, firefox
	Profile  string   `json:"profile"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Desc     string   `json:"desc,omitempty"`
	Enabled  bool     `json:"enabled"`
	Store    bool     `json:"store"`    // from the browser's store
	AllSites bool     `json:"allSites"` // can read and change every site
	Perms    []string `json:"perms"`    // the sensitive ones, as keys for the page
	Risk     string   `json:"risk"`     // high, medium, low
	Path     string   `json:"path,omitempty"`
}

// extSensitive: permissions worth showing, as the page's text keys.
var extSensitive = map[string]string{
	"cookies": "cookies", "webrequest": "webRequest", "webrequestblocking": "webRequest",
	"declarativenetrequest": "netRules", "declarativenetrequestwithhostaccess": "netRules",
	"proxy": "proxy", "debugger": "debugger", "nativemessaging": "native", "management": "management",
	"history": "history", "tabs": "tabs", "downloads": "downloads", "clipboardread": "clipboard",
	"privacy": "privacy", "scripting": "scripting", "webnavigation": "navigation", "browsingdata": "browsingData",
	"desktopcapture": "capture", "tabcapture": "capture", "geolocation": "location", "bookmarks": "bookmarks",
}

// extStrong are the permissions that, together with every site, make an
// extension able to watch or change everything you do online.
var extStrong = map[string]bool{"cookies": true, "webRequest": true, "proxy": true, "debugger": true,
	"native": true, "management": true, "scripting": true, "netRules": true}

func isAllSites(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "<all_urls>", "*://*/*", "http://*/*", "https://*/*", "*://*/", "http://*/", "https://*/":
		return true
	}
	return strings.HasPrefix(p, "*://*/") || strings.HasPrefix(p, "https://*/") || strings.HasPrefix(p, "http://*/")
}

// extRisk sorts the permissions and rates the extension.
func extRisk(e *browserExt, perms []string) {
	seen := map[string]bool{}
	e.Perms = e.Perms[:0]
	for _, p := range perms {
		if isAllSites(p) {
			e.AllSites = true
			continue
		}
		if k, ok := extSensitive[strings.ToLower(strings.TrimSpace(p))]; ok && !seen[k] {
			seen[k] = true
			e.Perms = append(e.Perms, k)
		}
	}
	sort.Strings(e.Perms)
	strong := false
	for _, k := range e.Perms {
		if extStrong[k] {
			strong = true
		}
	}
	switch {
	case e.AllSites && strong, !e.Store && (e.AllSites || strong):
		e.Risk = "high"
	case e.AllSites || strong || len(e.Perms) >= 3:
		e.Risk = "medium"
	default:
		e.Risk = "low"
	}
}

// ---------------------------------------------------------------- Chromium

type crxManifest struct {
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	Description   string          `json:"description"`
	DefaultLocale string          `json:"default_locale"`
	Permissions   []any           `json:"permissions"`
	Optional      []any           `json:"optional_permissions"`
	Hosts         []string        `json:"host_permissions"`
	Scripts       []crxScript     `json:"content_scripts"`
	Theme         json.RawMessage `json:"theme"`
	App           json.RawMessage `json:"app"`
}

type crxScript struct {
	Matches []string `json:"matches"`
}

// crxMessage resolves "__MSG_name__" from the extension's _locales folder.
func crxMessage(dir, locale, s string) string {
	if !strings.HasPrefix(s, "__MSG_") || !strings.HasSuffix(s, "__") {
		return s
	}
	key := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(s, "__MSG_"), "__"))
	for _, loc := range []string{locale, "en", "en_US"} {
		if loc == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "_locales", loc, "messages.json"))
		if err != nil {
			continue
		}
		var m map[string]struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(stripBOM(b), &m) != nil {
			continue
		}
		for k, v := range m {
			if strings.ToLower(k) == key && v.Message != "" {
				return v.Message
			}
		}
	}
	return s
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// crxPrefs is what a profile's (Secure) Preferences say about extensions.
type crxPref struct {
	State          *int  `json:"state"`
	DisableReasons any   `json:"disable_reasons"`
	FromWebstore   *bool `json:"from_webstore"`
	Location       int   `json:"location"`
}

func readCrxPrefs(profileDir string) map[string]crxPref {
	out := map[string]crxPref{}
	for _, f := range []string{"Preferences", "Secure Preferences"} {
		b, err := os.ReadFile(filepath.Join(profileDir, f))
		if err != nil {
			continue
		}
		var p struct {
			Extensions struct {
				Settings map[string]crxPref `json:"settings"`
			} `json:"extensions"`
		}
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		for id, s := range p.Extensions.Settings {
			cur := out[id]
			if s.State != nil {
				cur.State = s.State
			}
			if s.DisableReasons != nil {
				cur.DisableReasons = s.DisableReasons
			}
			if s.FromWebstore != nil {
				cur.FromWebstore = s.FromWebstore
			}
			if s.Location != 0 {
				cur.Location = s.Location
			}
			out[id] = cur
		}
	}
	return out
}

func disabledBy(reasons any) bool {
	switch v := reasons.(type) {
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	}
	return false
}

// newestVersionDir picks the newest version folder of an extension.
func newestVersionDir(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		v := strings.SplitN(e.Name(), "_", 2)[0]
		if best == "" || extVerCmp(v, strings.SplitN(best, "_", 2)[0]) > 0 {
			best = e.Name()
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(dir, best)
}

// extVerCmp compares dotted versions (1.10 > 1.9).
func extVerCmp(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x = extAtoi(pa[i])
		}
		if i < len(pb) {
			y = extAtoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func extAtoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			break
		}
	}
	return n
}

// crxProfileNames reads the profiles' display names from "Local State".
func crxProfileNames(userData string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(userData, "Local State"))
	if err != nil {
		return out
	}
	var ls struct {
		Profile struct {
			Info map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if json.Unmarshal(b, &ls) == nil {
		for dir, i := range ls.Profile.Info {
			out[dir] = i.Name
		}
	}
	return out
}

// scanChromium lists one Chromium browser's extensions. userData is its
// "User Data" folder; flat is for Opera, whose profile is that folder itself.
func scanChromium(browser, userData string, flat bool) []browserExt {
	var profiles []string
	names := map[string]string{}
	if flat {
		profiles = []string{userData}
	} else {
		names = crxProfileNames(userData)
		ents, _ := os.ReadDir(userData)
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() && (n == "Default" || strings.HasPrefix(n, "Profile ")) {
				profiles = append(profiles, filepath.Join(userData, n))
			}
		}
	}
	var out []browserExt
	for _, pd := range profiles {
		prefs := readCrxPrefs(pd)
		pname := names[filepath.Base(pd)]
		if pname == "" && !flat {
			pname = filepath.Base(pd)
		}
		ents, _ := os.ReadDir(filepath.Join(pd, "Extensions"))
		for _, e := range ents {
			id := e.Name()
			if !e.IsDir() || len(id) != 32 {
				continue
			}
			vd := newestVersionDir(filepath.Join(pd, "Extensions", id))
			if vd == "" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(vd, "manifest.json"))
			if err != nil {
				continue
			}
			var m crxManifest
			if json.Unmarshal(stripBOM(b), &m) != nil || len(m.Theme) > 0 {
				continue // themes change only colours
			}
			x := browserExt{Browser: browser, Profile: pname, ID: id, Version: m.Version, Path: vd}
			x.Name = crxMessage(vd, m.DefaultLocale, m.Name)
			x.Desc = crxMessage(vd, m.DefaultLocale, m.Description)
			if len(x.Desc) > 160 {
				x.Desc = x.Desc[:157] + "…"
			}
			p, known := prefs[id]
			x.Enabled = !known || ((p.State == nil || *p.State == 1) && !disabledBy(p.DisableReasons))
			// location 1 = from the store; 4 = unpacked (loaded by hand)
			x.Store = known && ((p.FromWebstore != nil && *p.FromWebstore) || p.Location == 1 || p.Location == 10)
			if !known {
				x.Store = true // not listed: the browser's own built-in ones
			}
			var perms []string
			for _, list := range [][]any{m.Permissions, m.Optional} {
				for _, v := range list {
					if s, ok := v.(string); ok {
						perms = append(perms, s)
					}
				}
			}
			perms = append(perms, m.Hosts...)
			for _, s := range m.Scripts {
				perms = append(perms, s.Matches...)
			}
			extRisk(&x, perms)
			out = append(out, x)
		}
	}
	return out
}

// ---------------------------------------------------------------- Firefox

type ffAddons struct {
	Addons []struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		Version       string `json:"version"`
		Active        bool   `json:"active"`
		UserDisabled  bool   `json:"userDisabled"`
		Location      string `json:"location"`
		SignedState   int    `json:"signedState"`
		Path          string `json:"path"`
		DefaultLocale struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"defaultLocale"`
		UserPermissions *struct {
			Permissions []string `json:"permissions"`
			Origins     []string `json:"origins"`
		} `json:"userPermissions"`
	} `json:"addons"`
}

func parseFirefoxAddons(b []byte, profile string) []browserExt {
	var a ffAddons
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	var out []browserExt
	for _, ad := range a.Addons {
		// only what the user installed (not Firefox's own system add-ons)
		if ad.Type != "extension" || (ad.Location != "app-profile" && ad.Location != "app-temporary") {
			continue
		}
		x := browserExt{Browser: "firefox", Profile: profile, ID: ad.ID, Name: ad.DefaultLocale.Name, Version: ad.Version,
			Desc: ad.DefaultLocale.Description, Enabled: ad.Active && !ad.UserDisabled, Path: ad.Path}
		if x.Name == "" {
			x.Name = ad.ID
		}
		if len(x.Desc) > 160 {
			x.Desc = x.Desc[:157] + "…"
		}
		// signed by Mozilla (2 = signed, 3 = system, 4 = privileged)
		x.Store = ad.SignedState >= 2 && ad.Location == "app-profile"
		var perms []string
		if ad.UserPermissions != nil {
			perms = append(perms, ad.UserPermissions.Permissions...)
			perms = append(perms, ad.UserPermissions.Origins...)
		}
		extRisk(&x, perms)
		out = append(out, x)
	}
	return out
}

func scanFirefox(profilesDir string) []browserExt {
	ents, _ := os.ReadDir(profilesDir)
	var out []browserExt
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(profilesDir, e.Name(), "extensions.json"))
		if err != nil {
			continue
		}
		name := e.Name()
		if i := strings.Index(name, "."); i >= 0 {
			name = name[i+1:] // "abcd1234.default-release" -> "default-release"
		}
		out = append(out, parseFirefoxAddons(b, name)...)
	}
	return out
}

// sortExts: the riskiest first, then by browser and name.
func sortExts(xs []browserExt) {
	rank := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(xs, func(i, j int) bool {
		a, b := xs[i], xs[j]
		if rank[a.Risk] != rank[b.Risk] {
			return rank[a.Risk] < rank[b.Risk]
		}
		if a.Browser != b.Browser {
			return a.Browser < b.Browser
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
