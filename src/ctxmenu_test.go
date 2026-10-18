package main

import (
	"strings"
	"testing"
)

func TestMenuName(t *testing.T) {
	for in, want := range map[string]string{
		"Open with &VLC":          "Open with VLC",
		"&Scan with Malwarebytes": "Scan with Malwarebytes",
		"Tom && Jerry":            "Tom & Jerry",
		"  Edit with Notepad++ ":  "Edit with Notepad++",
		"Plain":                   "Plain",
	} {
		if got := menuName(in); got != want {
			t.Errorf("menuName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMenuProgram(t *testing.T) {
	env := func(k string) string {
		return map[string]string{"SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`}[k]
	}
	for in, want := range map[string]string{
		`"C:\Program Files\VideoLAN\VLC\vlc.exe" --started-from-file "%1"`:          `C:\Program Files\VideoLAN\VLC\vlc.exe`,
		`C:\Program Files\Notepad++\notepad++.exe "%1"`:                             `C:\Program Files\Notepad++\notepad++.exe`,
		`%SystemRoot%\System32\rundll32.exe "C:\Program Files\App\menu.dll",Run %1`: `C:\Program Files\App\menu.dll`,
		`rundll32.exe C:\Tools\x.dll,Entry`:                                         `C:\Tools\x.dll`,
		`regsvr32.exe /s /u "C:\Tools\y.dll"`:                                       `C:\Tools\y.dll`,
		`"%ProgramFiles%\7-Zip\7zFM.exe" "%1"`:                                      `C:\Program Files\7-Zip\7zFM.exe`,
		`notepad.exe %1`:                                                            `notepad.exe`,
		``:                                                                          ``,
	} {
		if got := menuProgram(in, env); got != want {
			t.Errorf("menuProgram(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`"C:\Program Files\VideoLAN\VLC\vlc.exe",0`: `C:\Program Files\VideoLAN\VLC\vlc.exe`,
		`C:\Tools\app.exe,-101`:                     `C:\Tools\app.exe`,
		`%SystemRoot%\System32\shell32.dll,3`:       `C:\Windows\System32\shell32.dll`,
		`C:\Tools\app.ico`:                          `C:\Tools\app.ico`,
	} {
		if got := iconFile(in, env); got != want {
			t.Errorf("iconFile(%q) = %q, want %q", in, got, want)
		}
	}
}

// Windows' own entries are never offered: anything under the Windows
// folder, a bare program name, or nothing at all.
func TestWindowsOwn(t *testing.T) {
	win := `C:\Windows`
	for p, want := range map[string]bool{
		`C:\Windows\System32\shell32.dll`:  true,
		`c:\windows\explorer.exe`:          true,
		`C:\Windows`:                       true,
		`cmd.exe`:                          true,
		``:                                 true,
		`C:\WindowsApps\x.exe`:             false, // only the Windows folder itself
		`C:\Program Files\7-Zip\7-zip.dll`: false,
		`C:\Program Files\Windows Defender\shellext.dll`: false,
	} {
		if got := windowsOwn(p, win); got != want {
			t.Errorf("windowsOwn(%q) = %v, want %v", p, got, want)
		}
	}
	if !isGUID("{23170F69-40C1-278A-1000-000100020000}") || isGUID("23170F69-40C1-278A-1000-000100020000") || isGUID("{nope}") {
		t.Error("isGUID")
	}
}

// The same entry under files and folders is one switch; an extension is
// one entry per class id; ids stay the same between scans.
func TestMergeCtx(t *testing.T) {
	in := []ctxItem{
		{Kind: "verb", Name: "Open with VLC", Where: []string{"folders"}, Program: `C:\VLC\vlc.exe`, locs: []ctxLoc{{"hklm", `Directory\shell\vlc`}}},
		{Kind: "verb", Name: "Open with VLC", Where: []string{"files"}, Program: `c:\vlc\VLC.exe`, Off: true, locs: []ctxLoc{{"hklm", `*\shell\vlc`}}},
		{Kind: "ext", Name: "7-Zip", Where: []string{"files", "folders"}, Program: `C:\7-Zip\7-zip.dll`, CLSID: "{23170F69-40C1-278A-1000-000100020000}", locs: []ctxLoc{{"hklm", `*\shellex\ContextMenuHandlers\7-Zip`}}},
		{Kind: "ext", Name: " 7-Zip", Where: []string{"background"}, Program: `C:\7-Zip\7-zip.dll`, CLSID: "{23170f69-40c1-278a-1000-000100020000}", locs: []ctxLoc{{"hklm", `Directory\Background\shellex\ContextMenuHandlers\7-Zip`}}},
		{Kind: "verb", Name: "Add to archive", Where: []string{"files"}, Program: `C:\WinRAR\rar.exe`, Shift: true, locs: []ctxLoc{{"hkcu", `*\shell\rar`}}},
	}
	out := mergeCtx(in)
	if len(out) != 3 {
		t.Fatalf("%d entries: %+v", len(out), out)
	}
	byName := map[string]ctxItem{}
	for _, it := range out {
		byName[strings.TrimSpace(it.Name)] = it
		if len(it.ID) != 16 {
			t.Errorf("id %q", it.ID)
		}
	}
	vlc := byName["Open with VLC"]
	if strings.Join(vlc.Where, ",") != "files,folders" || len(vlc.locs) != 2 || vlc.Off {
		t.Errorf("vlc (shown for folders = on): %+v", vlc)
	}
	z := byName["7-Zip"]
	if strings.Join(z.Where, ",") != "files,folders,background" || len(z.locs) != 2 {
		t.Errorf("7-zip: %+v", z)
	}
	if !byName["Add to archive"].Shift {
		t.Error("shift")
	}
	if again := mergeCtx(in); again[0].ID != out[0].ID || again[2].ID != out[2].ID {
		t.Error("ids change between scans")
	}
	if out[0].Name > out[1].Name && strings.TrimSpace(out[0].Name) > strings.TrimSpace(out[1].Name) {
		t.Errorf("not sorted by name: %q %q", out[0].Name, out[1].Name)
	}
}
