//go:build windows

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// System information: read only. Under Wine (no PowerShell) the basic
// part from the registry still fills the page.
func TestSystemInfoRead(t *testing.T) {
	initPaths()
	start := time.Now()
	si := systemInfo(true)
	t.Logf("read in %v (error %q)", time.Since(start).Round(time.Millisecond), si.Error)
	t.Logf("OS: %s %s build %s.%d, %s, up %ds, PC %q", si.OS.Name, si.OS.Ver, si.OS.Build, si.OS.UBR, si.OS.Arch, si.OS.Uptime, si.OS.PC)
	for _, c := range si.CPU {
		t.Logf("CPU: %s, %d cores / %d threads, %d MHz, L2 %d KB, L3 %d KB, %s, virtualization %v", c.Name, c.Cores, c.Threads, c.MHz, c.L2, c.L3, c.Socket, c.Virt)
	}
	for _, g := range si.GPU {
		t.Logf("GPU: %s, %d MB, driver %s (%s), %dx%d %d Hz, virtual %v", g.Name, g.RAM>>20, g.Driver, g.Date, g.W, g.H, g.Hz, g.Virtual)
	}
	for _, r := range si.RAM {
		t.Logf("RAM: %s %d MB %s %d/%d MHz %s %s", r.Slot, r.Size>>20, r.Type, r.MHz, r.Cfg, r.Maker, r.Part)
	}
	t.Logf("slots %d; board %s %s; PC %s %s laptop=%v; BIOS %s %s %s; total %d MB", si.Slots, si.Board.Maker, si.Board.Model, si.Board.PCMaker, si.Board.PCModel, si.Board.Laptop,
		si.Board.BIOSMaker, si.Board.BIOS, si.Board.BIOSDate, si.Board.Total>>20)
	t.Logf("security: %+v", si.Security)
	t.Logf("monitors %+v; audio %v; net %+v; disks %+v; battery %+v", si.Monitors, si.Audio, si.Net, si.Disks, si.Battery)
	if si.OS.Build == "" || si.OS.Name == "" {
		t.Errorf("no Windows version: %+v", si.OS)
	}
	if len(si.CPU) == 0 || si.CPU[0].Name == "" {
		t.Errorf("no processor: %+v", si.CPU)
	}
	if si.Board.Total <= 0 && len(si.RAM) == 0 {
		t.Error("no memory size")
	}
	if strings.Contains(si.OS.Name, "Microsoft ") {
		t.Errorf("name not tidied: %q", si.OS.Name)
	}
	// the second call is the kept copy (fast), with a fresh uptime
	t2 := time.Now()
	again := systemInfo(false)
	if time.Since(t2) > time.Second || again.OS.Build != si.OS.Build {
		t.Errorf("the kept copy took %v", time.Since(t2))
	}
}

// The network page reads the connections and tests DNS speed (questions
// to public DNS servers; nothing is changed).
func TestNetStateAndDNSTest(t *testing.T) {
	initPaths()
	st := getNetState()
	t.Logf("current %q, backup %v", st.Current, st.Backup)
	for _, a := range st.Adapters {
		t.Logf("  #%d %s (%s) wifi=%v gateway=%v speed=%d dns=%v static=%v provider=%q", a.Index, a.Name, a.Desc, a.WiFi, a.Gateway, a.Speed, a.DNS, a.Static, a.Provider)
		if a.GUID == "" || a.Index == 0 {
			t.Errorf("adapter without id: %+v", a)
		}
	}
	if len(st.Providers) != len(dnsProviders) {
		t.Error("providers")
	}
	start := time.Now()
	res := dnsTest()
	t.Logf("DNS test in %v:", time.Since(start).Round(time.Millisecond))
	for _, r := range res {
		t.Logf("  %-16s %-18s %4d ms lost %d/%d", r.Server, r.ID, r.Ms, r.Lost, r.Of)
	}
	if len(res) < len(dnsProviders) {
		t.Errorf("%d results", len(res))
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("the test took %v", time.Since(start))
	}
}

// Switching DNS and back. Changes the network settings: under Wine only.
func TestDNSSetRestore(t *testing.T) {
	changesThisPC(t)
	initPaths()
	use := inUse(listAdapters())
	if len(use) == 0 {
		t.Skip("no connection here")
	}
	before := staticDNS(tcpip4Key, use[0].GUID)
	r := dnsSet("cloudflare")
	t.Logf("set: %+v", r)
	if !r.OK {
		t.Skipf("netsh does not work here: %s", r.Error)
	}
	if got := staticDNS(tcpip4Key, use[0].GUID); got != "1.1.1.1,1.0.0.1" {
		t.Errorf("IPv4 servers now %q", got)
	}
	if !r.State.Backup {
		t.Error("no backup kept")
	}
	r = dnsSet("google") // the first backup stays: Restore goes back to before Cloudflare
	if !r.OK {
		t.Fatal(r.Error)
	}
	r = dnsRestore()
	if !r.OK || r.State.Backup {
		t.Errorf("restore: %+v", r)
	}
	if got := staticDNS(tcpip4Key, use[0].GUID); got != before {
		t.Errorf("after restore %q, before %q", got, before)
	}
}

