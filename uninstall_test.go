package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestAppNames(t *testing.T) {
	for in, want := range map[string]string{
		"7-Zip 23.01 (x64)":           "7-Zip",
		"Mozilla Firefox (x64 en-US)": "Mozilla Firefox",
		"Discord":                     "Discord",
		"OBS Studio":                  "OBS Studio",
		"Notepad++ (64-bit x64)":      "Notepad++",
		"VLC media player":            "VLC media player",
		"Python 3.12.1 (64-bit)":      "Python",
		"Microsoft Visual C++ 2015-2022 Redistributable (x64) - 14.40.33810": "Microsoft Visual C++ Redistributable",
	} {
		if got := cleanAppName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if p := cleanPublisher("Discord Inc."); p != "Discord" {
		t.Errorf("publisher %q", p)
	}
	n := appIdent{Name: "Acme Photo Studio 2.1 (x64)", Publisher: "Acme Software, Inc.", Exes: []string{`C:\x\PhotoStudio.exe`, `C:\x\unins000.exe`, `C:\x\update.exe`}}.names()
	if n.full != "acmephotostudio" || n.pub != "acmesoftware" || n.short != "photostudio" || len(n.pubs) != 2 || len(n.strong) != 0 || len(n.exes) != 1 || n.exes[0] != "photostudio.exe" {
		t.Fatalf("%+v", n)
	}
	for name, want := range map[string]int{"Acme Photo Studio": 1, "acme-photo-studio": 1, "Photo": 0, "Studio": 0, "AcmePhotoStudioBackup": 3, "Microsoft": 0, "": 0} {
		if got := n.match(name); got != want {
			t.Errorf("match(%q) = %d, want %d", name, got, want)
		}
	}
	if n.matchIn("Photo Studio", true) != 1 || n.matchIn("Photo Studio", false) != 0 || n.matchIn("Photo", true) != 2 || n.matchIn("Photo", false) != 0 {
		t.Error("inside the publisher's folder")
	}
	d := appIdent{Name: "Discord", Publisher: "Discord Inc."}.names()
	if d.match("discord") != 1 || d.match("DiscordPTB") != 3 {
		t.Errorf("discord: %d %d", d.match("discord"), d.match("DiscordPTB"))
	}
	f := appIdent{Name: "Mozilla Firefox (x64 en-US)", Publisher: "Mozilla"}.names()
	if f.match("Firefox") != 2 || f.matchIn("Firefox", true) != 1 || f.match("Mozilla") != 0 {
		t.Errorf("firefox: %+v", f)
	}
}

// fakeReg is a registry in memory: key path -> values (name -> string).
type fakeReg map[string]map[string]string

func (r fakeReg) Subkeys(p string) []string {
	var out []string
	seen := map[string]bool{}
	pre := strings.ToLower(p) + `\`
	for k := range r {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, pre) {
			name := strings.SplitN(k[len(pre):], `\`, 2)[0]
			if !seen[strings.ToLower(name)] {
				seen[strings.ToLower(name)] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func (r fakeReg) Exists(p string) bool {
	for k := range r {
		if strings.EqualFold(k, p) || strings.HasPrefix(strings.ToLower(k), strings.ToLower(p)+`\`) {
			return true
		}
	}
	return false
}

func (r fakeReg) Values(p string) []string {
	var out []string
	for k, v := range r {
		if strings.EqualFold(k, p) {
			for n := range v {
				out = append(out, n)
			}
		}
	}
	return out
}

func (r fakeReg) String(p, name string) string {
	for k, v := range r {
		if strings.EqualFold(k, p) {
			return v[name]
		}
	}
	return ""
}

// makeLnk writes a shortcut that points to target (the way Windows does,
// with the Unicode path in the link info).
func makeLnk(t *testing.T, path, target string) {
	t.Helper()
	var b bytes.Buffer
	hdr := make([]byte, 0x4C)
	binary.LittleEndian.PutUint32(hdr, 0x4C)
	copy(hdr[4:], []byte{0x01, 0x14, 0x02, 0, 0, 0, 0, 0, 0xC0, 0, 0, 0, 0, 0, 0, 0x46})
	binary.LittleEndian.PutUint32(hdr[20:], lnkHasLinkInfo|lnkUnicode)
	b.Write(hdr)
	ansi := append([]byte("C:\\ansi-ignored"), 0)
	u := utf16.Encode([]rune(target))
	uni := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(uni[2*i:], c)
	}
	vol := make([]byte, 0x14)
	binary.LittleEndian.PutUint32(vol, 0x14)
	const hsize = 0x24
	volOff := hsize
	ansiOff := volOff + len(vol)
	sufOff := ansiOff + len(ansi)
	uniOff := sufOff + 1
	uniSufOff := uniOff + len(uni)
	size := uniSufOff + 2
	info := make([]byte, size)
	binary.LittleEndian.PutUint32(info[0:], uint32(size))
	binary.LittleEndian.PutUint32(info[4:], hsize)
	binary.LittleEndian.PutUint32(info[8:], 1)
	binary.LittleEndian.PutUint32(info[12:], uint32(volOff))
	binary.LittleEndian.PutUint32(info[16:], uint32(ansiOff))
	binary.LittleEndian.PutUint32(info[24:], uint32(sufOff))
	binary.LittleEndian.PutUint32(info[28:], uint32(uniOff))
	binary.LittleEndian.PutUint32(info[32:], uint32(uniSufOff))
	copy(info[volOff:], vol)
	copy(info[ansiOff:], ansi)
	copy(info[uniOff:], uni)
	b.Write(info)
	b.Write([]byte{0, 0, 0, 0}) // end of extra data
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseLnk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.lnk")
	makeLnk(t, p, `C:\Program Files\Acme\Фото\photo.exe`)
	li, err := readLnkFile(p)
	if err != nil || li.Target != `C:\Program Files\Acme\Фото\photo.exe` || li.Special || li.Network {
		t.Fatalf("%+v %v", li, err)
	}
	makeLnk(t, p, `\\server\share\app.exe`)
	if li, _ := readLnkFile(p); !li.Network {
		t.Errorf("network: %+v", li)
	}
	if _, err := parseLnk([]byte("not a shortcut at all, really not"), dir); err == nil {
		t.Error("garbage parsed")
	}
	// a shell item only (a Store app): nothing to check
	hdr := make([]byte, 0x4C+4)
	binary.LittleEndian.PutUint32(hdr, 0x4C)
	copy(hdr[4:], []byte{0x01, 0x14, 0x02, 0, 0, 0, 0, 0, 0xC0, 0, 0, 0, 0, 0, 0, 0x46})
	if li, err := parseLnk(hdr, dir); err != nil || !li.Special {
		t.Errorf("shell item: %+v %v", li, err)
	}
}

func TestScanLeftovers(t *testing.T) {
	root := t.TempDir()
	pf, pd, ad, sm, desk := filepath.Join(root, "PF"), filepath.Join(root, "PD"), filepath.Join(root, "AppData"), filepath.Join(root, "Start"), filepath.Join(root, "Desktop")
	loc := filepath.Join(pf, "Acme", "Photo Studio")
	other := filepath.Join(pf, "Acme", "Other App")
	mk(t, filepath.Join(loc, "settings.ini"), 100, 0) // left by the uninstaller
	mk(t, filepath.Join(other, "other.exe"), 10, 0)   // another installed program
	mk(t, filepath.Join(pd, "Acme", "Photo Studio", "db.dat"), 50, 0)
	mk(t, filepath.Join(ad, "Acme Photo Studio", "prefs.json"), 20, 0) // the user's settings
	mk(t, filepath.Join(ad, "Photo", "x"), 1, 0)
	mk(t, filepath.Join(ad, "AcmePhotoStudioBackup", "x"), 1, 0)
	mk(t, filepath.Join(ad, "Microsoft", "x"), 1, 0)
	makeLnk(t, filepath.Join(sm, "Acme Photo Studio.lnk"), filepath.Join(loc, "photostudio.exe"))
	makeLnk(t, filepath.Join(sm, "Notepad.lnk"), `C:\Windows\notepad.exe`)
	makeLnk(t, filepath.Join(desk, "My photos.lnk"), filepath.Join(loc, "photostudio.exe"))
	makeLnk(t, filepath.Join(desk, "Other.lnk"), filepath.Join(other, "other.exe"))
	id := appIdent{Name: "Acme Photo Studio 2.1 (x64)", Publisher: "Acme Software Inc.", Location: loc, Exes: []string{filepath.Join(loc, "photostudio.exe")}, Key: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AcmePhoto`}
	reg := fakeReg{
		`HKCU\Software\Acme Software\Photo Studio`:                                       {"x": "1"},
		`HKCU\Software\Acme Software\Other App`:                                          {"x": "1"},
		`HKLM\SOFTWARE\Acme Photo Studio`:                                                {"x": "1"},
		`HKLM32\SOFTWARE\Microsoft\Something`:                                            {},
		`HKCU\Software\Classes\Applications\photostudio.exe`:                             {},
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\photostudio.exe`:       {"": filepath.Join(loc, "photostudio.exe")},
		`HKCU\Software\Classes\Local Settings\Software\Microsoft\Windows\Shell\MuiCache`: {filepath.Join(loc, "photostudio.exe") + ".FriendlyAppName": "Photo", `C:\other.exe.FriendlyAppName`: "x"},
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AcmePhoto`:             {"DisplayName": "Acme Photo Studio"},
	}
	sc := lfScope{
		Roots:      []lfRoot{{pf, 2, "programs"}, {pd, 2, "data"}, {ad, 2, "userdata"}, {sm, 2, "startmenu"}, {desk, 1, "desktop"}},
		Protected:  []string{other, filepath.Join(root, "Windows")},
		UserData:   []string{ad},
		OtherNames: []string{"otherapp"},
		RegRoots:   []string{`HKCU\Software`, `HKLM\SOFTWARE`, `HKLM32\SOFTWARE`},
		Uninstall:  id.Key,
	}
	find := func(ls []leftover, path, value string) *leftover {
		for i := range ls {
			if strings.EqualFold(ls[i].Path, path) && ls[i].Value == value {
				return &ls[i]
			}
		}
		return nil
	}
	for level := 1; level <= 3; level++ {
		sc.Level = level
		ls := scanLeftovers(id, sc, osLeftFS{}, reg)
		for _, l := range ls { // every key it proposes can be deleted: nothing structural is proposed
			if l.Kind == "key" && !regKeyDeletable(l.Path) {
				t.Errorf("proposed a key that would be refused: %s", l.Path)
			}
		}
		want := []struct {
			path, value string
			sure        bool
			level       int
		}{
			{loc, "", true, 1},
			{filepath.Join(pd, "Acme", "Photo Studio"), "", true, 1},
			{filepath.Join(ad, "Acme Photo Studio"), "", false, 1}, // the user's data: shown, never pre-ticked
			{filepath.Join(sm, "Acme Photo Studio.lnk"), "", true, 1},
			{filepath.Join(desk, "My photos.lnk"), "", true, 1},
			{`HKCU\Software\Acme Software\Photo Studio`, "", true, 1},
			{`HKLM\SOFTWARE\Acme Photo Studio`, "", true, 1},
			{`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\photostudio.exe`, "", true, 1},
			{id.Key, "", true, 1},
			{`HKCU\Software\Classes\Applications\photostudio.exe`, "", false, 2},
			{filepath.Join(ad, "AcmePhotoStudioBackup"), "", false, 3},
			{`HKCU\Software\Classes\Local Settings\Software\Microsoft\Windows\Shell\MuiCache`, filepath.Join(loc, "photostudio.exe") + ".FriendlyAppName", false, 3},
		}
		for _, w := range want {
			l := find(ls, w.path, w.value)
			if w.level > level {
				if l != nil {
					t.Errorf("level %d: %s (level %d) listed", level, w.path, w.level)
				}
				continue
			}
			if l == nil {
				t.Errorf("level %d: %s %s not found", level, w.path, w.value)
			} else if l.Sure != w.sure {
				t.Errorf("level %d: %s sure=%v", level, w.path, l.Sure)
			}
		}
		// never: another program's folder or key, the shared publisher folder and key, Windows, shortcuts of others
		for _, never := range []string{other, filepath.Join(pf, "Acme"), pf, filepath.Join(ad, "Photo"), filepath.Join(ad, "Microsoft"),
			filepath.Join(sm, "Notepad.lnk"), filepath.Join(desk, "Other.lnk"), `HKCU\Software\Acme Software`, `HKCU\Software\Acme Software\Other App`, `HKLM32\SOFTWARE\Microsoft`} {
			if l := find(ls, never, ""); l != nil {
				t.Errorf("level %d: %s proposed (%s)", level, never, l.Why)
			}
		}
	}
	// a wrong "install location" (a whole games folder) is shown, never ticked;
	// one that holds another program is not shown at all
	games := filepath.Join(root, "Games")
	mk(t, filepath.Join(games, "x.bin"), 5, 0)
	sc.Level = 2
	id2 := id
	id2.Location = games
	if l := find(scanLeftovers(id2, sc, osLeftFS{}, fakeReg{}), games, ""); l == nil || l.Sure {
		t.Errorf("games folder: %+v", l)
	}
	sc.Protected = append(sc.Protected, filepath.Join(games, "Some Game"))
	if l := find(scanLeftovers(id2, sc, osLeftFS{}, fakeReg{}), games, ""); l != nil {
		t.Errorf("a folder holding another program was proposed")
	}
}

