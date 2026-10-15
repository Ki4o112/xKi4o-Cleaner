package main

// Uninstaller (like Revo Uninstaller): the installed programs, then — after
// a program's own uninstaller has run — what it left behind: folders, files,
// shortcuts and registry keys that clearly belong to it. Everything found is
// only proposed; the user ticks what goes. Nothing of Windows, of the user's
// own folders or of another installed program is ever proposed.
//
// This file holds the parts that do not depend on Windows (and are tested
// everywhere); uninstall_windows.go reads the registry and runs uninstallers.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type instApp struct {
	ID        string   `json:"id"`   // "reg:<root>:<key>" or "appx:<package full name>"
	Kind      string   `json:"kind"` // win32, store
	Name      string   `json:"name"`
	Publisher string   `json:"publisher,omitempty"`
	Version   string   `json:"version,omitempty"`
	Size      int64    `json:"size"`                // bytes, 0 = not known (yet)
	SizeFrom  string   `json:"sizeFrom,omitempty"`  // reg (what the setup wrote), disk (measured)
	Installed int64    `json:"installed,omitempty"` // unix seconds
	LastUsed  int64    `json:"lastUsed,omitempty"`  // unix seconds, 0 = not seen
	Location  string   `json:"location,omitempty"`
	Icon      string   `json:"icon,omitempty"` // a file to take the icon from
	Command   string   `json:"command,omitempty"`
	Quiet     string   `json:"-"`
	MSI       string   `json:"msi,omitempty"` // product code of a Windows Installer package
	Scope     string   `json:"scope"`         // machine, user
	Arch      string   `json:"arch,omitempty"`
	Key       string   `json:"key,omitempty"` // its uninstall entry in the registry
	Family    string   `json:"family,omitempty"`
	NoRemove  bool     `json:"noRemove,omitempty"`
	Broken    bool     `json:"broken,omitempty"` // the uninstaller is missing: only a forced uninstall can remove it
	Exes      []string `json:"-"`
}

// ---------------------------------------------------------------- names

var (
	reParen      = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	reVersion    = regexp.MustCompile(`(?i)^v?\d+([.\-_]\d+)*[a-z]?$`)
	reGUIDBraced = regexp.MustCompile(`\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}`)
	reArchWord   = regexp.MustCompile(`(?i)^(x64|x86|x86_64|amd64|arm64|64-bit|32-bit|64bit|32bit|win64|win32|en-us|en-gb|bg-bg)$`)
)

// Words that say nothing about which program it is.
var nameStop = map[string]bool{
	"the": true, "for": true, "and": true, "of": true, "app": true, "apps": true, "application": true, "client": true, "desktop": true,
	"launcher": true, "player": true, "manager": true, "tool": true, "tools": true, "suite": true, "update": true, "updater": true,
	"helper": true, "service": true, "services": true, "edition": true, "pro": true, "free": true, "plus": true, "software": true,
	"setup": true, "version": true, "runtime": true, "driver": true, "drivers": true, "windows": true, "microsoft": true, "system": true,
	"game": true, "games": true, "center": true, "centre": true, "online": true, "browser": true, "viewer": true, "editor": true,
	"home": true, "premium": true, "studio": true, "community": true, "professional": true, "standard": true, "ultimate": true,
	"redistributable": true, "package": true, "components": true, "component": true, "support": true, "bit": true, "beta": true,
	"preview": true, "insider": true, "stable": true, "portable": true, "library": true, "data": true, "files": true, "program": true,
	"programs": true, "common": true, "shared": true, "user": true, "users": true, "settings": true, "config": true, "cache": true,
	"inc": true, "ltd": true, "llc": true, "corporation": true, "corp": true, "gmbh": true, "company": true, "co": true, "sa": true,
}