func setTestKey(t *testing.T, root registry.Key, path string, vals map[string]string) {
	t.Helper()
	k, _, err := registry.CreateKey(root, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(path, err)
	}
	for n, v := range vals {
		if err := k.SetStringValue(n, v); err != nil {
			t.Fatal(err)
		}
	}
	k.Close()
}

// The right-click menu: a test program's verb and shell extension are
// listed (a Windows verb is not), hidden and shown again. Under Wine only;
// on a PC the list is only read.
func TestCtxMenu(t *testing.T) {
	initPaths()
	if !underWine() && os.Getenv("XKI4O_WRITE_TESTS") != "1" {
		items := ctxMenuList()
		t.Logf("%d entries of other programs:", len(items))
		for _, it := range items {
			t.Logf("  [%s] %q %v %s off=%v missing=%v shift=%v %s", it.Kind, it.Name, it.Where, it.Program, it.Off, it.Missing, it.Shift, it.CLSID)
			if windowsOwn(it.Program, P.windir) {
				t.Errorf("a Windows entry is offered: %+v", it)
			}
		}
		return
	}
	const clsid = "{0D7A5E3C-1B2F-4C5D-9E8F-A1B2C3D4E5F6}"
	paths := []string{`Software\Classes\*\shell\xKi4oTestVerb\command`, `Software\Classes\*\shell\xKi4oTestVerb`, `Software\Classes\*\shell\xKi4oWinVerb\command`, `Software\Classes\*\shell\xKi4oWinVerb`,
		`Software\Classes\CLSID\` + clsid + `\InprocServer32`, `Software\Classes\CLSID\` + clsid, `Software\Classes\Directory\shellex\ContextMenuHandlers\xKi4oTest`}
	cleanup := func() {
		for _, p := range paths {
			_ = registry.DeleteKey(registry.CURRENT_USER, p)
		}
		_ = setBlocked(clsid, "", false)
	}
	cleanup()
	t.Cleanup(cleanup)
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\*\shell\xKi4oTestVerb`, map[string]string{"MUIVerb": "xKi4o &Test", "Icon": `"C:\xKi4oTest\app.exe",0`})
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\*\shell\xKi4oTestVerb\command`, map[string]string{"": `"C:\xKi4oTest\app.exe" "%1"`})
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\*\shell\xKi4oWinVerb`, map[string]string{"": "Windows thing"})
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\*\shell\xKi4oWinVerb\command`, map[string]string{"": `%SystemRoot%\system32\notepad.exe "%1"`})
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\CLSID\`+clsid, map[string]string{"": "xKi4o test extension"})
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\CLSID\`+clsid+`\InprocServer32`, map[string]string{"": `C:\xKi4oTest\menu.dll`})
	setTestKey(t, registry.CURRENT_USER, `Software\Classes\Directory\shellex\ContextMenuHandlers\xKi4oTest`, map[string]string{"": clsid})

	find := func(items []ctxItem) (verb, ext *ctxItem) {
		for i := range items {
			switch {
			case items[i].Name == "xKi4o Test":
				verb = &items[i]
			case items[i].CLSID == clsid:
				ext = &items[i]
			case strings.Contains(items[i].Name, "Windows thing"):
				t.Errorf("a Windows program's verb is offered: %+v", items[i])
			}
		}
		return
	}
	verb, ext := find(ctxMenuList())
	if verb == nil || ext == nil {
		t.Fatalf("not listed: verb %v ext %v", verb, ext)
	}
	if !verb.Missing || verb.Off || verb.Program != `C:\xKi4oTest\app.exe` || verb.Icon != `C:\xKi4oTest\app.exe` || strings.Join(verb.Where, ",") != "files" {
		t.Errorf("verb %+v", *verb)
	}
	if ext.Name != "xKi4oTest" || ext.Program != `C:\xKi4oTest\menu.dll` || strings.Join(ext.Where, ",") != "folders" {
		t.Errorf("ext %+v", *ext)
	}
	r := ctxMenuSet([]string{verb.ID, ext.ID}, true)
	if r.Done != 2 || len(r.Failed) != 0 || !r.Explorer {
		t.Fatalf("hide: %+v", r)
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\*\shell\xKi4oTestVerb`, registry.QUERY_VALUE); err == nil {
		if _, _, err := k.GetStringValue("LegacyDisable"); err != nil {
			t.Error("no LegacyDisable on the hidden verb")
		}
		k.Close()
	}
	if !blockedCLSIDs()[clsid] {
		t.Error("the extension is not on the blocked list")
	}
	verb, ext = find(r.Items)
	if verb == nil || ext == nil || !verb.Off || !ext.Off {
		t.Fatalf("after hiding: %v %v", verb, ext)
	}
	r = ctxMenuSet([]string{verb.ID, ext.ID}, false)
	verb, ext = find(r.Items)
	if r.Done != 2 || verb == nil || ext == nil || verb.Off || ext.Off || blockedCLSIDs()[clsid] {
		t.Errorf("shown again: %+v %v %v", r, verb, ext)
	}
	if r := ctxMenuSet([]string{"nope"}, true); r.Done != 0 {
		t.Error("an unknown id did something")
	}
}

