//go:build windows

package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The connection check only sends pings (read only, also on a real PC).
func TestNetPing(t *testing.T) {
	rs := netPing()
	if len(rs) != 3 {
		t.Fatalf("%d results", len(rs))
	}
	for _, r := range rs {
		t.Logf("%s %s: sent %d lost %d avg %.1f ms jitter %.1f ms min %.0f max %.0f %s", r.ID, r.Host, r.Sent, r.Lost, r.Avg, r.Jitter, r.Min, r.Max, r.Error)
		if r.Error == "" && r.Sent != 12 {
			t.Errorf("%s: %d pings sent", r.ID, r.Sent)
		}
		if r.Lost > r.Sent || r.Loss < 0 || r.Loss > 100 {
			t.Errorf("%s: counts %+v", r.ID, r)
		}
	}
}

// An unknown fix is refused before anything runs; the icon cache list has
// only Explorer's icon cache files. Nothing here changes the PC.
func TestQuickFixSafe(t *testing.T) {
	if r := quickFix("format-c"); r.OK || r.Error == "" {
		t.Errorf("unknown fix: %+v", r)
	}
	for _, f := range iconCacheFiles() {
		b := strings.ToLower(filepath.Base(f))
		if ok := b == "iconcache.db" || strings.HasPrefix(b, "iconcache_") && strings.HasSuffix(b, ".db"); !ok {
			t.Errorf("not an icon cache file: %s", f)
		}
	}
	t.Logf("icon cache files: %d", len(iconCacheFiles()))
}

// The new speed tweaks of 3.4: each one goes back to what Windows had
// (written only under Wine or with XKI4O_WRITE_TESTS=1).
func TestTweaks34(t *testing.T) {
	buildTweaks()
	want := []string{"paging-exec-off", "ntfs-lastaccess-off", "icon-cache-large", "hover-fast", "keyboard-fast", "gpu-tdr-longer", "timer-res-global"}
	by := map[string]*tweak{}
	for _, tw := range tweaks {
		by[tw.id] = tw
	}
	for _, id := range want {
		tw := by[id]
		if tw == nil {
			t.Errorf("no tweak %s", id)
			continue
		}
		if tw.group != "perf" || tw.since != "3.4.0" || len(tw.reg) == 0 || presetNever[id] {
			t.Errorf("%s: group %s since %s", id, tw.group, tw.since)
		}
	}
}

// nmUnzip takes only the known net-merge files and never writes outside its
// folder (no sub-folders, no "../" escape).
func TestNMUnzipSafe(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "p.zip")
	f, err := os.Create(zp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := []string{"net-merge.exe", "net-merge-tray.exe", "wintun.dll", "wintun-LICENSE.txt",
		"LICENSE", "THIRD-PARTY-NOTICES.md", "README.md", "sub/evil.exe", "../escape.exe", "setup.ps1"}
	for _, n := range entries {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("PK stub " + n))
	}
	zw.Close()
	f.Close()

	out := t.TempDir()
	n, err := nmUnzip(zp, out)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("extracted %d files, want 7", n)
	}
	if _, err := os.Stat(filepath.Join(out, "net-merge.exe")); err != nil {
		t.Errorf("net-merge.exe not extracted: %v", err)
	}
	for _, bad := range []string{"evil.exe", "escape.exe", "setup.ps1"} {
		if _, err := os.Stat(filepath.Join(out, bad)); err == nil {
			t.Errorf("%s should not have been written", bad)
		}
	}
	// nothing escaped the output folder
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.exe")); err == nil {
		t.Error("a file escaped the output folder")
	}
}