// Folder and key names that are never a program's own, whatever matches.
var nameNever = map[string]bool{
	"microsoft": true, "windows": true, "commonfiles": true, "packagecache": true, "packages": true, "temp": true, "tmp": true,
	"programs": true, "classes": true, "policies": true, "wow6432node": true, "clients": true, "registeredapplications": true,
	"odbc": true, "khronos": true, "applicationdata": true, "localsettings": true, "startmenu": true, "desktop": true,
	"documents": true, "downloads": true, "pictures": true, "music": true, "videos": true, "favorites": true, "links": true,
	"savedgames": true, "onedrive": true, "crashdumps": true, "d3dscache": true, "connecteddevicesplatform": true, "comms": true,
	"virtualstore": true, "history": true, "inetcache": true, "explorer": true, "fontcache": true, "systemprofile": true,
	"default": true, "public": true, "allusers": true, "installer": true, "uninstall": true, "apppaths": true, "run": true,
	"runonce": true, "shell": true, "software": true, "system": true, "hardware": true, "sam": true, "security": true,
	"currentversion": true, "users": true, "programfiles": true, "programfilesx86": true, "programdata": true, "appdata": true,
	"local": true, "locallow": true, "roaming": true, "common": true, "shared": true, "updates": true, "logs": true, "cache": true,
}

// Companies with many programs: their own folder or key is never proposed
// (only the program's folder inside it).
var bigVendor = map[string]bool{
	"microsoft": true, "google": true, "apple": true, "mozilla": true, "adobe": true, "oracle": true, "intel": true, "nvidia": true,
	"nvidiacorporation": true, "amd": true, "advancedmicrodevices": true, "realtek": true, "valve": true, "electronicarts": true,
	"ubisoft": true, "epicgames": true, "logitech": true, "razer": true, "corsair": true, "asus": true, "asustek": true, "msi": true,
	"lenovo": true, "hp": true, "dell": true, "jetbrains": true, "python": true, "pythonsoftwarefoundation": true, "java": true,
	"autodesk": true, "blizzard": true, "blizzardentertainment": true, "riotgames": true, "steelseries": true, "obsproject": false,
}

// normName keeps letters and digits only, in lower case: "OBS-Studio" and
// "OBS Studio" are the same name.
func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanAppName drops versions, architectures and notes in brackets:
// "7-Zip 23.01 (x64)" -> "7-Zip", "Mozilla Firefox (x64 en-US)" -> "Mozilla Firefox".
func cleanAppName(name string) string {
	s := reParen.ReplaceAllString(name, " ")
	var keep []string
	for _, w := range strings.Fields(s) {
		t := strings.Trim(w, ",;:-–—")
		if t == "" || reVersion.MatchString(t) || reArchWord.MatchString(t) {
			continue
		}
		keep = append(keep, w)
	}
	return strings.Trim(strings.Join(keep, " "), " -–—,")
}

var companyWords = map[string]bool{"inc": true, "ltd": true, "llc": true, "corporation": true, "corp": true, "gmbh": true, "company": true,
	"co": true, "sa": true, "srl": true, "bv": true, "ag": true, "oy": true, "ab": true, "limited": true, "incorporated": true, "sro": true}

// cleanPublisher drops company suffixes: "Discord Inc." -> "Discord".
func cleanPublisher(p string) string {
	p = reParen.ReplaceAllString(p, " ")
	var keep []string
	for _, w := range strings.Fields(strings.ReplaceAll(p, ",", " ")) {
		if n := normName(w); n != "" && !companyWords[n] {
			keep = append(keep, w)
		}
	}
	return strings.Join(keep, " ")
}

// appIdent is what a program is recognised by in its leftovers. It is read
// before the uninstaller runs (the files are gone afterwards).
type appIdent struct {
	Name      string   `json:"name"`
	Publisher string   `json:"publisher"`
	Location  string   `json:"location"`
	Exes      []string `json:"exes"` // full paths of its programs
	Key       string   `json:"key"`  // its uninstall entry
	Family    string   `json:"family"`
}

type identNames struct {
	full   string   // the whole clean name
	short  string   // the name without the publisher in front ("Acme Photo" by Acme -> "photo")
	strong []string // words that name it on their own ("Firefox" of "Mozilla Firefox")
	tokens []string // distinctive words of it (only inside the publisher's folder or key)
	pub    string   // the publisher
	pubs   []string // the publisher and its first word ("Acme Software" -> acmesoftware, acme)
	exes   []string // file names of its programs
}