func TestLastUsedSources(t *testing.T) {
	if prefetchName("DISCORD.EXE-1A2B3C4D.pf") != "discord.exe" || prefetchName("x.txt") != "" || prefetchName("NO-HASH") != "" {
		t.Error("prefetch name")
	}
	if rot13(`P:\Cebtenz Svyrf\Qvfpbeq\Hcqngr.rkr`) != `C:\Program Files\Discord\Update.exe` {
		t.Error("rot13")
	}
	b := make([]byte, 72)
	when := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	binary.LittleEndian.PutUint64(b[60:], uint64((when.Unix()+11644473600)*10000000))
	if userAssistTime(b) != when.Unix() || userAssistTime(b[:20]) != 0 {
		t.Error("userassist time")
	}
	u := usage{byName: map[string]int64{"photo.exe": 100, "update.exe": 900}, byPath: map[string]int64{strings.ToLower(filepath.Clean(`C:\x\update.exe`)): 300}}
	if got := u.lastUsed([]string{`C:\x\photo.exe`, `C:\y\update.exe`}); got != 100 {
		t.Errorf("a generic name counted: %d", got)
	}
	if got := u.lastUsed([]string{`C:\x\update.exe`}); got != 300 {
		t.Errorf("full path: %d", got)
	}
	if installDate("20241031") == 0 || installDate("2024") != 0 || installDate("abcdefgh") != 0 {
		t.Error("install date")
	}
	if cleanRegPath(`"C:\a b\x.exe",0`) != `C:\a b\x.exe` || cleanRegPath(`C:\x.exe`) != `C:\x.exe` {
		t.Error("reg path")
	}
	dir := t.TempDir()
	mk(t, filepath.Join(dir, "app.exe"), 1, 0)
	mk(t, filepath.Join(dir, "bin", "tool.exe"), 1, 0)
	mk(t, filepath.Join(dir, "bin", "deep", "no.exe"), 1, 0)
	mk(t, filepath.Join(dir, "readme.txt"), 1, 0)
	if exes := programExes(dir, 10); len(exes) != 2 {
		t.Errorf("exes: %v", exes)
	}
}

