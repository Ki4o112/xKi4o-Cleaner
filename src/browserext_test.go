package main

import (
	"os"
	"path/filepath"
	"testing"
)

func wr(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanChromium(t *testing.T) {
	ud := t.TempDir()
	a, b, c, th := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddd"
	wr(t, filepath.Join(ud, "Local State"), `{"profile":{"info_cache":{"Default":{"name":"Work"}}}}`)
	// an ad blocker from the store: every site + network rules -> high
	wr(t, filepath.Join(ud, "Default/Extensions", a, "1.9.0_0/manifest.json"), `{"name":"Old","version":"1.9.0"}`)
	wr(t, filepath.Join(ud, "Default/Extensions", a, "1.10.0_0/manifest.json"),
		"\xef\xbb\xbf"+`{"name":"__MSG_n__","default_locale":"en","version":"1.10.0","permissions":["storage","declarativeNetRequest"],"host_permissions":["<all_urls>"]}`)
	wr(t, filepath.Join(ud, "Default/Extensions", a, "1.10.0_0/_locales/en/messages.json"), `{"N":{"message":"Blocker"}}`)
	// loaded by hand, reads every site -> high even with no other rights
	wr(t, filepath.Join(ud, "Default/Extensions", b, "2.0_0/manifest.json"), `{"name":"Unpacked","version":"2.0","content_scripts":[{"matches":["https://*/*"]}]}`)
	// small, from the store, turned off
	wr(t, filepath.Join(ud, "Default/Extensions", c, "3.0_0/manifest.json"), `{"name":"Notes","version":"3.0","permissions":["storage"]}`)
	// a theme: skipped
	wr(t, filepath.Join(ud, "Default/Extensions", th, "1_0/manifest.json"), `{"name":"Dark","version":"1","theme":{}}`)
	wr(t, filepath.Join(ud, "Default/Secure Preferences"), `{"extensions":{"settings":{
		"`+a+`":{"state":1,"from_webstore":true,"location":1},
		"`+b+`":{"state":1,"location":4},
		"`+c+`":{"disable_reasons":[1],"location":1}}}}`)
	xs := scanChromium("chrome", ud, false)
	sortExts(xs)
	if len(xs) != 3 {
		t.Fatalf("%d: %+v", len(xs), xs)
	}
	by := map[string]browserExt{}
	for _, x := range xs {
		by[x.ID] = x
	}
	if x := by[a]; x.Name != "Blocker" || x.Version != "1.10.0" || !x.AllSites || x.Risk != "high" || !x.Store || !x.Enabled || x.Profile != "Work" {
		t.Errorf("a: %+v", x)
	}
	if x := by[b]; x.Store || !x.AllSites || x.Risk != "high" {
		t.Errorf("b: %+v", x)
	}
	if x := by[c]; x.Enabled || x.Risk != "low" {
		t.Errorf("c: %+v", x)
	}
	if xs[2].ID != c {
		t.Errorf("low risk last: %v", xs[2].ID)
	}
}

func TestFirefoxAddons(t *testing.T) {
	js := `{"addons":[
	 {"id":"ub@x","type":"extension","version":"1.6","active":true,"location":"app-profile","signedState":2,
	  "defaultLocale":{"name":"uBlock"},"userPermissions":{"permissions":["webRequest","storage"],"origins":["<all_urls>"]}},
	 {"id":"sys@mozilla","type":"extension","active":true,"location":"app-system-defaults","defaultLocale":{"name":"System"}},
	 {"id":"dict","type":"dictionary","location":"app-profile"},
	 {"id":"tmp@x","type":"extension","active":false,"userDisabled":true,"location":"app-temporary","defaultLocale":{"name":"Dev"}}]}`
	xs := parseFirefoxAddons([]byte(js), "default")
	if len(xs) != 2 || xs[0].Name != "uBlock" || xs[0].Risk != "high" || !xs[0].Store || xs[1].Store || xs[1].Enabled {
		t.Fatalf("%+v", xs)
	}
	if parseFirefoxAddons([]byte("nope"), "x") != nil {
		t.Fatal("bad json")
	}
}

func TestExtRisk(t *testing.T) {
	for _, c := range []struct {
		perms []string
		store bool
		want  string
	}{
		{[]string{"storage"}, true, "low"},
		{[]string{"tabs", "history", "bookmarks"}, true, "medium"},
		{[]string{"*://*/*"}, true, "medium"},
		{[]string{"*://*/*", "cookies"}, true, "high"},
		{[]string{"nativeMessaging"}, false, "high"},
		{[]string{"https://example.com/*"}, true, "low"},
	} {
		x := browserExt{Store: c.store}
		extRisk(&x, c.perms)
		if x.Risk != c.want {
			t.Errorf("%v store=%v: %s", c.perms, c.store, x.Risk)
		}
	}
}