// winBase is the file name of a Windows or a POSIX path.
func winBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (id appIdent) names() identNames {
	var n identNames
	clean := cleanAppName(id.Name)
	n.full = normName(clean)
	pub := cleanPublisher(id.Publisher)
	n.pub = normName(pub)
	if n.pub != "" {
		n.pubs = append(n.pubs, n.pub)
		if f := strings.Fields(pub); len(f) > 1 {
			if w := normName(f[0]); len([]rune(w)) >= 4 && !nameStop[w] && w != n.pub {
				n.pubs = append(n.pubs, w)
			}
		}
	}
	for _, p := range n.pubs {
		if strings.HasPrefix(n.full, p) && len([]rune(n.full))-len([]rune(p)) >= 3 {
			n.short = strings.TrimPrefix(n.full, p)
			break
		}
	}
	words := strings.FieldsFunc(clean, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range words {
		t := normName(w)
		if len([]rune(t)) >= 5 && !nameStop[t] && !nameNever[t] && t != n.full && !n.isPub(t) {
			n.tokens = append(n.tokens, t)
			// "Mozilla Firefox" by Mozilla: "Firefox" names it on its own
			if n.short == t {
				n.strong = append(n.strong, t)
			}
		}
	}
	for _, e := range id.Exes {
		b := strings.ToLower(winBase(e))
		if !genericExe(b) {
			n.exes = append(n.exes, b)
		}
	}
	return n
}

func (n identNames) isPub(x string) bool {
	for _, p := range n.pubs {
		if x == p {
			return true
		}
	}
	return false
}

var genericExes = map[string]bool{"update.exe": true, "setup.exe": true, "launcher.exe": true, "helper.exe": true, "installer.exe": true,
	"updater.exe": true, "crashpad_handler.exe": true, "elevate.exe": true, "maintenancetool.exe": true, "notification_helper.exe": true,
	"squirrel.exe": true, "createdump.exe": true, "crashreporter.exe": true, "bugreport.exe": true, "uninstall.exe": true, "uninst.exe": true,
	"install.exe": true, "start.exe": true, "run.exe": true, "app.exe": true, "main.exe": true, "service.exe": true, "agent.exe": true}

// genericExe: a file name many programs use (it says nothing about which).
func genericExe(name string) bool {
	n := strings.ToLower(name)
	return genericExes[n] || strings.HasPrefix(n, "unins") || strings.HasPrefix(n, "uninst") || strings.HasPrefix(n, "vc_redist")
}

// match tells how surely a folder or key name belongs to the program:
// 1 = it is the program's name, 2 = a distinctive word of it, 3 = contains
// it (only in the advanced scan), 0 = no.
func (n identNames) match(name string) int { return n.matchIn(name, false) }

// matchIn is match inside the publisher's folder or key, where the name
// without the publisher ("Photo Studio" in "Acme") is the program's too.
func (n identNames) matchIn(name string, inPub bool) int {
	x := normName(strings.TrimSuffix(name, winExt(name)))
	if x == "" || nameNever[x] {
		return 0
	}
	if len([]rune(n.full)) >= 3 && x == n.full {
		return 1
	}
	for _, p := range n.pubs {
		if x == p+n.full {
			return 1
		}
	}
	if inPub && n.short != "" && x == n.short {
		return 1
	}
	for _, t := range n.strong {
		if x == t {
			return 2
		}
	}
	if inPub {
		for _, t := range n.tokens {
			if x == t {
				return 2
			}
		}
	}
	if len([]rune(n.full)) >= 6 && strings.Contains(x, n.full) {
		return 3
	}
	return 0
}

// winExt is the extension of a file name (".lnk"), "" for folders like "Photo Studio 2.1".
func winExt(name string) string {
	e := filepath.Ext(name)
	if len(e) > 6 || strings.ContainsAny(e, " ") {
		return ""
	}
	if _, err := strconv.Atoi(strings.TrimPrefix(e, ".")); err == nil {
		return ""
	}
	return e
}

// ---------------------------------------------------------------- leftovers

type leftover struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // dir, file, lnk, key, value
	Path  string `json:"path"` // a file path, or a registry path such as HKCU\Software\X (HKLM32 = 32-bit view)
	Value string `json:"value,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Files int    `json:"files,omitempty"`
	Sure  bool   `json:"sure"`  // certainly the program's: ticked from the start
	Why   string `json:"why"`   // reason (a text key)
	Level int    `json:"level"` // 1 safe, 2 moderate, 3 advanced
}

type lfEnt struct {
	Name string
	Dir  bool
	Link bool
}

// lfFS is the file system as the leftover scan sees it.
type lfFS interface {
	ReadDir(dir string) []lfEnt
	Exists(path string) bool
	Size(path string) (int64, int)
	LnkTarget(path string) string // where a shortcut points ("" = unknown)
}

// lfReg is the registry as the leftover scan sees it. Paths start with HKCU,
// HKLM (64-bit view) or HKLM32 (32-bit view).
type lfReg interface {
	Subkeys(path string) []string
	Exists(path string) bool
	Values(path string) []string
	String(path, value string) string
}

type lfRoot struct {
	Path  string
	Depth int    // 1: its children; 2: also their children (publisher\program)
	Kind  string // programs, data (ProgramData), userdata (AppData), startmenu, desktop
}

// lfScope is everything the scan must respect.
type lfScope struct {
	Roots      []lfRoot
	Protected  []string // never proposed, nor anything above them: Windows, user folders, other programs' folders
	UserData   []string // places where a program keeps the user's data (AppData, Documents): found there, never pre-ticked
	OtherNames []string // normalized names of the other installed programs
	RegRoots   []string // HKCU\Software, HKLM\SOFTWARE, HKLM32\SOFTWARE
	Level      int      // 1 safe, 2 moderate, 3 advanced
	Uninstall  string   // the program's own uninstall key, if it is still there
}

func lowerClean(p string) string { return strings.ToLower(filepath.Clean(p)) }

func under(p, root string) bool {
	p, root = lowerClean(p), lowerClean(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, `\`)+`\`) || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// protectedFor: the path is a protected place, or holds one (deleting it
// would delete the protected place too).
func protectedFor(p string, protected []string) bool {
	for _, x := range protected {
		if x != "" && (under(x, p) || lowerClean(x) == lowerClean(p)) {
			return true
		}
	}
	return false
}

// scanLeftovers finds what the program left. The program's uninstaller has
// already run; ident was read before.
func scanLeftovers(id appIdent, sc lfScope, fs lfFS, reg lfReg) []leftover {
	n := id.names()
	others := map[string]bool{}
	for _, o := range sc.OtherNames {
		if o != "" && o != n.full {
			others[o] = true
		}
	}
	var out []leftover
	seen := map[string]bool{}
	add := func(l leftover) {
		if l.Level > sc.Level {
			return
		}
		k := strings.ToLower(l.Kind + "|" + l.Path + "|" + l.Value)
		if seen[k] {
			return
		}
		seen[k] = true
		l.ID = strconv.Itoa(len(out) + 1)
		out = append(out, l)
	}
	okPath := func(p string) bool {
		if protectedFor(p, sc.Protected) {
			return false
		}
		for _, r := range sc.Roots {
			if lowerClean(r.Path) == lowerClean(p) {
				return false
			}
		}
		return true
	}
	addDir := func(p string, sure bool, why string, level int) {
		if !okPath(p) || !fs.Exists(p) {
			return
		}
		size, files := fs.Size(p)
		add(leftover{Kind: "dir", Path: p, Size: size, Files: files, Sure: sure, Why: why, Level: level})
	}

	inUserData := func(p string) bool {
		for _, u := range sc.UserData {
			if under(p, u) {
				return true
			}
		}
		return false
	}

	matchedIn := func(name string, inPub bool) int {
		m := n.matchIn(name, inPub)
		if m == 0 || others[normName(strings.TrimSuffix(name, winExt(name)))] {
			return 0
		}
		return m
	}
	matched := func(name string) int { return matchedIn(name, false) }

	// the program's folder, if the uninstaller left it: ticked only when its
	// name is the program's (a wrong "install location" such as a whole games
	// folder is shown, never pre-ticked)
	if id.Location != "" && filepath.IsAbs(id.Location) && len(strings.FieldsFunc(filepath.Clean(id.Location), func(r rune) bool { return r == '\\' || r == '/' })) >= 2 {
		inPub := n.isPub(normName(filepath.Base(filepath.Dir(id.Location))))
		m := matchedIn(filepath.Base(id.Location), inPub)
		sure := m == 1 || (m == 2 && inPub)
		addDir(id.Location, sure && !inUserData(id.Location), "lf.why.location", 1)
	}
	// a Store app's own data folder
	if id.Family != "" {
		for _, r := range sc.Roots {
			if r.Kind == "packages" {
				addDir(filepath.Join(r.Path, id.Family), false, "lf.why.storeData", 1)
			}
		}
	}

	for _, r := range sc.Roots {
		if r.Kind == "packages" {
			continue
		}
		userData := r.Kind == "userdata"
		for _, e := range fs.ReadDir(r.Path) {
			if e.Link {
				continue
			}
			p := filepath.Join(r.Path, e.Name)
			switch {
			case r.Kind == "desktop":
				if !e.Dir && strings.EqualFold(filepath.Ext(e.Name), ".lnk") {
					if t := fs.LnkTarget(p); t != "" && ((id.Location != "" && under(t, id.Location)) || exeNamed(t, n.exes)) && !fs.Exists(t) {
						add(leftover{Kind: "lnk", Path: p, Sure: true, Why: "lf.why.shortcut", Level: 1})
					}
				}
				continue
			case !e.Dir:
				if r.Kind == "startmenu" && strings.EqualFold(filepath.Ext(e.Name), ".lnk") {
					if m := matched(e.Name); m == 1 {
						add(leftover{Kind: "lnk", Path: p, Sure: true, Why: "lf.why.shortcut", Level: 1})
					} else if t := fs.LnkTarget(p); t != "" && id.Location != "" && under(t, id.Location) && !fs.Exists(t) {
						add(leftover{Kind: "lnk", Path: p, Sure: true, Why: "lf.why.shortcut", Level: 1})
					}
				}
				continue
			}
			why := func(w string) string {
				if userData {
					return "lf.why.userData"
				}
				return w
			}
			switch matched(e.Name) {
			case 1:
				addDir(p, !userData, why("lf.why.name"), 1)
				continue
			case 2:
				addDir(p, false, why("lf.why.word"), 2)
				continue
			case 3:
				addDir(p, false, why("lf.why.contains"), 3)
				continue
			}
			// a publisher's folder: the program's folder inside it
			isPub := len([]rune(normName(e.Name))) >= 3 && n.isPub(normName(e.Name))
			if !isPub && (r.Depth < 2 || sc.Level < 3) {
				continue
			}
			found, total := 0, 0
			for _, k := range fs.ReadDir(p) {
				total++
				if k.Link {
					continue
				}
				kp := filepath.Join(p, k.Name)
				km := matchedIn(k.Name, isPub)
				if !k.Dir {
					if r.Kind == "startmenu" && km == 1 && strings.EqualFold(filepath.Ext(k.Name), ".lnk") {
						add(leftover{Kind: "lnk", Path: kp, Sure: true, Why: "lf.why.shortcut", Level: 1})
						found++
					}
					continue
				}
				switch {
				case km == 1 && isPub:
					addDir(kp, !userData, why("lf.why.pubName"), 1)
					found++
				case km == 2 && isPub:
					addDir(kp, false, why("lf.why.word"), 2)
					found++
				case km == 1:
					addDir(kp, false, why("lf.why.deep"), 3)
				}
			}
			if isPub && !bigVendor[normName(e.Name)] && found > 0 && found == total {
				addDir(p, false, why("lf.why.pubEmpty"), 2)
			}
		}
	}

	// registry
	for _, root := range sc.RegRoots {
		for _, k := range reg.Subkeys(root) {
			kp := root + `\` + k
			switch matched(k) {
			case 1:
				add(leftover{Kind: "key", Path: kp, Sure: true, Why: "lf.why.name", Level: 1})
				continue
			case 2:
				add(leftover{Kind: "key", Path: kp, Why: "lf.why.word", Level: 2})
				continue
			}
			if !n.isPub(normName(k)) {
				continue
			}
			subs := reg.Subkeys(kp)
			found := 0
			for _, s := range subs {
				if sm := matchedIn(s, true); sm == 1 {
					add(leftover{Kind: "key", Path: kp + `\` + s, Sure: true, Why: "lf.why.pubName", Level: 1})
					found++
				} else if sm == 2 {
					add(leftover{Kind: "key", Path: kp + `\` + s, Why: "lf.why.word", Level: 2})
					found++
				}
			}
			if !bigVendor[normName(k)] && found > 0 && found == len(subs) && len(reg.Values(kp)) == 0 {
				add(leftover{Kind: "key", Path: kp, Why: "lf.why.pubEmpty", Level: 2})
			}
		}
	}
	// "App Paths" and "Applications" entries of its programs
	for _, exe := range n.exes {
		for _, base := range []string{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths`, `HKLM32\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths`, `HKCU\Software\Microsoft\Windows\CurrentVersion\App Paths`} {
			kp := base + `\` + exe
			if !reg.Exists(kp) {
				continue
			}
			t := cleanRegPath(reg.String(kp, ""))
			if t == "" || (id.Location != "" && under(t, id.Location)) || !fs.Exists(t) {
				add(leftover{Kind: "key", Path: kp, Sure: true, Why: "lf.why.appPath", Level: 1})
			}
		}
		for _, base := range []string{`HKCU\Software\Classes\Applications`, `HKLM\SOFTWARE\Classes\Applications`} {
			if kp := base + `\` + exe; reg.Exists(kp) {
				add(leftover{Kind: "key", Path: kp, Why: "lf.why.assoc", Level: 2})
			}
		}
	}
	// names Windows remembered for its programs
	if id.Location != "" {
		mui := `HKCU\Software\Classes\Local Settings\Software\Microsoft\Windows\Shell\MuiCache`
		for _, v := range reg.Values(mui) {
			if under(strings.SplitN(v, ".FriendlyAppName", 2)[0], id.Location) || under(strings.SplitN(v, ".ApplicationCompany", 2)[0], id.Location) {
				add(leftover{Kind: "value", Path: mui, Value: v, Why: "lf.why.mui", Level: 3})
			}
		}
	}
	// its uninstall entry, if it is still there
	if sc.Uninstall != "" && reg.Exists(sc.Uninstall) {
		add(leftover{Kind: "key", Path: sc.Uninstall, Sure: true, Why: "lf.why.entry", Level: 1})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sure != out[j].Sure {
			return out[i].Sure
		}
		return out[i].Level < out[j].Level
	})
	for i := range out {
		out[i].ID = strconv.Itoa(i + 1)
	}
	return out
}

func exeNamed(p string, names []string) bool {
	b := strings.ToLower(winBase(p))
	for _, n := range names {
		if b == n {
			return true
		}
	}
	return false
}

// cleanRegPath: `"C:\x\app.exe"` or C:\x\app.exe,0 -> C:\x\app.exe.
func cleanRegPath(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if i := strings.Index(v[1:], `"`); i >= 0 {
			v = v[1 : i+1]
		}
	}
	if i := strings.LastIndex(v, ","); i > 0 && !strings.ContainsAny(v[i:], `\/`) {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// ---------------------------------------------------------------- last used

// prefetchName: "DISCORD.EXE-1A2B3C4D.pf" -> "discord.exe".
func prefetchName(file string) string {
	f := strings.ToLower(file)
	if !strings.HasSuffix(f, ".pf") {
		return ""
	}
	f = strings.TrimSuffix(f, ".pf")
	i := strings.LastIndex(f, "-")
	if i <= 0 {
		return ""
	}
	return f[:i]
}

// rot13 is how Windows hides the names in UserAssist.
func rot13(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			return 'A' + (r-'A'+13)%26
		}
		return r
	}, s)
}

