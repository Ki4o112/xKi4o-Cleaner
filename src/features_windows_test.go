//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestListDirFull(t *testing.T) {
	root := t.TempDir()
	mk(t, filepath.Join(root, "a.bin"), 1234, 0)
	mk(t, filepath.Join(root, "sub", "b.bin"), 10, 0)
	ents, err := listDirFull(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]dirEnt{}
	for _, e := range ents {
		got[e.name] = e
	}
	if e := got["a.bin"]; e.dir || e.size != 1234 || e.mtime == 0 {
		t.Fatalf("file entry: %+v", e)
	}
	if e := got["sub"]; !e.dir || e.link {
		t.Fatalf("dir entry: %+v", e)
	}
	if len(ents) != 2 {
		t.Fatalf("entries: %v", ents)
	}
}

func waitDisk(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		dj.mu.Lock()
		r := dj.running
		dj.mu.Unlock()
		if !r {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("disk scan did not finish")
}

func TestDiskJob(t *testing.T) {
	initPaths()
	root := t.TempDir()
	mk(t, filepath.Join(root, "Games", "big.pak"), 9000, 0)
	mk(t, filepath.Join(root, "Games", "small.cfg"), 10, 0)
	mk(t, filepath.Join(root, "Videos", "clip.mp4"), 4000, 0)
	if !diskScan(root) {
		t.Fatal("scan did not start")
	}
	if diskScan(root) {
		t.Fatal("a second scan started while one runs")
	}
	waitDisk(t)
	v := diskChildren(root)
	if !v.OK || len(v.Items) != 2 || v.Items[0].Name != "Games" || v.Item.Size != 13010 {
		t.Fatalf("children: %+v", v)
	}
	if top := diskTop(diskFilter{Min: 1000}); len(top) != 2 || top[0].Name != "big.pak" {
		t.Fatalf("top: %+v", top)
	}
	if top := diskTop(diskFilter{Cat: "video"}); len(top) != 1 || top[0].Name != "clip.mp4" {
		t.Fatalf("top video: %+v", top)
	}
	if top := diskTop(diskFilter{Under: filepath.Join(root, "Games"), Query: "BIG"}); len(top) != 1 {
		t.Fatalf("top under: %+v", top)
	}
	// refused: outside the scanned folder, the scanned folder itself, missing
	for _, p := range []string{os.Getenv("SystemRoot"), root, filepath.Join(root, "nope.bin")} {
		if r := diskDelete([]string{p}); r[0].OK {
			t.Fatalf("deleting %s was allowed", p)
		}
	}
	// refresh of one folder after a change outside the app
	mk(t, filepath.Join(root, "Videos", "more.mp4"), 1000, 0)
	if !diskRefresh(filepath.Join(root, "Videos")) {
		t.Fatal("refresh did not start")
	}
	waitDisk(t)
	if v := diskChildren(root); v.Item.Size != 14010 {
		t.Fatalf("after refresh: %d", v.Item.Size)
	}
	// a file in use is refused
	f, err := os.Open(filepath.Join(root, "Games", "small.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	r := diskDelete([]string{filepath.Join(root, "Games", "small.cfg")})
	f.Close()
	if r[0].OK {
		t.Fatal("a file in use was deleted")
	}
}

func TestProtectedPaths(t *testing.T) {
	initPaths()
	me := filepath.Base(P.profile)
	for p, want := range map[string]bool{
		`C:\`: true, `C:\Windows\Temp\x`: true, `C:\Program Files\App`: false, `C:\Program Files`: true, `C:\pagefile.sys`: true,
		`C:\Program Files\Windows Defender\x`: true, `C:\ProgramData\Microsoft\Windows`: true, `C:\ProgramData\SomeApp\cache`: false,
		`C:\Users`: true, `C:\Users\someoneelse\Documents`: true, `C:\Users\` + me: true,
		`C:\Users\` + me + `\Videos\a.mp4`: false, `C:\Games\x`: false, `D:\Movies`: false,
		`C:\Users\` + me + `\AppData\Local`: true, `C:\Users\` + me + `\AppData\Local\Temp\x`: false,
	} {
		if got := protectedPath(p); got != want {
			t.Errorf("protectedPath(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestStartupToggleAndRestore(t *testing.T) {
	changesThisPC(t)
	initPaths()
	withTempLocalAppData(t)
	const name = "xKi4oCleanerTestEntry"
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	cmd := `"` + filepath.Join(os.Getenv("SystemRoot"), "notepad.exe") + `" /x`
	if err := k.SetStringValue(name, cmd); err != nil {
		t.Fatal(err)
	}
	k.Close()
	defer func() {
		if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
			_ = k.DeleteValue(name)
			k.Close()
		}
		if k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey+`\Run`, registry.SET_VALUE); err == nil {
			_ = k.DeleteValue(name)
			k.Close()
		}
	}()
	key := "reg|user|" + name
	e := findStartup(key)
	if e == nil || !e.Enabled {
		t.Fatalf("entry not found or not enabled: %+v", e)
	}
	r := setStartup(key, false)
	if !r.OK || r.Entry == nil || r.Entry.Enabled {
		t.Fatalf("disable: %+v", r)
	}
	// the entry itself is untouched, only the approval changed
	k2, _ := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if v, _, _ := k2.GetStringValue(name); v != cmd {
		t.Fatalf("Run value changed: %q", v)
	}
	k2.Close()
	if en, _, raw, ok := approvedState(registry.CURRENT_USER, "Run", name); en || !ok || raw[0] != 3 {
		t.Fatalf("approved value: %v %v", en, raw)
	}
	// the log has the backup; restoring it removes the value it created
	log := startupLogList()
	if len(log) == 0 || log[0].Action != "disable" || log[0].Backup == nil || log[0].Backup.Exists {
		t.Fatalf("log: %+v", log)
	}
	if r := restoreStartup(log[0].ID); !r.OK {
		t.Fatalf("restore: %+v", r)
	}
	if _, _, _, ok := approvedState(registry.CURRENT_USER, "Run", name); ok {
		t.Fatal("approved value left after restore")
	}
	if r := restoreStartup(log[0].ID); r.OK {
		t.Fatal("restored twice")
	}
	if r := setStartup(key, true); !r.OK || !r.Entry.Enabled {
		t.Fatalf("enable: %+v", r)
	}
	if r := setStartup("reg|user|does-not-exist-xyz", false); r.OK {
		t.Fatal("a missing entry was changed")
	}
}

func TestTweakHistoryUndoAndProfiles(t *testing.T) {
	changesThisPC(t)
	cleanTestKey(t)
	defer cleanTestKey(t)
	withTempLocalAppData(t)
	histMu.Lock()
	hist, histOK = nil, false
	histMu.Unlock()
	oldAdmin := app.admin
	app.admin = true
	defer func() { app.admin = oldAdmin }()
	buildTweaks()
	old := tweaks
	defer func() { tweaks = old }()
	user := sz(hkcu, testKey+`\H`, "Mode", "fast", "normal")
	if err := user.write("custom"); err != nil { // the user's own value
		t.Fatal(err)
	}
	tw := &tweak{id: "t-hist", group: "perf", reg: []rv{user, dw(hkcu, testKey+`\H\Made`, "Flag", 1, nil)}}
	tweaks = append(tweaks, tw)

	r := setTweak("t-hist", true, tweakOpts{})
	if !r.OK || r.State.State != "on" || !r.State.Applied {
		t.Fatalf("apply: %+v", r)
	}
	h := tweakHistory()
	if h.Undo == nil || h.Undo.Tweak != "t-hist" || h.Undo.Action != "apply" || !h.Items[0].OK {
		t.Fatalf("history: %+v", h)
	}
	// undo puts back exactly the earlier state: the user's value, no new key
	if r := undoTweak(0); !r.OK {
		t.Fatalf("undo: %+v", r)
	}
	if v, _ := user.read(); v != "custom" || keyExistsCU(testKey+`\H\Made`) || getBackup("t-hist") != nil {
		t.Fatalf("undo did not restore: %q", v)
	}
	h = tweakHistory()
	if h.Items[0].Action != "undo" || !h.Items[1].Undone {
		t.Fatalf("history after undo: %+v", h.Items[:2])
	}
	// on, off, then undo the "off": back on, with the original kept
	setTweak("t-hist", true, tweakOpts{})
	if r := setTweak("t-hist", false, tweakOpts{}); !r.OK || r.State.State != "off" {
		t.Fatalf("revert: %+v", r)
	}
	if v, _ := user.read(); v != "custom" {
		t.Fatalf("revert did not restore the user's value: %q", v)
	}
	if r := undoTweak(0); !r.OK || r.State.State != "on" || getBackup("t-hist") == nil {
		t.Fatalf("undo of revert: %+v", r)
	}
	// "undo all" restores the original of every applied tweak
	if ids := appliedTweaks(); len(ids) != 1 || ids[0] != "t-hist" {
		t.Fatalf("applied: %v", ids)
	}
	if res := undoAllTweaks([]string{"t-hist"}); len(res) != 1 || !res[0].OK {
		t.Fatalf("undo all: %+v", res)
	}
	if v, _ := user.read(); v != "custom" {
		t.Fatalf("undo all: %q", v)
	}
	// changed outside the app: shown as drift
	setTweak("t-hist", true, tweakOpts{})
	_ = user.write("someone-else")
	if st := fullState(tw, newProbe()); !st.Drift || st.State != "partial" {
		t.Fatalf("drift not seen: %+v", st)
	}
	setTweak("t-hist", false, tweakOpts{})
	// no saved original: off only after the user agreed to the defaults
	_ = user.write("fast")
	_ = dw(hkcu, testKey+`\H\Made`, "Flag", 1, nil).write(uint32(1))
	if r := setTweak("t-hist", false, tweakOpts{}); r.OK || !r.NoBackup {
		t.Fatalf("expected the no-backup question: %+v", r)
	}
	if r := setTweak("t-hist", false, tweakOpts{UseDefaults: true}); !r.OK {
		t.Fatalf("defaults: %+v", r)
	}
	if v, _ := user.read(); v != "normal" {
		t.Fatalf("default not written: %q", v)
	}
	// history survives a restart of the app
	histMu.Lock()
	hist, histOK = nil, false
	histMu.Unlock()
	if h := tweakHistory(); len(h.Items) < 8 {
		t.Fatalf("history not read back: %d", len(h.Items))
	}
	// a profile plan lists only real changes
	prof := tweakProfile{Kind: profileKind, Version: 1, Tweaks: map[string]bool{"t-hist": true, "no-such": true, "debloat": true}}
	b, _ := json.Marshal(prof)
	pf := filepath.Join(t.TempDir(), "p.json")
	_ = os.WriteFile(pf, b, 0o644)
	plan := planProfileFile(pf)
	if !plan.OK || len(plan.Steps) != 1 || plan.Steps[0].ID != "t-hist" || !plan.Steps[0].On || len(plan.Skipped) != 2 {
		t.Fatalf("plan: %+v", plan)
	}
	_ = os.WriteFile(pf, []byte(`{"kind":"other"}`), 0o644)
	if p := planProfileFile(pf); p.OK || !strings.Contains(p.Error, "xKi4o") {
		t.Fatalf("bad profile accepted: %+v", p)
	}
}

func TestEveryOwnedSettingHasOneTweak(t *testing.T) {
	buildTweaks()
	owner := map[string]string{}
	for _, tw := range tweaks {
		for _, o := range tw.owns {
			if p, ok := owner[o]; ok {
				t.Errorf("%s changed by %s and %s", o, p, tw.id)
			}
			owner[o] = tw.id
		}
		if tw.apply != nil && !tw.oneWay && tw.snap == nil && tw.revert == nil {
			t.Errorf("%s: has extra steps but no way back", tw.id)
		}
		if tw.snap != nil && tw.restore == nil {
			t.Errorf("%s: snapshot without restore", tw.id)
		}
		if tw.apply != nil && len(tw.owns) == 0 {
			t.Errorf("%s: extra steps without a declared setting", tw.id)
		}
	}
}

func TestStartupKeys(t *testing.T) {
	for key, want := range map[string][3]string{
		"reg|user|Discord":      {"reg", "user", "Discord"},
		"reg|machine32|A|B":     {"reg", "machine32", "A|B"},
		"folder|user|Steam.lnk": {"folder", "user", "Steam.lnk"},
		`task|\Vendor\Up|date`:  {"task", "machine", `\Vendor\Up|date`},
	} {
		s, sc, n, ok := parseKey(key)
		if !ok || s != want[0] || sc != want[1] || n != want[2] {
			t.Errorf("%s -> %s %s %s %v", key, s, sc, n, ok)
		}
	}
	if _, _, _, ok := parseKey("bogus"); ok {
		t.Error("bad key accepted")
	}
}
