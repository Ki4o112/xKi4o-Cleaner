package main

// The right-click menu of Explorer: entries that programs add. Only entries
// of other programs are listed (Windows' own stay as they are). Hiding one
// is undone with the same switch: a verb gets the LegacyDisable value that
// Explorer honors, an extension goes on Windows' list of blocked shell
// extensions. Nothing is deleted.

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// Where an entry shows, by the class it is registered under.
var ctxClasses = []struct {
	class string
	where []string
}{
	{`*`, []string{"files"}},
	{`AllFilesystemObjects`, []string{"files", "folders"}},
	{`Directory`, []string{"folders"}},
	{`Folder`, []string{"folders"}},
	{`Directory\Background`, []string{"background"}},
	{`DesktopBackground`, []string{"background"}},
	{`Drive`, []string{"drives"}},
}

// ctxLoc: one registry place of an entry (root "hkcu" or "hklm", the path
// under Software\Classes).
type ctxLoc struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

type ctxItem struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"` // "verb" or "ext"
	Name    string   `json:"name"`
	Where   []string `json:"where"`
	Program string   `json:"program"`
	Icon    string   `json:"icon,omitempty"` // a file for its picture
	Missing bool     `json:"missing"`        // the program is not there any more
	Shift   bool     `json:"shift"`          // shows only with Shift held
	Off     bool     `json:"off"`
	CLSID   string   `json:"clsid,omitempty"`
	locs    []ctxLoc
}

var reAccel = regexp.MustCompile(`&(.)`)

// menuName: the text without its keyboard accelerator ("Open with &VLC" →
// "Open with VLC"; "&&" stays one "&").
func menuName(s string) string {
	s = reAccel.ReplaceAllStringFunc(s, func(m string) string { return m[1:] })
	return strings.TrimSpace(s)
}

// menuProgram: the program a menu command runs. For rundll32 and regsvr32
// it is the DLL they load.
func menuProgram(cmd string, get func(string) string) string {
	exe, args := commandParts(expandPercent(cmd, get))
	switch strings.ToLower(winBase(exe)) {
	case "rundll32.exe", "rundll32", "regsvr32.exe", "regsvr32":
		args = strings.TrimSpace(args)
		for strings.HasPrefix(args, "/") { // regsvr32 /s /u x.dll
			_, args, _ = strings.Cut(args, " ")
			args = strings.TrimSpace(args)
		}
		if strings.HasPrefix(args, `"`) {
			if k := strings.IndexByte(args[1:], '"'); k >= 0 {
				return args[1 : 1+k]
			}
		}
		if i := strings.IndexByte(args, ','); i >= 0 {
			args = args[:i]
		}
		if i := strings.Index(strings.ToLower(args), ".dll"); i >= 0 {
			return args[:i+4]
		}
		return strings.TrimSpace(args)
	}
	return exe
}

// iconFile: the file of an Icon value (`"C:\x\app.exe",0` → C:\x\app.exe).
func iconFile(v string, get func(string) string) string {
	v = strings.TrimSpace(expandPercent(v, get))
	if strings.HasPrefix(v, `"`) {
		if k := strings.IndexByte(v[1:], '"'); k >= 0 {
			return v[1 : 1+k]
		}
	}
	if i := strings.LastIndexByte(v, ','); i > 0 && strings.Trim(strings.TrimSpace(v[i+1:]), "-0123456789") == "" {
		v = v[:i]
	}
	return strings.Trim(strings.TrimSpace(v), `"`)
}

// underDir: p is inside dir (Windows paths, any case).
func underDir(p, dir string) bool {
	p, dir = strings.ToLower(strings.ReplaceAll(p, "/", `\`)), strings.ToLower(strings.TrimRight(strings.ReplaceAll(dir, "/", `\`), `\`))
	return dir != "" && (p == dir || strings.HasPrefix(p, dir+`\`))
}

// windowsOwn: a program of Windows itself (its menu entries are not
// listed, so they cannot be hidden here by mistake). A bare name with no
// folder ("cmd.exe") is found in the Windows folder too.
func windowsOwn(prog, windir string) bool {
	if prog == "" {
		return true // nothing to show for it
	}
	if !strings.ContainsAny(prog, `\/`) {
		return true
	}
	return underDir(prog, windir)
}

// isGUID: a {xxxxxxxx-...} class id.
func isGUID(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) == 38 && s[0] == '{' && s[37] == '}' && reGUID.MatchString(s)
}

// mergeCtx joins the same entry registered for several kinds of objects
// (one switch hides it everywhere) and gives each a stable id.
func mergeCtx(items []ctxItem) []ctxItem {
	by := map[string]*ctxItem{}
	var order []string
	for _, it := range items {
		key := it.Kind + "|" + strings.ToLower(it.Program) + "|" + strings.ToLower(it.Name)
		if it.Kind == "ext" {
			key = "ext|" + strings.ToUpper(it.CLSID)
		}
		m, ok := by[key]
		if !ok {
			c := it
			c.Where = append([]string{}, it.Where...)
			c.locs = append([]ctxLoc{}, it.locs...)
			by[key] = &c
			order = append(order, key)
			continue
		}
		for _, w := range it.Where {
			if !containsStr(m.Where, w) {
				m.Where = append(m.Where, w)
			}
		}
		m.locs = append(m.locs, it.locs...)
		m.Off = m.Off && it.Off // shown somewhere = on
		m.Shift = m.Shift && it.Shift
		if m.Icon == "" {
			m.Icon = it.Icon
		}
	}
	out := make([]ctxItem, 0, len(order))
	rank := map[string]int{"files": 0, "folders": 1, "background": 2, "drives": 3}
	for _, k := range order {
		m := by[k]
		sort.Slice(m.Where, func(a, b int) bool { return rank[m.Where[a]] < rank[m.Where[b]] })
		h := sha256.Sum256([]byte(k))
		m.ID = hex.EncodeToString(h[:8])
		out = append(out, *m)
	}
	sort.SliceStable(out, func(a, b int) bool { return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name) })
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