// userAssistTime reads the last run time of a UserAssist value (72 bytes
// since Windows 7: the FILETIME is at offset 60).
func userAssistTime(b []byte) int64 {
	if len(b) < 68 {
		return 0
	}
	ft := int64(binary.LittleEndian.Uint64(b[60:68]))
	if ft <= 0 {
		return 0
	}
	return ft/10000000 - 11644473600
}

// usage is when programs were last run: by file name (Prefetch) and by
// full path (UserAssist).
type usage struct {
	byName map[string]int64
	byPath map[string]int64
}

func (u usage) lastUsed(exes []string) int64 {
	var last int64
	for _, e := range exes {
		if t := u.byPath[strings.ToLower(filepath.Clean(e))]; t > last {
			last = t
		}
		if b := strings.ToLower(winBase(e)); !genericExe(b) {
			if t := u.byName[b]; t > last {
				last = t
			}
		}
	}
	return last
}

// programExes lists the programs (.exe) in a folder and one level below, at
// most limit of them.
func programExes(dir string, limit int) []string {
	var out []string
	if dir == "" {
		return out
	}
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			if len(out) >= limit {
				return
			}
			p := filepath.Join(d, e.Name())
			if e.IsDir() {
				if depth > 0 && e.Type()&os.ModeSymlink == 0 {
					walk(p, depth-1)
				}
			} else if strings.EqualFold(filepath.Ext(e.Name()), ".exe") {
				out = append(out, p)
			}
		}
	}
	walk(dir, 1)
	return out
}

