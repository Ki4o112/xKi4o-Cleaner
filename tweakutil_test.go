package main

import (
	"reflect"
	"testing"
)

func TestDXSettings(t *testing.T) {
	cases := []struct{ in, key, val, want string }{
		{"", "SwapEffectUpgradeEnable", "1", "SwapEffectUpgradeEnable=1;"},
		{"VRROptimizeEnable=0;", "SwapEffectUpgradeEnable", "1", "VRROptimizeEnable=0;SwapEffectUpgradeEnable=1;"},
		{"SwapEffectUpgradeEnable=0;AutoHDREnable=1;", "SwapEffectUpgradeEnable", "1", "SwapEffectUpgradeEnable=1;AutoHDREnable=1;"},
		{" a=1 ; ;b=2", "c", "3", "a=1;b=2;c=3;"},
	}
	for _, c := range cases {
		if got := dxSet(c.in, c.key, c.val); got != c.want {
			t.Errorf("dxSet(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := dxDel("VRROptimizeEnable=0;SwapEffectUpgradeEnable=1;", "SwapEffectUpgradeEnable"); got != "VRROptimizeEnable=0;" {
		t.Errorf("dxDel = %q", got)
	}
	if got := dxDel("SwapEffectUpgradeEnable=1;", "SwapEffectUpgradeEnable"); got != "" {
		t.Errorf("dxDel to empty = %q", got)
	}
	if dxGet("A=1;SwapEffectUpgradeEnable=1;", "SwapEffectUpgradeEnable") != "1" || dxGet("", "x") != "" {
		t.Error("dxGet")
	}
}

func TestWTDefaultProfile(t *testing.T) {
	src := "{\n    // comment stays\n    \"$schema\": \"x\",\n    \"defaultProfile\": \"{61C54BBD-C2C6-5271-96E7-009A87FF44BF}\",\n    \"profiles\": {}\n}"
	if g := wtDefaultProfile(src); g != "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}" {
		t.Fatalf("read: %q", g)
	}
	out, ok := wtSetDefaultProfile(src, "{574e775e-4f2a-5b96-ac1e-a2962a402336}")
	if !ok {
		t.Fatal("not changed")
	}
	want := "{\n    // comment stays\n    \"$schema\": \"x\",\n    \"defaultProfile\": \"{574e775e-4f2a-5b96-ac1e-a2962a402336}\",\n    \"profiles\": {}\n}"
	if out != want {
		t.Fatalf("got\n%s", out)
	}
	if _, ok := wtSetDefaultProfile(`{"profiles":{}}`, "{574e775e-4f2a-5b96-ac1e-a2962a402336}"); ok {
		t.Fatal("changed a file without defaultProfile")
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		exe  string
		args []string
	}{
		{`"C:\Users\A B\AppData\Local\Microsoft\OneDrive\25.1\OneDriveSetup.exe" /uninstall `, `C:\Users\A B\AppData\Local\Microsoft\OneDrive\25.1\OneDriveSetup.exe`, []string{"/uninstall"}},
		{`C:\Program Files\Microsoft OneDrive\25.1\OneDriveSetup.exe  /uninstall  /allusers`, `C:\Program Files\Microsoft OneDrive\25.1\OneDriveSetup.exe`, []string{"/uninstall", "/allusers"}},
		{`setup /x`, "setup", []string{"/x"}},
		{"", "", nil},
	}
	for _, c := range cases {
		exe, args := splitCommand(c.in)
		if exe != c.exe || (len(args) > 0 || len(c.args) > 0) && !reflect.DeepEqual(args, c.args) {
			t.Errorf("splitCommand(%q) = %q %q", c.in, exe, args)
		}
	}
}

func TestBcdFlag(t *testing.T) {
	out := "Windows Boot Loader\n-------------------\nidentifier              {current}\ndisabledynamictick      Yes\nnx                      OptIn\n"
	if p, on := bcdFlag(out, "disabledynamictick"); !p || !on {
		t.Error("yes not detected")
	}
	if p, on := bcdFlag("disabledynamictick      Не\n", "disabledynamictick"); !p || on {
		t.Error("Bulgarian no counted as on")
	}
	if p, _ := bcdFlag(out, "useplatformclock"); p {
		t.Error("missing element found")
	}
}

func TestMisc(t *testing.T) {
	if g := firstGUID("Power Scheme GUID: 381B4222-F694-41F0-9685-FF5BB260DF2E  (Balanced)"); g != "381b4222-f694-41f0-9685-ff5bb260df2e" {
		t.Errorf("firstGUID = %q", g)
	}
	if !pkgMatch("king.com.*", "king.com.CandyCrushSaga") || pkgMatch("king.com.*", "Microsoft.king") || !pkgMatch("Microsoft.Todos", "microsoft.todos") {
		t.Error("pkgMatch")
	}
	for f, want := range map[string]bool{"HH:mm": true, "H:mm": true, "h:mm tt": false, "hh:mm": false} {
		if is24h(f) != want {
			t.Errorf("is24h(%q)", f)
		}
	}
	if psQuote("it's") != "'it''s'" {
		t.Error("psQuote")
	}
}