func TestRegKeyDeletable(t *testing.T) {
	yes := []string{
		`HKCU\Software\Acme`, `HKCU\Software\Acme\Photo Studio`, `HKLM\SOFTWARE\Acme`, `HKLM32\SOFTWARE\Acme`,
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{12345678-1234-1234-1234-123456789012}`,
		`HKLM32\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Acme`,
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\acme.exe`,
		`HKCU\Software\Classes\Applications\acme.exe`, `HKCU\Software\Microsoft\Teams`,
	}
	no := []string{
		``, `HKCU`, `HKCU\Software`, `HKLM\SOFTWARE`, `HKLM32\SOFTWARE`, `HKLM\SOFTWARE\WOW6432Node`, `HKLM\SYSTEM\CurrentControlSet\Services\Acme`,
		`HKCU\Software\Microsoft`, `HKCU\Software\Microsoft\Windows`, `HKCU\Software\Microsoft\Windows\CurrentVersion`,
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, `HKLM32\SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`,
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths`,
		`HKCU\Software\Classes`, `HKCU\Software\Classes\Applications`, `HKCU\Software\Classes\Local Settings`,
		`HKCU\Software\Policies`, `HKLM\SOFTWARE\Microsoft\Windows NT`, `HKU\S-1-5-18\Software\Acme`, `HKCR\Acme`,
	}
	for _, k := range yes {
		if !regKeyDeletable(k) {
			t.Errorf("%s: should be deletable", k)
		}
	}
	for _, k := range no {
		if regKeyDeletable(k) {
			t.Errorf("%s: must never be deleted", k)
		}
	}
	if got := regCanon(`HKLM32\SOFTWARE\Acme\X`); got != `HKLM\SOFTWARE\WOW6432Node\Acme\X` {
		t.Errorf("regCanon = %s", got)
	}
}