// msiUninstallArgs turns a Windows Installer command into an uninstall:
// "MsiExec.exe /I{GUID}" (modify) -> "/X{GUID}".
func msiUninstallArgs(code string, quiet bool) []string {
	a := []string{"/X" + code}
	if quiet {
		a = append(a, "/qb")
	}
	return a
}

// installDate reads "20241031" (what setups write) as unix seconds.
func installDate(s string) int64 {
	s = strings.TrimSpace(s)
	if len(s) != 8 {
		return 0
	}
	t, err := time.ParseInLocation("20060102", s, time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// osLeftFS is the real file system for the leftover scan.
type osLeftFS struct{}

func (osLeftFS) ReadDir(dir string) []lfEnt {
	var out []lfEnt
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, lfEnt{Name: e.Name(), Dir: fi.IsDir(), Link: isLink(fi)})
	}
	return out
}

func (osLeftFS) Exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// Size counts a folder (links not followed), at most 200 000 files.
func (osLeftFS) Size(p string) (int64, int) {
	var size int64
	files := 0
	var walk func(d string)
	walk = func(d string) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			if files > 200000 {
				return
			}
			fi, err := e.Info()
			if err != nil || isLink(fi) {
				continue
			}
			if fi.IsDir() {
				walk(filepath.Join(d, e.Name()))
			} else {
				size += fi.Size()
				files++
			}
		}
	}
	if fi, err := os.Lstat(p); err == nil && !fi.IsDir() {
		return fi.Size(), 1
	}
	walk(p)
	return size, files
}

