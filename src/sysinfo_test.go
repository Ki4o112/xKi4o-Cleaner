package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCPUName(t *testing.T) {
	cases := map[string]string{
		"Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz":                     "Intel Core i7-9700K",
		"AMD Ryzen 7 5800X 8-Core Processor            ":               "AMD Ryzen 7 5800X",
		"13th Gen Intel(R) Core(TM) i5-13600KF":                        "13th Gen Intel Core i5-13600KF",
		"AMD Ryzen 9 7950X3D 16-Core Processor":                        "AMD Ryzen 9 7950X3D",
		"Intel(R) Core(TM) Ultra 7 155H":                               "Intel Core Ultra 7 155H",
		"Intel(R) Pentium(R) CPU G4560 @ 3.50GHz":                      "Intel Pentium G4560",
		"AMD Athlon Silver 3050U with Radeon Graphics":                 "AMD Athlon Silver 3050U with Radeon Graphics",
		"Snapdragon(R) X Elite - X1E78100 - Qualcomm(R) Oryon(TM) CPU": "Snapdragon X Elite - X1E78100 - Qualcomm Oryon",
	}
	for in, want := range cases {
		if got := cpuName(in); got != want {
			t.Errorf("cpuName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRAMMaker(t *testing.T) {
	cases := map[string]string{
		"80CE":             "Samsung",
		"CE00000000000000": "Samsung",
		"802C0000802C":     "Micron",
		"Kingston":         "Kingston",
		"G Skill Intl":     "G Skill Intl",
		"0000":             "",
		"Unknown":          "",
		"  ":               "",
		"ABCD":             "ABCD",
		"04CD":             "G.Skill",
	}
	for in, want := range cases {
		if got := ramMaker(in); got != want {
			t.Errorf("ramMaker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMemTypeAndBattery(t *testing.T) {
	for n, want := range map[int]string{26: "DDR4", 34: "DDR5", 24: "DDR3", 0: "", 2: "", 35: "LPDDR5"} {
		if got := memTypeName(n); got != want {
			t.Errorf("memTypeName(%d) = %q, want %q", n, got, want)
		}
	}
	for _, c := range []struct {
		full, design int64
		want         int
	}{{42000, 50000, 84}, {51000, 50000, 100}, {0, 50000, 0}, {42000, 0, 0}, {33333, 50000, 67}} {
		if got := batteryHealth(c.full, c.design); got != c.want {
			t.Errorf("batteryHealth(%d, %d) = %d, want %d", c.full, c.design, got, c.want)
		}
	}
	for in, want := range map[string]string{"ASUSTeK COMPUTER INC.": "ASUS", "Micro-Star International Co., Ltd.": "MSI", "HP": "HP", "Hewlett-Packard": "HP",
		"LENOVO": "Lenovo", "Dell Inc.": "Dell", "Gigabyte Technology Co., Ltd.": "Gigabyte", "Framework": "Framework", "HPE thing": "HPE thing"} {
		if got := shortMaker(in); got != want {
			t.Errorf("shortMaker(%q) = %q, want %q", in, got, want)
		}
	}
	if monitorMaker("GSM") != "LG" || monitorMaker("XYZ") != "XYZ" || monitorMaker(" del ") != "Dell" {
		t.Error("monitor makers")
	}
}

// What the PowerShell part prints, as it looks on a real PC: every key
// lands in its field, and tidying gives the names people know.
func TestSysInfoFromScript(t *testing.T) {
	out := `{"os":{"name":"Microsoft Windows 11 Pro","ver":"24H2","build":"26100","ubr":4652,"arch":"64-bit","installed":"2025-01-10T09:00:00Z","boot":"2026-10-17T08:00:00Z","pc":"DESKTOP-1"},
"cpu":[{"name":"AMD Ryzen 7 5800X 8-Core Processor            ","cores":8,"threads":16,"mhz":3801,"l2":4096,"l3":32768,"socket":"AM4 ","virt":true}],
"gpu":[{"name":"Parsec Virtual Display Adapter","driver":"0.45","date":"","ram":0,"w":0,"h":0,"hz":0},
       {"name":"NVIDIA GeForce RTX 3070","driver":"32.0.15.6094","date":"2024-08-14T00:00:00Z","ram":8589934592,"w":2560,"h":1440,"hz":165},
       {"name":"NVIDIA GeForce RTX 3070","driver":"32.0.15.6094","date":"2024-08-14T00:00:00Z","ram":8589934592,"w":2560,"h":1440,"hz":165}],
"ram":[{"size":17179869184,"mhz":3600,"cfg":3600,"maker":"CE00000000000000","part":"F4-3600C16-16GVKC   ","slot":"DIMM_A2","typeN":26},
       {"size":17179869184,"mhz":3600,"cfg":3600,"maker":"G Skill Intl","part":"F4-3600C16-16GVKC","slot":"DIMM_B2","typeN":26}],
"slots":4,
"board":{"maker":"ASUSTeK COMPUTER INC.","model":"ROG STRIX B550-F GAMING","pcMaker":"System manufacturer","pcModel":"System Product Name","laptop":false,"total":34272509952,"bios":"3607","biosMaker":"American Megatrends Inc.","biosDate":"2024-03-04T00:00:00Z"},
"security":{"firmware":"UEFI","secureBoot":"on","tpm":"2.0","vbs":true,"hvci":false,"av":["Windows Defender","Windows Defender"]},
"monitors":[{"name":"VG27AQ","maker":"AUS","year":2021,"inch":27.0},{"name":"","maker":"","year":0,"inch":0}],
"audio":["NVIDIA High Definition Audio","Realtek(R) Audio","REALTEK(R) AUDIO"],
"net":[{"name":"Ethernet","desc":"Realtek PCIe 2.5GbE Family Controller","speed":1000000000,"wifi":false}],
"disks":[{"name":"Samsung SSD 970 EVO Plus 1TB","media":"SSD","bus":"NVMe","size":1000204886016}]}`
	var si sysInfo
	if err := json.Unmarshal([]byte(out), &si); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-10-17T10:30:00Z")
	tidySysInfo(&si, now)
	if si.OS.Name != "Windows 11 Pro" || si.OS.UBR != 4652 || si.OS.Uptime != 9000 {
		t.Errorf("os: %+v", si.OS)
	}
	if len(si.CPU) != 1 || si.CPU[0].Name != "AMD Ryzen 7 5800X" || si.CPU[0].Socket != "AM4" || !si.CPU[0].Virt {
		t.Errorf("cpu: %+v", si.CPU)
	}
	if len(si.GPU) != 2 || si.GPU[0].Name != "NVIDIA GeForce RTX 3070" || si.GPU[0].Virtual || !si.GPU[1].Virtual || si.GPU[0].Hz != 165 {
		t.Errorf("gpu (the real card first, the twin dropped, the virtual one marked): %+v", si.GPU)
	}
	if len(si.RAM) != 2 || si.RAM[0].Maker != "Samsung" || si.RAM[0].Type != "DDR4" || si.RAM[0].Part != "F4-3600C16-16GVKC" || si.Slots != 4 {
		t.Errorf("ram: %+v slots %d", si.RAM, si.Slots)
	}
	if si.Board.PCMaker != "" || si.Board.PCModel != "" || si.Board.Model != "ROG STRIX B550-F GAMING" || si.Board.Maker != "ASUS" {
		t.Errorf("board (placeholder names dropped): %+v", si.Board)
	}
	if len(si.Security.AV) != 1 || si.Security.TPM != "2.0" || si.Security.SecureBoot != "on" {
		t.Errorf("security: %+v", si.Security)
	}
	if len(si.Monitors) != 1 || si.Monitors[0].Maker != "ASUS" || si.Monitors[0].Inch != 27 {
		t.Errorf("monitors: %+v", si.Monitors)
	}
	if len(si.Audio) != 2 {
		t.Errorf("audio: %v", si.Audio)
	}
	if si.Battery != nil || len(si.Net) != 1 || len(si.Disks) != 1 {
		t.Errorf("battery %v net %v disks %v", si.Battery, si.Net, si.Disks)
	}
	// nothing read at all: lists are empty, not null (the page loops over them)
	var empty sysInfo
	tidySysInfo(&empty, now)
	b, _ := json.Marshal(empty)
	for _, k := range []string{`"cpu":[]`, `"gpu":[]`, `"ram":[]`, `"monitors":[]`, `"audio":[]`, `"net":[]`, `"disks":[]`, `"av":[]`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("empty info: %s missing in %s", k, b)
		}
	}
}
