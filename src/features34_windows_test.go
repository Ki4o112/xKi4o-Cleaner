//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
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
