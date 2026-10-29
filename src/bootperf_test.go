package main

import "testing"

func TestBootSummary(t *testing.T) {
	recs := []bootRec{
		{ID: 100, T: 1000, D: map[string]string{"BootTime": "41000", "MainPathBootTime": "15000", "BootPostBootTime": "26000"}},
		{ID: 100, T: 3000, D: map[string]string{"BootTime": "38000", "MainPathBootTime": "14000", "BootPostBootTime": "24000"}},
		{ID: 100, T: 2000, D: map[string]string{"BootTime": "0"}}, // a broken record
		{ID: 101, T: 1000, D: map[string]string{"Name": "OneDrive.exe", "FriendlyName": "Microsoft OneDrive", "TotalTime": "9000", "DegradationTime": "6000"}},
		{ID: 101, T: 3000, D: map[string]string{"Name": "OneDrive.exe", "FriendlyName": "Microsoft OneDrive", "TotalTime": "8000", "DegradationTime": "5000"}},
		{ID: 103, T: 3000, D: map[string]string{"Name": "SysMain", "FriendlyName": "SysMain", "TotalTime": "3000", "DegradationTime": "2000"}},
		{ID: 102, T: 3000, D: map[string]string{"Name": "x.sys", "FriendlyName": "", "TotalTime": "1500"}},
		{ID: 101, T: 3000, D: map[string]string{}}, // no name: left out
	}
	p := bootSummary(recs)
	if len(p.Boots) != 2 || p.Boots[0].At != 3000 || p.Boots[0].Total != 38000 || p.Boots[1].Post != 26000 {
		t.Fatalf("boots %+v", p.Boots)
	}
	if len(p.Slow) != 3 {
		t.Fatalf("slow %+v", p.Slow)
	}
	s := p.Slow[0]
	if s.Name != "Microsoft OneDrive" || s.Exe != "onedrive.exe" || s.Times != 2 || s.Delay != 11000 || s.Last != 3000 || s.Kind != "app" {
		t.Fatalf("first %+v", s)
	}
	if p.Slow[1].Kind != "service" || p.Slow[2].Name != "x.sys" || p.Slow[2].Delay != 1500 {
		t.Fatalf("rest %+v", p.Slow[1:])
	}
	if e := bootSummary(nil); e.Boots == nil || e.Slow == nil {
		t.Fatal("nil lists reach the page as null")
	}
}
