package main

import (
	"strings"
	"testing"
)

func TestWUParse(t *testing.T) {
	// PowerShell 5.1: a one-item list comes as the item itself
	one := `{"updates":{"ID":"0b1c2d3e-1111-2222-3333-444455556666","Title":"2026-10 Cumulative Update for Windows 11 Version 25H2 (KB5061111)","KB":"5061111","Type":1,"Size":812345678,"Downloaded":false,"BrowseOnly":false,"Reboot":true,"Severity":"Critical","Cats":"Security Updates|Windows 11","Date":"2026-10-14","Desc":"A security update","DrvClass":"","DrvVer":""},"history":{"title":"Security Intelligence Update","date":"2026-10-17T08:00:00Z","result":2,"op":1},"reboot":false}`
	st, err := wuParse("noise\n" + one + "\n")
	if err != nil || len(st.Updates) != 1 || len(st.History) != 1 {
		t.Fatalf("one: %+v %v", st, err)
	}
	u := st.Updates[0]
	if u.KB != "KB5061111" || u.Kind != "software" || !u.Reboot || u.Severity != "Critical" || len(u.Cats) != 2 || u.Size != 812345678 {
		t.Errorf("update %+v", u)
	}
	many := `{"updates":[{"ID":"0b1c2d3e-1111-2222-3333-444455556666","Title":"A","Type":1},{"ID":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","Title":"NVIDIA - Display - 32.0.15.6094","Type":2,"BrowseOnly":true,"DrvClass":"Display","DrvVer":"2024-08-14"},{"ID":"bad id","Title":"x","Type":1},{"ID":"aaaaaaaa-bbbb-cccc-dddd-ffffffffffff","Title":"","Type":1}],"history":[],"reboot":true}`
	st, err = wuParse(many)
	if err != nil || len(st.Updates) != 2 || !st.Reboot {
		t.Fatalf("many: %+v %v", st, err)
	}
	d := st.Updates[1]
	if d.Kind != "driver" || !d.Display || !d.Optional {
		t.Errorf("driver %+v", d)
	}
	none := `{"updates":[],"history":null,"reboot":false}`
	if st, err := wuParse(none); err != nil || len(st.Updates) != 0 || st.Updates == nil || st.History == nil {
		t.Errorf("none: %+v %v", st, err)
	}
	if _, err := wuParse("ERR|0x8024402C|no server"); err == nil || !strings.Contains(err.Error(), T("wu.hr.net")) {
		t.Errorf("err: %v", err)
	}
	if _, err := wuParse(""); err == nil {
		t.Error("nothing printed is an error")
	}
}

func TestWULines(t *testing.T) {
	if li, ok := wuLine("COUNT|3"); !ok || li.Count != 3 {
		t.Error("COUNT")
	}
	if li, ok := wuLine("STEP|2|download|0b1c2d3e-1111-2222-3333-444455556666"); !ok || li.I != 2 || li.Phase != "download" {
		t.Error("STEP")
	}
	if li, ok := wuLine("DONE|2|0b1c2d3e-1111-2222-3333-444455556666|2|0x00000000|1"); !ok || li.Result != 2 || !li.Reboot {
		t.Errorf("DONE %+v", li)
	}
	if _, ok := wuLine("hello"); ok {
		t.Error("noise")
	}
	if !wuIDsOK([]string{"0b1c2d3e-1111-2222-3333-444455556666"}) || wuIDsOK(nil) || wuIDsOK([]string{"x'); Remove-Item C:\\ -Recurse; ('"}) {
		t.Error("wuIDsOK")
	}
	if wuHResult("0x80070422") != T("wu.hr.off") || wuHResult("0x80241234") != "0x80241234" || wuHResult("x") != "" {
		t.Error("wuHResult")
	}
}