func (osLeftFS) LnkTarget(p string) string {
	li, err := readLnkFile(p)
	if err != nil || li.Special || li.Network {
		return ""
	}
	return li.Target
}

// regCanon writes a key path the way the registry has it: the 32-bit view
// (HKLM32\SOFTWARE\X) is HKLM\SOFTWARE\WOW6432Node\X.
func regCanon(p string) string {
	root, rest, _ := strings.Cut(strings.Trim(p, `\`), `\`)
	switch strings.ToUpper(root) {
	case "HKCU", "HKEY_CURRENT_USER":
		return `HKCU\` + rest
	case "HKLM", "HKEY_LOCAL_MACHINE":
		return `HKLM\` + rest
	case "HKLM32":
		if strings.EqualFold(rest, "SOFTWARE") {
			return `HKLM\SOFTWARE\WOW6432Node`
		}
		if len(rest) > 9 && strings.EqualFold(rest[:9], `SOFTWARE\`) {
			return `HKLM\SOFTWARE\WOW6432Node\` + rest[9:]
		}
		return `HKLM\` + rest
	}
	return ""
}

// keys that hold everyone's settings: never deleted, nor anything above them
var regStructural = func() []string {
	var out []string
	for _, base := range []string{`HKCU\Software`, `HKLM\SOFTWARE`, `HKLM\SOFTWARE\WOW6432Node`} {
		out = append(out, base+`\Classes`, base+`\Policies`, base+`\Microsoft\Windows NT\CurrentVersion`,
			base+`\Classes\Applications`, base+`\Classes\Local Settings\Software\Microsoft\Windows\Shell\MuiCache`)
		for _, k := range []string{"Run", "RunOnce", "RunOnceEx", "App Paths", "Uninstall", "Explorer", "Installer", "Policies", "Shell Extensions"} {
			out = append(out, base+`\Microsoft\Windows\CurrentVersion\`+k)
		}
	}
	return out
}()

// regKeyDeletable: a program's own key, under HKCU\Software or HKLM\SOFTWARE,
// at least one level below them, and not one of the keys Windows itself and
// every program use.
func regKeyDeletable(p string) bool {
	c := strings.ToLower(regCanon(p))
	if c == "" {
		return false
	}
	var rest string
	switch {
	case strings.HasPrefix(c, `hkcu\software\`):
		rest = c[len(`hkcu\software\`):]
	case strings.HasPrefix(c, `hklm\software\wow6432node\`):
		rest = c[len(`hklm\software\wow6432node\`):]
	case strings.HasPrefix(c, `hklm\software\`):
		rest = c[len(`hklm\software\`):]
	default:
		return false
	}
	if rest == "" || rest == "wow6432node" {
		return false
	}
	for _, s := range regStructural {
		if s = strings.ToLower(s); s == c || strings.HasPrefix(s, c+`\`) {
			return false
		}
	}
	return true
}
