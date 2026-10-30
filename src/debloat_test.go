package main

import (
	"strings"
	"testing"
)

type memReg struct {
	vals map[string]regOp // key|name
	keys map[string]bool
}

func (f memReg) value(key, name string) (string, uint64, string, []byte, bool) {
	v, ok := f.vals[strings.ToLower(key+"|"+name)]
	return v.Type, v.Num, v.Str, v.Bin, ok
}
func (f memReg) keyExists(key string) bool { return f.keys[strings.ToLower(key)] }

func TestDebloatData(t *testing.T) {
	d := debloatData()
	if len(d.Features) < 90 || len(d.Apps) < 150 || len(d.Groups) < 9 || !strings.HasPrefix(d.Source, "Win11Debloat") {
		t.Fatalf("%d features, %d apps, %d groups, %q", len(d.Features), len(d.Apps), len(d.Groups), d.Source)
	}
	cats := map[string]bool{}
	for _, c := range d.Categories {
		if c.BG == "" {
			t.Errorf("category %s has no Bulgarian name", c.ID)
		}
		cats[c.ID] = true
	}
	for _, f := range d.Features {
		if f.BG[0] == "" || f.Label == "" || !cats[f.Cat] {
			t.Errorf("feature %s: %+v", f.ID, f.BG)
		}
		if len(f.Apply) == 0 && f.Custom == "" {
			t.Errorf("feature %s does nothing", f.ID)
		}
		for _, ops := range [][]regOp{f.Apply, f.Undo} {
			for _, op := range ops {
				root := strings.SplitN(op.Key, `\`, 2)[0]
				if root != "HKCU" && root != "HKLM" && root != "HKCR" && root != "HKU" {
					t.Errorf("%s: root %s", f.ID, op.Key)
				}
				if op.Op == "set" && op.Type != "dword" && op.Type != "string" && op.Type != "binary" {
					t.Errorf("%s: type %s", f.ID, op.Type)
				}
			}
		}
	}
	for _, g := range d.Groups {
		for _, v := range g.Values {
			for _, id := range v.IDs {
				if dbFeatureByID(id) == nil || v.BG == "" {
					t.Errorf("group %s: %s", g.ID, id)
				}
			}
		}
	}
	for _, id := range d.Defaults {
		if dbFeatureByID(id) == nil {
			t.Errorf("default %s", id)
		}
	}
	for _, a := range d.Apps {
		if len(a.IDs) == 0 || len(a.BG) != 2 || a.BG[1] == "" || (a.Rec != "safe" && a.Rec != "optional" && a.Rec != "unsafe") {
			t.Errorf("app %+v", a)
		}
	}
	// a binary value came through as bytes
	f := dbFeatureByID("DisableAnimations")
	found := false
	for _, op := range f.Apply {
		if op.Type == "binary" && len(op.Bin) == 8 && op.Bin[0] == 0x90 {
			found = true
		}
	}
	if !found {
		t.Error("binary value of DisableAnimations")
	}
	if len(emptyStartBin) < 100 {
		t.Error("start2.bin")
	}
}

func TestDebloatState(t *testing.T) {
	ops := []regOp{
		{Op: "mkkey", Key: `HKCU\A`},
		{Op: "set", Key: `HKCU\A`, Name: "X", Type: "dword", Num: 0},
		{Op: "set", Key: `HKCU\A`, Name: "S", Type: "string", Str: "v"},
		{Op: "delval", Key: `HKCU\A`, Name: "Gone"},
		{Op: "delkey", Key: `HKCR\B`},
	}
	r := memReg{vals: map[string]regOp{}, keys: map[string]bool{}}
	if regApplied(ops, r) {
		t.Fatal("nothing set yet")
	}
	r.keys[`hkcu\a`] = true
	r.vals[`hkcu\a|x`] = regOp{Type: "dword", Num: 0}
	r.vals[`hkcu\a|s`] = regOp{Type: "string", Str: "v"}
	if !regApplied(ops, r) {
		t.Fatal("all set")
	}
	r.vals[`hkcu\a|gone`] = regOp{Type: "dword", Num: 1}
	if regApplied(ops, r) {
		t.Fatal("a value that should be gone")
	}
	delete(r.vals, `hkcu\a|gone`)
	r.keys[`hkcr\b`] = true
	if regApplied(ops, r) {
		t.Fatal("a key that should be gone")
	}
	if regApplied(nil, r) {
		t.Fatal("no operations is not applied")
	}
	f := &dbFeature{Min: 22000, Max: 0}
	if f.fits(19045) || !f.fits(26100) || buildNum("26100.4652") != 26100 {
		t.Fatal("versions")
	}
	if !appMatches("Asphalt8Airborne", "A278AB0D.Asphalt8Airborne") || appMatches("Microsoft.Paint", "Microsoft.Photos") {
		t.Fatal("app names")
	}
}
