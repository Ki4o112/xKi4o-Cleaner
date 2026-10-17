package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGPUVendor(t *testing.T) {
	cases := []struct {
		hw   []string
		name string
		want [4]string
	}{
		{[]string{`PCI\VEN_10DE&DEV_2684&SUBSYS_16F110DE&REV_A1`, `PCI\VEN_10DE&DEV_2684`}, "NVIDIA GeForce RTX 4090", [4]string{"nvidia", "10DE", "2684", "16F110DE"}},
		{[]string{`PCI\VEN_1002&DEV_744C&SUBSYS_0E3B1002&REV_C8`}, "AMD Radeon RX 7900 XTX", [4]string{"amd", "1002", "744C", "0E3B1002"}},
		{[]string{`PCI\VEN_8086&DEV_A780&SUBSYS_88821458&REV_04`}, "Intel(R) UHD Graphics 770", [4]string{"intel", "8086", "A780", "88821458"}},
		{[]string{`ROOT\ParsecVDA`}, "Parsec Virtual Display Adapter", [4]string{"other", "", "", ""}},
		{nil, "NVIDIA GeForce GTX 1060", [4]string{"nvidia", "", "", ""}},
		{nil, "Radeon RX 580", [4]string{"amd", "", "", ""}},
	}
	for _, c := range cases {
		v, ven, dev, sub := gpuVendor(c.hw, c.name)
		if got := [4]string{v, ven, dev, sub}; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDriverVersions(t *testing.T) {
	v := uint64(32)<<48 | uint64(0)<<32 | uint64(16)<<16 | 1742
	if got := winDriverVersion(v); got != "32.0.16.1742" {
		t.Errorf("winDriverVersion = %q", got)
	}
	for in, want := range map[string]string{
		"32.0.16.1742": "617.42", "31.0.15.5222": "552.22", "27.21.14.5671": "456.71", "32.0.15.6094": "560.94",
		"32.0.101.6078": "", "1.2.3": "", "": "", "32.0.16.x": "",
	} {
		if got := nvVersion(in); got != want {
			t.Errorf("nvVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if nvNumber("617.42") != 61742 || nvNumber("560.94") != 56094 || nvNumber("x") != -1 || nvNumber("617.4") != -1 {
		t.Error("nvNumber")
	}
	if nvNumber("617.42") <= nvNumber("580.97") {
		t.Error("617.42 should be newer than 580.97")
	}
}

func TestVirtualAdapters(t *testing.T) {
	for name, ven := range map[string]string{"Parsec Virtual Display Adapter": "", "Microsoft Remote Display Adapter": "", "Meta Virtual Monitor": "", "Citrix Indirect Display Adapter": ""} {
		if !isVirtualAdapter(name, ven) {
			t.Errorf("%s should be virtual", name)
		}
	}
	for name, ven := range map[string]string{"NVIDIA GeForce RTX 4090": "10DE", "AMD Radeon RX 7800 XT": "1002", "Intel(R) Arc(TM) A770 Graphics": "8086", "Microsoft Basic Display Adapter": "10DE"} {
		if isVirtualAdapter(name, ven) {
			t.Errorf("%s is a graphics card", name)
		}
	}
}

func TestGPUSmallReadings(t *testing.T) {
	if gpuProblemKey(0) != "" || gpuProblemKey(43) != "gpu.p.43" || gpuProblemKey(22) != "gpu.p.22" || gpuProblemKey(77) != "gpu.p.x" {
		t.Error("gpuProblemKey")
	}
	if rebarState(0) != "" || rebarState(256<<20) != "off" || rebarState(32<<30) != "on" || rebarState(16<<30) != "on" {
		t.Error("rebarState")
	}
	if got := nvLimits(0x4 | 0x40); !reflect.DeepEqual(got, []string{"power", "thermal"}) {
		t.Errorf("nvLimits = %v", got)
	}
	if got := nvLimits(0x1); !reflect.DeepEqual(got, []string{"idle"}) {
		t.Errorf("nvLimits idle = %v", got)
	}
	if nvLimits(0) != nil {
		t.Error("no reasons")
	}
}

const nvProductsSample = `<?xml version="1.0" encoding="UTF-8"?>
<LookupValueSearch><LookupValues>
<LookupValue ParentID="1"><Name>GeForce 8800 Ultra</Name><Value>8</Value></LookupValue>
<LookupValue ParentID="120"><Name>GeForce RTX 3070</Name><Value>933</Value></LookupValue>
<LookupValue ParentID="120"><Name>GeForce RTX 3070 Ti</Name><Value>965</Value></LookupValue>
<LookupValue ParentID="112"><Name>GeForce GTX 1650</Name><Value>897</Value></LookupValue>
<LookupValue ParentID="115"><Name>GeForce GTX 1650</Name><Value>898</Value></LookupValue>
<LookupValue ParentID="127"><Name>GeForce RTX 4090</Name><Value>995</Value></LookupValue>
<LookupValue ParentID="129"><Name>GeForce RTX 4070 Laptop GPU</Name><Value>1007</Value></LookupValue>
<LookupValue ParentID="x"><Name>broken</Name><Value>1</Value></LookupValue>
</LookupValues></LookupValueSearch>`

func TestNvProductsAndMatch(t *testing.T) {
	ps, err := nvProducts([]byte(nvProductsSample))
	if err != nil || len(ps) != 7 {
		t.Fatalf("nvProducts: %d, %v", len(ps), err)
	}
	m := nvMatch(ps, "NVIDIA GeForce RTX 3070")
	if len(m) != 1 || m[0].ID != 933 || m[0].Series != 120 {
		t.Errorf("RTX 3070: %+v", m)
	}
	if m := nvMatch(ps, "NVIDIA  GeForce RTX 4090 "); len(m) != 1 || m[0].ID != 995 {
		t.Errorf("RTX 4090: %+v", m)
	}
	if m := nvMatch(ps, "NVIDIA GeForce RTX 4070 Laptop GPU"); len(m) != 1 || m[0].Series != 129 {
		t.Errorf("laptop: %+v", m)
	}
	if m := nvMatch(ps, "NVIDIA GeForce GTX 1650"); len(m) != 2 {
		t.Errorf("GTX 1650 (desktop and notebook): %+v", m)
	}
	if m := nvMatch(ps, "NVIDIA GeForce RTX 3070 Ti"); len(m) != 1 || m[0].ID != 965 {
		t.Errorf("Ti is its own: %+v", m)
	}
	if _, err := nvProducts([]byte("<html>no</html>")); err == nil {
		t.Error("a page that is not the list")
	}
}

const nvDriversSample = `{"Success":"1","IDS":[
 {"downloadInfo":{"ID":"230000","Version":"580.97","Name":"GeForce%20Game%20Ready%20Driver","IsBeta":"0","ReleaseDateTime":"Tue Aug 12, 2025",
   "DownloadURL":"https://us.download.nvidia.com/Windows/580.97/580.97-desktop-win10-win11-64bit-international-dch-whql.exe","DownloadURLFileSize":"873.5 MB","DetailsURL":"https://www.nvidia.com/en-us/drivers/details/230000/"}},
 {"downloadInfo":{"ID":"240000","Version":"617.42","Name":"GeForce%20Game%20Ready%20Driver","IsBeta":"0","ReleaseDateTime":"Wed Sep 30, 2026",
   "DownloadURL":"https://us.download.nvidia.com/Windows/617.42/617.42-desktop-win10-win11-64bit-international-dch-whql.exe","DownloadURLFileSize":"912.30 MB","DetailsURL":"https://www.nvidia.com/en-us/drivers/details/240000/"}},
 {"downloadInfo":{"ID":"239000","Version":"611.10","Name":"NVIDIA%20Studio%20Driver","IsBeta":"0","ReleaseDateTime":"Mon Aug 3, 2026",
   "DownloadURL":"https://us.download.nvidia.com/Windows/611.10/611.10-desktop-win10-win11-64bit-international-nsd-dch-whql.exe","DownloadURLFileSize":"905 MB","DetailsURL":"javascript:alert(1)"}},
 {"downloadInfo":{"ID":"1","Version":"999.99","Name":"x","DownloadURL":"https://evil.example.com/nvidia.exe"}},
 {"downloadInfo":{"ID":"2","Version":"bad","Name":"x","DownloadURL":"https://us.download.nvidia.com/Windows/x.exe"}}
]}`

func TestNvDrivers(t *testing.T) {
	ds, err := nvDrivers([]byte(nvDriversSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("drivers: %+v", ds)
	}
	if ds[0].Version != "617.42" || ds[1].Version != "611.10" || ds[2].Version != "580.97" {
		t.Errorf("newest first: %s %s %s", ds[0].Version, ds[1].Version, ds[2].Version)
	}
	if ds[0].Name != "GeForce Game Ready Driver" || ds[0].Studio || !ds[1].Studio {
		t.Errorf("names: %q studio=%v / %q studio=%v", ds[0].Name, ds[0].Studio, ds[1].Name, ds[1].Studio)
	}
	if ds[0].Date != "2026-09-30" || ds[2].Date != "2025-08-12" || ds[1].Date != "2026-08-03" {
		t.Errorf("dates: %s %s %s", ds[0].Date, ds[1].Date, ds[2].Date)
	}
	if ds[1].Details != "" {
		t.Errorf("a details link that is not https is dropped: %q", ds[1].Details)
	}
	if ds[0].Size != "912.30 MB" {
		t.Errorf("size %q", ds[0].Size)
	}
	if _, err := nvDrivers([]byte(`{"Success":"0","IDS":[]}`)); err == nil {
		t.Error("Success 0 is an error")
	}
	if _, err := nvDrivers([]byte(`not json`)); err == nil {
		t.Error("not JSON")
	}
}

func TestNvDownloadOK(t *testing.T) {
	good := []string{
		"https://us.download.nvidia.com/Windows/617.42/617.42-desktop-win10-win11-64bit-international-dch-whql.exe",
		"https://international.download.nvidia.com/Windows/617.42/x.EXE",
		"https://download.nvidia.com/Windows/1/x.exe",
	}
	bad := []string{
		"http://us.download.nvidia.com/Windows/x.exe",
		"https://us.download.nvidia.com.evil.com/x.exe",
		"https://evildownload.nvidia.com.example/x.exe",
		"https://us.download.nvidia.com/Windows/x.zip",
		"https://user:pw@us.download.nvidia.com/x.exe",
		"https://www.nvidia.com/x.exe",
		"file:///C:/x.exe",
		"",
	}
	for _, u := range good {
		if !nvDownloadOK(u) {
			t.Errorf("should be allowed: %s", u)
		}
	}
	for _, u := range bad {
		if nvDownloadOK(u) {
			t.Errorf("should be refused: %s", u)
		}
	}
	if !nvIsNotebook("https://us.download.nvidia.com/Windows/617.42/617.42-notebook-win10-win11-64bit-international-dch-whql.exe") || nvIsNotebook(good[0]) {
		t.Error("nvIsNotebook")
	}
}

func TestNvSigner(t *testing.T) {
	ok := []string{
		"Valid|CN=NVIDIA Corporation, O=NVIDIA Corporation, L=Santa Clara, S=California, C=US",
		`Valid|CN="NVIDIA Corporation", OU=2-J, O="NVIDIA Corporation", L=Santa Clara, S=California, C=US`,
	}
	no := []string{
		"NotSigned|",
		"HashMismatch|CN=NVIDIA Corporation, O=NVIDIA Corporation",
		"Valid|CN=NVIDIA Corporation, O=Evil Ltd",
		"Valid|CN=Evil, O=NVIDIA Corporation Fake",
		"",
	}
	for _, s := range ok {
		if n, v := nvSigner(s); !v || n != "NVIDIA Corporation" {
			t.Errorf("should pass: %s", s)
		}
	}
	for _, s := range no {
		if _, v := nvSigner(s); v {
			t.Errorf("should fail: %s", s)
		}
	}
}

func TestGPURemovePlan(t *testing.T) {
	d := gpuDevice{Driver: gpuDriverPkg{Inf: "oem45.inf", Version: "32.0.16.1742"}, Packages: []gpuDriverPkg{
		{Inf: "oem45.inf", Version: "32.0.16.1742", InUse: true},
		{Inf: "oem12.inf", Version: "32.0.15.8097"},
		{Inf: "oem30.inf", Version: "32.0.15.6094"},
		{Inf: "display.inf", Version: "10.0.26100.1", Basic: true},
	}}
	names := func(ps []gpuDriverPkg) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.Inf)
		}
		return strings.Join(s, ",")
	}
	if got := names(gpuRemovePlan(d, false)); got != "oem45.inf" {
		t.Errorf("only the one in use: %s", got)
	}
	if got := names(gpuRemovePlan(d, true)); got != "oem12.inf,oem30.inf,oem45.inf" {
		t.Errorf("all, the one in use last, never Microsoft's: %s", got)
	}
	basic := gpuDevice{NoDriver: true, Driver: gpuDriverPkg{Inf: "display.inf", Basic: true}, Packages: []gpuDriverPkg{{Inf: "display.inf", Basic: true, InUse: true}}}
	if got := gpuRemovePlan(basic, true); len(got) != 0 {
		t.Errorf("nothing to remove from the basic driver: %v", got)
	}
	// a card whose list could not be read still gets its own driver removed
	bare := gpuDevice{Driver: gpuDriverPkg{Inf: "oem7.inf", Version: "1.2.3.4"}}
	if got := names(gpuRemovePlan(bare, true)); got != "oem7.inf" {
		t.Errorf("bare: %s", got)
	}
}

func TestGPUBackupDirName(t *testing.T) {
	at := time.Date(2026, 10, 17, 21, 5, 9, 0, time.UTC)
	if got := gpuBackupDirName("nvidia", "32.0.16.1742", "2026-09-30", at); got != "nvidia-32.0.16.1742-2026-09-30-20261017-210509" {
		t.Errorf("name %q", got)
	}
	if got := gpuBackupDirName(`a\b`, "1/2", "", at); strings.ContainsAny(got, `\/:`) {
		t.Errorf("unsafe characters kept: %q", got)
	}
}
