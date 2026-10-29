//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestHelperHold is not a test: run as a child process it opens a file and
// keeps it open, like an app that uses its cache.
func TestHelperHold(t *testing.T) {
	path := os.Getenv("XKI4O_HOLD")
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(path+".ready", nil, 0o644)
	time.Sleep(60 * time.Second)
	f.Close()
	os.Exit(0)
}

func startHolder(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable() // a full path: started from its own folder, os.Args[0] is only "wtests.exe"
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "-test.run=^TestHelperHold$")
	cmd.Env = append(os.Environ(), "XKI4O_HOLD="+path)
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start a helper process:", err)
	}
	for i := 0; i < 100 && !exists(path+".ready"); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !exists(path + ".ready") {
		_ = cmd.Process.Kill()
		t.Fatal("helper did not open the file")
	}
	_ = os.Remove(path + ".ready")
	return cmd
}

// A file another program has open is skipped and reported as locked; once
// the program is gone the same run deletes it.
func TestLockedFileSkipped(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	mk(t, filepath.Join(cache, "free.bin"), 100, 0)
	mk(t, filepath.Join(cache, "used.bin"), 40, 0)

	// held open by this process, with every kind of sharing allowed
	p, _ := windows.UTF16PtrFromString(filepath.Join(cache, "used.bin"))
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if k := probeDelete(filepath.Join(cache, "used.bin")); k != "locked" {
		windows.CloseHandle(h)
		t.Skipf("this system does not report sharing conflicts (probe: %q)", k)
	}
	dry := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{dry: true})
	st := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{})
	windows.CloseHandle(h)
	if dry.files != 1 || dry.locked != 1 || dry.lockedBytes != 40 {
		t.Fatalf("dry run: %+v", dry)
	}
	if st.files != 1 || st.bytes != 100 || st.locked != 1 || st.lockedBytes != 40 {
		t.Fatalf("clean: %+v", st)
	}
	if len(st.issues) != 1 || st.issues[0].Kind != "locked" || !strings.HasSuffix(st.issues[0].Path, "used.bin") {
		t.Fatalf("issues: %+v", st.issues)
	}
	if !exists(filepath.Join(cache, "used.bin")) || exists(filepath.Join(cache, "free.bin")) {
		t.Fatal("wrong file deleted")
	}
	st = cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{})
	if st.files != 1 || exists(filepath.Join(cache, "used.bin")) {
		t.Fatalf("file not deleted after it was closed: %+v", st)
	}
}

func TestReadOnlyFileDeleted(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "Cache", "ro.bin")
	mk(t, f, 10, 0)
	if err := os.Chmod(f, 0o444); err != nil {
		t.Fatal(err)
	}
	st := cleanWith([]target{{Path: filepath.Dir(f), Kind: kContents}}, &cleaner{})
	if st.files != 1 || exists(f) {
		t.Fatalf("read-only file not deleted: %+v", st)
	}
}

