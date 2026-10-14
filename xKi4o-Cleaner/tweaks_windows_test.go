//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// The tests work below this key and remove it afterwards.
const testKey = `Software\xKi4oCleanerTest`

func cleanTestKey(t *testing.T) {
	t.Helper()
	var del func(path string)
	del = func(path string) {
		k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			return
		}
		subs, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, s := range subs {
			del(path + `\` + s)
		}
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
	}
	del(testKey)
}

func keyExistsCU(path string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func withTempLocalAppData(t *testing.T) {
	t.Helper()
	old := os.Getenv("LOCALAPPDATA")
	dir := t.TempDir()
	os.Setenv("LOCALAPPDATA", dir)
	backupMu.Lock()
	backups = nil
	backupMu.Unlock()
	t.Cleanup(func() {
		os.Setenv("LOCALAPPDATA", old)
		backupMu.Lock()
		backups = nil
		backupMu.Unlock()
	})
}

func TestRegValues(t *testing.T) {
	cleanTestKey(t)
	defer cleanTestKey(t)

	d := dw(hkcu, testKey+`\A`, "Dw", 0, uint32(1))
	s := sz(hkcu, testKey+`\A`, "Sz", "0", "400")
	q := rv{root: hkcu, path: testKey + `\A`, name: "Qw", kind: registry.QWORD, on: uint64(1)}
	def := rv{root: hkcu, path: testKey + `\B\C`, name: "", kind: registry.SZ, on: "", keys: []string{testKey + `\B\C`, testKey + `\B`}}
	absent := rv{root: hkcu, path: testKey + `\A`, name: "Missing", kind: registry.DWORD, on: uint32(1), absentOn: true}

	for _, v := range []rv{d, s, q, def} {
		if v.isOn() {
			t.Fatalf("%s on before writing", v.name)
		}
		if err := v.write(v.on); err != nil {
			t.Fatalf("write %s: %v", v.name, err)
		}
		if !v.isOn() {
			got, ok := v.read()
			t.Fatalf("%s not on after writing (got %q %v)", v.name, got, ok)
		}
	}
	if !absent.isOn() {
		t.Fatal("absentOn value not on")
	}
	// default value "" exists -> on; deleting it and the keys -> off and gone
	if err := def.write(nil); err != nil {
		t.Fatal(err)
	}
	dropEmptyKeys(hkcu, def.keys)
	if def.isOn() || keyExistsCU(testKey+`\B`) {
		t.Fatal("default value or keys left behind")
	}
	// a DWORD written as text compares by value
	str := sz(hkcu, testKey+`\A`, "Dw", "0", nil)
	if !str.isOn() {
		t.Fatal("DWORD 0 does not match text 0")
	}
}

func TestSnapshotRestore(t *testing.T) {
	cleanTestKey(t)
	defer cleanTestKey(t)

	existing := dw(hkcu, testKey+`\X`, "Keep", 0, nil)
	if err := existing.write(uint32(7)); err != nil {
		t.Fatal(err)
	}
	created := dw(hkcu, testKey+`\Y\Z`, "New", 1, nil)

	s1, s2 := snapReg(existing), snapReg(created)
	if !s1.Exists || s1.Int != 7 || s2.Exists {
		t.Fatalf("snapshots wrong: %+v %+v", s1, s2)
	}
	if len(s2.Created) != 2 || s2.Created[0] != testKey+`\Y` || s2.Created[1] != testKey+`\Y\Z` {
		t.Fatalf("created keys: %v", s2.Created)
	}
	_ = existing.write(existing.on)
	_ = created.write(created.on)

	if err := restoreReg(s1); err != nil {
		t.Fatal(err)
	}
	if err := restoreReg(s2); err != nil {
		t.Fatal(err)
	}
	if v, _ := existing.read(); v != "7" {
		t.Fatalf("existing value not restored: %q", v)
	}
	if keyExistsCU(testKey + `\Y`) {
		t.Fatal("created keys not removed")
	}
}

func TestApplyRevertWithBackup(t *testing.T) {
	cleanTestKey(t)
	defer cleanTestKey(t)
	withTempLocalAppData(t)

	pre := sz(hkcu, testKey+`\T`, "Delay", "0", "400")
	if err := pre.write("250"); err != nil { // the user's own value
		t.Fatal(err)
	}
	tw := &tweak{id: "test-tweak", reg: []rv{pre, dw(hkcu, testKey+`\T\New`, "Flag", 1, nil)}}
	if st := tw.regState(); st != "off" {
		t.Fatalf("state before: %s", st)
	}
	if err := applyTweak(tw, nil); err != nil {
		t.Fatal(err)
	}
	if st := tw.regState(); st != "on" {
		t.Fatalf("state after apply: %s", st)
	}
	if getBackup("test-tweak") == nil {
		t.Fatal("no backup saved")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", "tweaks-backup.json")); err != nil {
		t.Fatal("backup file missing:", err)
	}
	// applying again keeps the original backup
	if err := applyTweak(tw, nil); err != nil {
		t.Fatal(err)
	}
	if err := revertTweak(tw, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := pre.read(); v != "250" {
		t.Fatalf("user's value not restored: %q", v)
	}
	if keyExistsCU(testKey + `\T\New`) {
		t.Fatal("key created by the tweak was left")
	}
	if getBackup("test-tweak") != nil {
		t.Fatal("backup not removed after revert")
	}
	// without a backup, revert writes the defaults
	if err := applyTweak(tw, nil); err != nil {
		t.Fatal(err)
	}
	putBackup("test-tweak", nil)
	if err := revertTweak(tw, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := pre.read(); v != "400" {
		t.Fatalf("default not written: %q", v)
	}
}

func TestCatalogue(t *testing.T) {
	buildTweaks()
	seen := map[string]bool{}
	groups := map[string]bool{"perf": true, "gaming": true, "privacy": true, "look": true, "explorer": true, "system": true, "network": true, "apps": true}
	for _, tw := range tweaks {
		if seen[tw.id] {
			t.Errorf("duplicate tweak %s", tw.id)
		}
		seen[tw.id] = true
		if !groups[tw.group] {
			t.Errorf("%s: unknown group %q", tw.id, tw.group)
		}
		if len(tw.reg) == 0 && len(tw.svc) == 0 && tw.detect == nil {
			t.Errorf("%s: no way to detect its state", tw.id)
		}
		if tw.oneWay && tw.revert != nil {
			t.Errorf("%s: one-way tweak with a revert step", tw.id)
		}
		for _, v := range tw.reg {
			if v.on == nil {
				t.Errorf("%s: %s has no value", tw.id, v.name)
			}
			if v.root == hkcu || v.root == hku {
				continue
			}
			if !tw.sys {
				t.Errorf("%s: changes HKLM but is not marked as a system change", tw.id)
			}
		}
		if len(tw.svc) > 0 && !tw.sys {
			t.Errorf("%s: changes services but is not marked as a system change", tw.id)
		}
	}
	if len(tweaks) < 50 {
		t.Errorf("only %d tweaks", len(tweaks))
	}
	// every Windows service the tweaks touch appears in one tweak only
	svcOwner := map[string]string{}
	for _, tw := range tweaks {
		for _, s := range tw.svc {
			if o, ok := svcOwner[s.name]; ok {
				t.Errorf("service %s in %s and %s", s.name, o, tw.id)
			}
			svcOwner[s.name] = tw.id
		}
	}
	// and so does every registry value
	regOwner := map[string]string{}
	for _, tw := range tweaks {
		for _, v := range tw.reg {
			k := string(rune('0'+v.root)) + v.path + "|" + v.name
			if o, ok := regOwner[k]; ok {
				t.Errorf("value %s\\%s in %s and %s", v.path, v.name, o, tw.id)
			}
			regOwner[k] = tw.id
		}
	}
}

func TestStatesDoNotPanic(t *testing.T) {
	buildTweaks()
	p := newProbe()
	for _, tw := range tweaks {
		if tw.slow {
			continue // needs PowerShell
		}
		st := tw.state(p)
		switch st.State {
		case "on", "off", "partial", "na":
		default:
			t.Errorf("%s: state %q (%s)", tw.id, st.State, st.Note)
		}
	}
}

func TestCloseProcesses(t *testing.T) {
	exe := filepath.Join(os.Getenv("SystemRoot"), "notepad.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skip("notepad not available")
	}
	cmd := exec.Command(exe)
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start notepad:", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	time.Sleep(1500 * time.Millisecond)
	if len(procPIDs(map[string]bool{"notepad.exe": true})) == 0 {
		_ = cmd.Process.Kill()
		t.Skip("notepad.exe started another process (Windows 11 Store Notepad)")
	}
	closeProcesses(map[string]bool{"notepad.exe": true}, 3*time.Second)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("notepad still running")
	}
}

func TestServiceRead(t *testing.T) {
	buildTweaks()
	// Reading the start type of a service must work without changing it.
	m, err := mgr.Connect()
	if err != nil {
		t.Skip("no service manager:", err)
	}
	defer m.Disconnect()
	found := false
	for _, name := range []string{"PlugPlay", "RpcSs", "EventLog", "Winmgmt", "Spooler", "DiagTrack"} {
		if st, _, ok := svcStart(m, name); ok {
			found = true
			if st > 4 {
				t.Errorf("%s: start type %d", name, st)
			}
		}
	}
	if !found {
		t.Skip("none of the probe services exist here")
	}
}