// Every category automatic cleaning may touch is in the catalog and needs
// no restore point.
func TestAutoCleanCategories(t *testing.T) {
	initPaths()
	buildCatalog()
	for _, id := range autoCleanIDs {
		it := findItem(id)
		if it == nil {
			t.Errorf("%s: not in the catalog", id)
			continue
		}
		if it.deep {
			t.Errorf("%s needs a restore point: not for automatic cleaning", id)
		}
	}
	st := autoCleanState()
	t.Logf("automatic cleaning: %+v", st)
}

// The scheduled task: made for this program, read back, removed. Under
// Wine only (it changes Task Scheduler).
func TestAutoCleanTask(t *testing.T) {
	changesThisPC(t)
	initPaths()
	app.admin = isElevated()
	st := autoCleanSet("weekly")
	t.Logf("weekly: %+v", st)
	if st.Error != "" {
		t.Skipf("Task Scheduler does not work here: %s", st.Error)
	}
	if st.Freq != "weekly" {
		t.Errorf("freq %q", st.Freq)
	}
	if x := readTaskXML(); taskCommand(x) == "" {
		t.Logf("the stored task has no command (Wine's Task Scheduler keeps only part of it):\n%s", x)
	}
	if st.Task && st.Moved {
		exe, _ := os.Executable()
		x := readTaskXML()
		t.Errorf("the task starts another program: %q, this is %q\n%s", taskCommand(x), exe, x)
	}
	st = autoCleanSet("off")
	if st.Error != "" || st.Freq != "off" || st.Task {
		t.Errorf("off: %+v", st)
	}
	if st := autoCleanSet("hourly"); st.Error == "" {
		t.Error("hourly accepted")
	}
}

// One automatic run without the window: it cleans, writes how it went, and
// a second run at the same time is refused. Under Wine only.
func TestRunAutoClean(t *testing.T) {
	changesThisPC(t)
	initPaths()
	buildCatalog()
	app.admin = isElevated()
	runAutoClean("test")
	st := autoCleanState()
	if st.Last == nil || time.Since(time.Unix(st.Last.At, 0)) > time.Minute {
		t.Fatalf("no record of the run: %+v", st.Last)
	}
	t.Logf("last run: %+v", *st.Last)
	app.busy.Store(true)
	runAutoClean("test")
	app.busy.Store(false)
	if st := autoCleanState(); st.Last == nil || st.Last.State != "busy" {
		t.Errorf("a run while busy: %+v", st.Last)
	}
}

// The page saves its copy of the settings whole; what the backend keeps
// there (the DNS backup, the schedule) must survive that. A temporary
// folder stands in for the app's data folder.
func TestPageSaveKeepsBackendSettings(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if !updateSettings(func(m map[string]any) {
		m["dnsBackup"] = map[string]any{"{x}": map[string]any{"name": "Wi-Fi", "v4": "192.168.1.2", "v6": ""}}
		m["autoClean"] = "weekly"
		m["lang"] = "bg"
	}) {
		t.Fatal("write")
	}
	page := getSettings()
	delete(page, "dnsBackup")       // an old copy of the page, from before the change
	page["autoClean"] = "daily"     // or one that tries to change what it does not own
	page["autoCleanLast"] = "bogus" // nor add it
	page["lang"] = "en"
	if !savePageSettings(page) {
		t.Fatal("page save")
	}
	m := getSettings()
	if b, _ := m["dnsBackup"].(map[string]any); len(b) != 1 {
		t.Errorf("the DNS backup was lost: %v", m["dnsBackup"])
	}
	if m["autoClean"] != "weekly" || m["autoCleanLast"] != nil {
		t.Errorf("schedule %v / %v", m["autoClean"], m["autoCleanLast"])
	}
	if m["lang"] != "en" {
		t.Errorf("the page's own setting was not saved: %v", m["lang"])
	}
}