// The whole blocker flow with a real second process: the check finds the
// app of the category, polite closing is tried, the forced close only runs
// when asked for, and a check after closing comes back clean.
func TestBlockerFlow(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "Cache")
	held := filepath.Join(cache, "held.bin")
	mk(t, held, 64, 0)
	mk(t, filepath.Join(cache, "other.bin"), 32, 0)
	child := startHolder(t, held)
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	defer func() { _ = child.Process.Kill() }()

	exe := strings.ToLower(filepath.Base(os.Args[0]))
	old := catalog
	defer func() { catalog = old }()
	catalog = []*item{
		{id: "t-files", targets: func() []target { return []target{{Path: cache, Kind: kContents}} }},
		{id: "t-app", procs: []string{exe}, targets: func() []target { return nil }},
	}

	// 1. files in use are found (who holds them needs the Restart Manager)
	rep := checkBlockers([]string{"t-files"})
	if li := rep.Locked["t-files"]; li.Files != 1 || li.Bytes != 64 {
		t.Fatalf("locked files not found: %+v", rep)
	}
	if len(rep.Apps) > 0 {
		a := rep.Apps[0]
		if a.Exe != exe || !a.Closable || a.Files != 1 || a.Running {
			t.Errorf("holder: %+v", a)
		}
	} else {
		t.Log("Restart Manager returned no holders here (expected under Wine)")
	}

	// 2. the category's own app is running
	rep = checkBlockers([]string{"t-app"})
	if len(rep.Apps) != 1 || !rep.Apps[0].Running || !rep.Apps[0].Closable || rep.Apps[0].Items[0] != "t-app" {
		t.Fatalf("running app not reported: %+v", rep.Apps)
	}
	for _, pid := range rep.Apps[0].PIDs {
		if pid == windows.GetCurrentProcessId() {
			t.Fatal("this app itself was listed")
		}
	}
	key := rep.Apps[0].Key

	// 3. polite closing: the helper has no window, so it stays open
	res := closeBlockers([]string{key}, false)
	if len(res) != 1 {
		t.Fatalf("close result: %+v", res)
	}
	// Windows' Restart Manager closes a console program politely too (Wine
	// does not): give its exit a moment to reach the test
	wait := time.After(0)
	if res[0].Closed {
		wait = time.After(5 * time.Second)
	}
	select {
	case <-done:
		t.Log("helper closed politely")
	case <-wait:
		if res[0].Closed || res[0].Note == "" {
			t.Fatalf("still running but reported closed: %+v", res[0])
		}
		// 4. forced close only on request
		res = forceCloseBlockers([]string{key})
		if len(res) != 1 || !res[0].Closed {
			t.Fatalf("forced close failed: %+v", res)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("helper still running")
		}
	}

	// 5. check again: nothing blocks, and the file is now free
	rep = checkBlockers([]string{"t-app", "t-files"})
	if len(rep.Apps) != 0 || rep.Locked["t-files"].Files != 0 {
		t.Fatalf("still blocked after closing: %+v", rep)
	}
	st := cleanWith([]target{{Path: cache, Kind: kContents}}, &cleaner{})
	if st.files != 2 || st.locked != 0 {
		t.Fatalf("clean after closing: %+v", st)
	}
}

// Windows and security processes are never offered for closing.
func TestNeverCloseLists(t *testing.T) {
	for _, n := range []string{"explorer.exe", "svchost.exe", "csrss.exe", "lsass.exe", "msedgewebview2.exe", "dwm.exe"} {
		if !windowsProcs[n] {
			t.Errorf("%s not protected", n)
		}
	}
	for _, n := range []string{"msmpeng.exe", "avp.exe", "ekrn.exe", "mbamservice.exe"} {
		if !securityProcs[n] {
			t.Errorf("%s not protected", n)
		}
	}
	// apps the catalog closes are not in those lists
	buildCatalog()
	for _, it := range catalog {
		for _, p := range it.procs {
			if windowsProcs[p] || securityProcs[p] {
				t.Errorf("%s: %s is protected but listed as the category's app", it.id, p)
			}
		}
	}
}

// Parts of Windows are never offered for closing before cleaning, whatever
// their file is called (the report from a real PC listed CrossDeviceResume
// and ShellHost as "apps" to close).
func TestWindowsComponentsAreNotClosable(t *testing.T) {
	for _, n := range []string{"Microsoft® Windows® Operating System", "Microsoft (R) Windows (R) Operating System", "microsoft windows\u00a0operating  system"} {
		if !isWindowsProduct(n) {
			t.Errorf("%q not seen as Windows", n)
		}
	}
	for _, n := range []string{"", "Viber", "Claude", "Microsoft Teams", "Logitech G HUB"} {
		if isWindowsProduct(n) {
			t.Errorf("%q seen as Windows", n)
		}
	}
	win := windowsDir()
	for _, c := range []struct {
		path, exe string
		want      bool
	}{
		{filepath.Join(win, `SystemApps\MicrosoftWindows.Client.CBS_cw5n1h2txyewy\CrossDeviceResume.exe`), "crossdeviceresume.exe", true},
		{filepath.Join(win, `System32\ShellHost.exe`), "shellhost.exe", true},
		{"", "crossdeviceresume.exe", true}, // path unreadable: the name still counts
		{filepath.Join(win, `System32\notepad.exe`), "notepad.exe", true},
		{`C:\Users\x\AppData\Local\Viber\Viber.exe`, "viber.exe", false},
		{"", "claude.exe", false},
	} {
		if got := windowsComponent(c.path, c.exe); got != c.want {
			t.Errorf("%s (%s): %v", c.path, c.exe, got)
		}
	}
	// this test program itself is not part of Windows
	self, _ := os.Executable()
	if windowsComponent(self, strings.ToLower(filepath.Base(self))) {
		t.Errorf("%s seen as Windows", self)
	}
}