// How connections are shared: the flag the app passes and how it is read back.
func TestNMBalanceHelpers(t *testing.T) {
	if got := strings.Join(nmTrayArgs("auto", false), " "); got != "tray" {
		t.Fatalf("auto: %q", got)
	}
	if got := strings.Join(nmTrayArgs("even", true), " "); got != "tray -auto-weight=false -off" {
		t.Fatalf("even off: %q", got)
	}
	for cl, want := range map[string]bool{
		`"C:\x\net-merge-tray.exe" tray`:                    false,
		`"C:\x\net-merge-tray.exe" tray -auto-weight=false`: true,
		`net-merge.exe tray --auto-weight=false -off`:       true,
		`net-merge.exe tray -Auto-Weight=FALSE`:             true,
		`net-merge.exe tray -auto-weight=true`:              false,
	} {
		if nmEvenArgs(cl) != want {
			t.Errorf("%s: want %v", cl, want)
		}
	}
	// Ethernet measured about 20x faster: it gets ~95% of new connections; an
	// adapter whose check has not passed gets none while another one is up.
	nics := []nmNIC{
		{Name: "Ethernet", IP: "192.168.1.81", State: "up", Present: true, Weight: 1},
		{Name: "Wi-Fi", IP: "172.20.10.10", State: "up", Present: true, Weight: 0.05},
		{Name: "USB", IP: "192.168.42.6", State: "checking", Present: true, Weight: 1},
	}
	ads := make([]nmAdapter, len(nics))
	nmShares(ads, nics)
	if ads[0].Share < 95 || ads[0].Share > 95.3 || ads[1].Share < 4.7 || ads[1].Share > 5 || ads[2].Share != 0 {
		t.Fatalf("shares: %.2f %.2f %.2f", ads[0].Share, ads[1].Share, ads[2].Share)
	}
	// nothing checked yet: everyone shares by weight
	nics[0].State, nics[1].State = "checking", "checking"
	nmShares(ads, nics)
	if ads[2].Share == 0 {
		t.Fatalf("unchecked adapter got no share: %+v", ads)
	}
	// net-merge is never closed by game mode
	for _, e := range []string{"net-merge.exe", "net-merge-tray.exe"} {
		if !gmSkip(e, `C:\Users\x\AppData\Local\xKi4oCleaner\net-merge\`+e) {
			t.Errorf("%s can be closed by game mode", e)
		}
	}
}

// What net-merge reports on this PC right now (read only: nothing is started,
// stopped or changed). Shows each adapter's share, measured speed and traffic.
func TestNMLiveStatus(t *testing.T) {
	st := nmState()
	t.Logf("installed=%v path=%q running=%v on=%v balance(saved)=%s running-with=%q", st.Installed, st.Path, st.Running, st.On, st.Balance, st.RunBalance)
	for pid := range procPIDs(map[string]bool{"net-merge.exe": true, "net-merge-tray.exe": true}) {
		t.Logf("  process %d: %s", pid, processCommandLine(pid))
	}
	if b, err := os.ReadFile(nmStateFile()); err == nil {
		var f nmFile
		_ = json.Unmarshal(b, &f)
		t.Logf("status.json: updated %s ago, mode %q, ipv6 %q", time.Since(f.Updated).Round(time.Millisecond), f.Mode, f.V6)
		for _, n := range f.NICs {
			t.Logf("  %-12s %-15s state=%-11s present=%v weight=%.3f peak=%.1f Mbit/s flows %d/%d via-nm ↓%.1f ↑%.1f all-apps ↓%.1f ↑%.1f Mbit/s",
				n.Name, n.IP, n.State, n.Present, n.Weight, n.Peak*8/1e6, n.Active, n.Total, n.RateIn*8/1e6, n.RateOut*8/1e6, n.LinkIn*8/1e6, n.LinkOut*8/1e6)
		}
	} else {
		t.Logf("status.json: %v (merging is off or net-merge is not running)", err)
	}
	for _, a := range st.Adapters {
		t.Logf("  share %-12s %.1f%%", a.Name, a.Share)
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(nmStateFile()), "net-merge.log")); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		t.Logf("net-merge.log (last %d lines):\n%s", len(lines), strings.Join(lines, "\n"))
	}
}
