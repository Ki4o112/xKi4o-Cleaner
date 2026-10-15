//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestRegLoc(t *testing.T) {
	cases := []struct{ in, full string }{
		{`HKCU\Software\X`, `HKCU\Software\X`},
		{`HKLM\SOFTWARE\X`, `HKLM\SOFTWARE\X`},
		{`HKLM32\SOFTWARE\X\Y`, `HKLM\SOFTWARE\WOW6432Node\X\Y`},
		{`HKLM32\SOFTWARE`, `HKLM\SOFTWARE\WOW6432Node`},
	}
	for _, c := range cases {
		_, _, full, ok := regLoc(c.in)
		if !ok || full != c.full {
			t.Errorf("%s -> %s %v", c.in, full, ok)
		}
	}
	if _, _, _, ok := regLoc(`HKU\x`); ok {
		t.Error("HKU must not be reachable")
	}
	// structural keys are refused before anything is opened
	for _, k := range []string{`HKCU\Software`, `HKCU\Software\Microsoft`, `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, `HKLM32\SOFTWARE`} {
		if err := regDeleteKey(k); err == nil {
			t.Errorf("%s: deleting must be refused", k)
		}
	}
}

// A key of this test only (HKCU\Software\xKi4o-test-...): saved to a .reg
// file, deleted, and put back from the file with reg.exe. The backup goes
// to a temporary folder, not to the app's own.
func TestRegBackupDeleteRestore(t *testing.T) {
	initPaths()
	t.Setenv("LOCALAPPDATA", t.TempDir())
	base := fmt.Sprintf(`Software\xKi4o-test-%d`, time.Now().UnixNano())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\Probe App\Sub`, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.SetStringValue("Name", "Пробна стойност")
	_ = k.SetDWordValue("Count", 7)
	k.Close()
	defer func() { _ = regDeleteKey(`HKCU\` + base) }()
	path := `HKCU\` + base + `\Probe App`
	if !(winReg{}).Exists(path) || len((winReg{}).Subkeys(path)) != 1 {
		t.Fatal("test key not readable")
	}
	file := filepath.Join(regBackupDir(), "2026-01-01 000000 Probe App.reg")
	if err := regExportAll([]string{path}, file); err != nil {
		t.Fatalf("export: %v", err)
	}
	b, _ := os.ReadFile(file)
	text := decodeRegFile(b)
	if !strings.HasPrefix(text, "Windows Registry Editor Version 5.00") || !strings.Contains(text, `\Probe App\Sub]`) || !strings.Contains(text, "Пробна стойност") {
		t.Fatalf("backup content:\n%s", text)
	}
	if err := regDeleteKey(path); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if (winReg{}).Exists(path) {
		t.Fatal("key still there")
	}
	list := regBackups()
	if len(list) != 1 || list[0].Name != "Probe App" || list[0].Keys < 2 {
		t.Fatalf("backups: %+v", list)
	}
	if msg := regRestore(list[0].File); msg != "" {
		t.Fatalf("restore: %s", msg)
	}
	if v := (winReg{}).String(path+`\Sub`, "Name"); v != "Пробна стойност" {
		t.Fatalf("restored value = %q", v)
	}
	// only files of the backup folder are imported
	for _, bad := range []string{`..\x.reg`, `C:\x.reg`, `x.txt`, ``} {
		if regRestore(bad) == "" {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// The list of installed programs is read; for each of the first programs
// the leftover scan runs as if it had been uninstalled (nothing is deleted;
// sizes are not measured). Whatever it would propose must respect the rules.
func TestAppsListAndDryLeftoverScan(t *testing.T) {
	initPaths()
	v := appsList()
	t.Logf("%d programs", len(v.Apps))
	for _, a := range v.Apps {
		if a.ID == "" || a.Name == "" || a.Kind != "win32" {
			t.Errorf("bad entry %+v", a)
		}
	}
	if len(v.Apps) == 0 && !underWine() {
		t.Fatal("no programs found on a real Windows")
	}
	prot := []string{windowsDir(), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), P.programData, P.local, P.roaming, P.profile}
	n := 0
	for _, a := range v.Apps {
		if n++; n > 60 {
			break
		}
		sc := leftoverScope(a, v.Apps, 3)
		ident := appIdent{Name: a.Name, Publisher: a.Publisher, Location: a.Location, Exes: a.Exes, Key: a.Key}
		sc.Uninstall = a.Key
		found := scanLeftovers(ident, sc, sizelessFS{}, winReg{})
		sure := 0
		for _, l := range found {
			if l.Sure {
				sure++
			}
			switch l.Kind {
			case "key":
				if !regKeyDeletable(l.Path) {
					t.Errorf("%s: proposes a protected key %s", a.Name, l.Path)
				}
			case "value":
				if !strings.HasSuffix(l.Path, `\MuiCache`) {
					t.Errorf("%s: proposes a value of %s", a.Name, l.Path)
				}
			default:
				for _, p := range prot {
					if p != "" && lowerClean(p) == lowerClean(l.Path) {
						t.Errorf("%s: proposes a whole system folder %s", a.Name, l.Path)
					}
				}
				if underWindowsDir(l.Path) {
					t.Errorf("%s: proposes something in Windows: %s", a.Name, l.Path)
				}
				for _, u := range sc.UserData {
					if l.Sure && under(l.Path, u) && l.Kind == "dir" {
						t.Errorf("%s: pre-ticks the user's data %s", a.Name, l.Path)
					}
				}
			}
		}
		t.Logf("%-50.50s %3d found, %2d sure, last used %s", a.Name, len(found), sure, unixDay(a.LastUsed))
	}
	// a name no program has: nothing at all is proposed
	fake := instApp{ID: "x", Name: "Zqxv Nonexistent Probe 9", Publisher: "Zqxv Labs"}
	sc := leftoverScope(fake, v.Apps, 3)
	if found := scanLeftovers(appIdent{Name: fake.Name, Publisher: fake.Publisher}, sc, sizelessFS{}, winReg{}); len(found) != 0 {
		t.Errorf("a program that never existed has leftovers: %+v", found)
	}
}

type sizelessFS struct{ osLeftFS }

func (sizelessFS) Size(string) (int64, int) { return 0, 0 }

func unixDay(t int64) string {
	if t <= 0 {
		return "-"
	}
	return time.Unix(t, 0).Format("2006-01-02")
}

func TestStoreAppsList(t *testing.T) {
	if underWine() {
		t.Skip("no Store apps under Wine")
	}
	v := storeApps()
	t.Logf("%d Store apps", len(v.Apps))
	for i, a := range v.Apps {
		if a.Kind != "store" || !strings.HasPrefix(a.ID, "appx:") || a.Family == "" || a.Name == "" {
			t.Errorf("bad entry %+v", a)
		}
		if i < 8 {
			t.Logf("  %s (%s) %s", a.Name, a.Publisher, a.Version)
		}
	}
}

// The whole uninstall flow with a made-up program: its uninstaller (cmd.exe)
// removes its exe and its entry; the scan finds what it left; the chosen
// leftovers go (keys after a backup); the backup puts the key back.
func TestUninstallFlow(t *testing.T) {
	changesThisPC(t)
	initPaths()
	t.Setenv("LOCALAPPDATA", t.TempDir()) // the registry backup goes here
	pf := os.Getenv("ProgramFiles")
	dir := filepath.Join(pf, "xKi4o Test Vendor", "xKi4o Probe App")
	mk(t, filepath.Join(dir, "probe.exe"), 100, 0)
	mk(t, filepath.Join(dir, "settings.ini"), 10, 0)
	data := filepath.Join(P.programData, "xKi4o Test Vendor", "xKi4o Probe App")
	mk(t, filepath.Join(data, "db.dat"), 10, 0)
	user := filepath.Join(P.roaming, "xKi4o Probe App")
	mk(t, filepath.Join(user, "prefs.json"), 10, 0)
	defer os.RemoveAll(filepath.Join(pf, "xKi4o Test Vendor"))
	defer os.RemoveAll(filepath.Join(P.programData, "xKi4o Test Vendor"))
	defer os.RemoveAll(user)
	const entry = `Software\Microsoft\Windows\CurrentVersion\Uninstall\xKi4oProbeApp`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, entry, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	cmd := filepath.Join(windowsDir(), "System32", "cmd.exe")
	_ = k.SetStringValue("DisplayName", "xKi4o Probe App")
	_ = k.SetStringValue("Publisher", "xKi4o Test Vendor")
	_ = k.SetStringValue("DisplayVersion", "1.2.3")
	_ = k.SetStringValue("InstallLocation", dir)
	_ = k.SetStringValue("UninstallString", `"`+cmd+`" /c del /q "`+filepath.Join(dir, "probe.exe")+`" & reg delete "HKCU\`+entry+`" /f`)
	k.Close()
	defer func() { _ = registry.DeleteKey(registry.CURRENT_USER, entry) }()
	sk, _, _ := registry.CreateKey(registry.CURRENT_USER, `Software\xKi4o Test Vendor\xKi4o Probe App`, registry.ALL_ACCESS)
	_ = sk.SetStringValue("x", "1")
	sk.Close()
	defer func() { _ = regDeleteKey(`HKCU\Software\xKi4o Test Vendor`) }()

	v := appsList()
	id := ""
	for _, a := range v.Apps {
		if a.Name == "xKi4o Probe App" {
			id = a.ID
			if a.Location == "" || len(a.Exes) == 0 || a.Broken {
				t.Errorf("entry read wrong: %+v", a)
			}
		}
	}
	if id == "" {
		t.Fatal("the test program is not listed")
	}
	if !uninstStart(id, uninstOpts{Level: 2}) {
		t.Fatal("did not start")
	}
	if uninstStart(id, uninstOpts{Level: 2}) {
		t.Error("a second uninstall at the same time must be refused")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		uj.mu.Lock()
		ph := uj.phase
		uj.mu.Unlock()
		if ph == "found" || ph == "still" || ph == "error" {
			if ph != "found" {
				t.Fatalf("ended with %s", ph)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stuck in %s", ph)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if exists(filepath.Join(dir, "probe.exe")) {
		t.Error("the uninstaller did not run")
	}
	uj.mu.Lock()
	found := append([]leftover(nil), uj.found...)
	uj.mu.Unlock()
	want := map[string]bool{dir: true, data: true, `HKCU\Software\xKi4o Test Vendor\xKi4o Probe App`: true}
	var ids []string
	for _, l := range found {
		t.Logf("found %s %s sure=%v %s", l.Kind, l.Path, l.Sure, l.Why)
		if strings.EqualFold(l.Path, user) {
			if l.Sure {
				t.Error("the user's settings folder must not be pre-ticked")
			}
		} else if want[l.Path] && !l.Sure {
			t.Errorf("%s should be ticked", l.Path)
		}
		delete(want, l.Path)
		ids = append(ids, l.ID)
	}
	if len(want) > 0 {
		t.Fatalf("not found: %v", want)
	}
	res := uninstRemove(ids)
	for _, r := range res.Items {
		if !r.OK {
			t.Errorf("not removed: %+v", r)
		}
	}
	if res.Backup == "" || exists(dir) || exists(data) || (winReg{}).Exists(`HKCU\Software\xKi4o Test Vendor\xKi4o Probe App`) {
		t.Fatalf("after removing: %+v", res)
	}
	if msg := regRestore(res.Backup); msg != "" || !(winReg{}).Exists(`HKCU\Software\xKi4o Test Vendor\xKi4o Probe App`) {
		t.Fatalf("the backup did not put the key back: %s", msg)
	}
	uninstFinish()
}
