//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

type extBrowser struct {
	id, exe, userData, page string
	flat                    bool
}

func extBrowsers() []extBrowser {
	la, ra := P.local, os.Getenv("APPDATA")
	if la == "" {
		la = os.Getenv("LOCALAPPDATA")
	}
	return []extBrowser{
		{"chrome", "chrome.exe", filepath.Join(la, `Google\Chrome\User Data`), "chrome://extensions/?id=", false},
		{"edge", "msedge.exe", filepath.Join(la, `Microsoft\Edge\User Data`), "edge://extensions/?id=", false},
		{"brave", "brave.exe", filepath.Join(la, `BraveSoftware\Brave-Browser\User Data`), "brave://extensions/?id=", false},
		{"vivaldi", "vivaldi.exe", filepath.Join(la, `Vivaldi\User Data`), "vivaldi://extensions/?id=", false},
		{"opera", "opera.exe", filepath.Join(ra, `Opera Software\Opera Stable`), "opera://extensions/?id=", true},
		{"operagx", "opera.exe", filepath.Join(ra, `Opera Software\Opera GX Stable`), "opera://extensions/?id=", true},
	}
}

type extList struct {
	Exts     []browserExt `json:"exts"`
	Browsers []string     `json:"browsers"` // the browsers found
	High     int          `json:"high"`
	Medium   int          `json:"medium"`
}

// extScan lists every browser's extensions (read only).
func extScan() extList {
	out := extList{Exts: []browserExt{}, Browsers: []string{}}
	for _, b := range extBrowsers() {
		if fi, err := os.Stat(b.userData); err != nil || !fi.IsDir() {
			continue
		}
		out.Browsers = append(out.Browsers, b.id)
		out.Exts = append(out.Exts, scanChromium(b.id, b.userData, b.flat)...)
	}
	ff := filepath.Join(os.Getenv("APPDATA"), `Mozilla\Firefox\Profiles`)
	if fi, err := os.Stat(ff); err == nil && fi.IsDir() {
		out.Browsers = append(out.Browsers, "firefox")
		out.Exts = append(out.Exts, scanFirefox(ff)...)
	}
	sortExts(out.Exts)
	for _, x := range out.Exts {
		switch x.Risk {
		case "high":
			out.High++
		case "medium":
			out.Medium++
		}
	}
	return out
}

// browserExe finds a browser's program from Windows' App Paths list.
func browserExe(name string) string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if err == nil && v != "" {
			if _, e := os.Stat(v); e == nil {
				return v
			}
		}
	}
	return ""
}

// extOpen opens the browser's own extensions page for one extension, as the
// signed-in user (never with this app's administrator rights).
func extOpen(browser, id string) map[string]any {
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("@._-{}", c)
		if !ok {
			return map[string]any{"error": "bad id"}
		}
	}
	exe, url := "", ""
	if browser == "firefox" {
		exe, url = browserExe("firefox.exe"), "about:addons"
	} else {
		for _, b := range extBrowsers() {
			if b.id == browser {
				exe, url = browserExe(b.exe), b.page+id
				if exe == "" && b.id == "opera" {
					exe = filepath.Join(P.local, `Programs\Opera\opera.exe`)
				}
				if b.id == "operagx" { // GX has its own folder; App Paths may point at plain Opera
					if gx := filepath.Join(P.local, `Programs\Opera GX\opera.exe`); fileExists(gx) {
						exe = gx
					}
				}
			}
		}
	}
	if exe == "" {
		return map[string]any{"error": T("ext.err.noBrowser")}
	}
	if _, err := os.Stat(exe); err != nil {
		return map[string]any{"error": T("ext.err.noBrowser")}
	}
	if err := startAsUser(exe, `"`+exe+`" `+url); err != nil {
		return map[string]any{"error": err.Error()}
	}
	logEvent("ext", "info", browser, T("log.extOpen", browser, id), nil)
	return map[string]any{"ok": true}
}

// extShowFolder shows an extension's files in File Explorer.
func extShowFolder(path string) bool {
	la, ra := strings.ToLower(P.local), strings.ToLower(os.Getenv("APPDATA"))
	lp := strings.ToLower(path)
	if (la == "" || !strings.HasPrefix(lp, la)) && (ra == "" || !strings.HasPrefix(lp, ra)) {
		return false
	}
	return showInFolder(path)
}
