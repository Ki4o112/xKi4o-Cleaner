package main

// The Graphics card page: what the card is and how it is doing, the
// drivers on this PC (go back to one of them), older drivers from NVIDIA,
// and taking the driver off (like DDU), with a copy to put it back.
// This file has what does not need Windows: the types, reading NVIDIA's
// lists, version numbers and the checks; gpu_windows.go does the rest.

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type gpuDriverPkg struct {
	Inf      string `json:"inf"`                 // oem12.inf: the name Windows published it under
	Version  string `json:"version"`             // 32.0.16.1742 (Windows' numbering)
	VendorV  string `json:"vendorVer,omitempty"` // 617.42 (NVIDIA's numbering)
	Date     string `json:"date,omitempty"`      // 2026-09-30
	Provider string `json:"provider,omitempty"`
	Desc     string `json:"desc,omitempty"`
	Size     int64  `json:"size"`
	InUse    bool   `json:"inUse"`
	Basic    bool   `json:"basic,omitempty"` // Microsoft's own (the Basic Display Adapter)
}

type gpuDevice struct {
	ID       string         `json:"id"` // the device instance (PCI\VEN_10DE&DEV_2684&…\4&…)
	Name     string         `json:"name"`
	Vendor   string         `json:"vendor"` // nvidia, amd, intel, other
	VenID    string         `json:"venId,omitempty"`
	DevID    string         `json:"devId,omitempty"`
	SubSys   string         `json:"subsys,omitempty"`
	Driver   gpuDriverPkg   `json:"driver"`
	Problem  int            `json:"problem"` // Device Manager's code (43, 28, 22…); 0 = fine
	Disabled bool           `json:"disabled"`
	NoDriver bool           `json:"noDriver"` // runs on the Microsoft Basic Display Adapter
	VRAM     uint64         `json:"vram"`
	Virtual  bool           `json:"virtual"` // Parsec, Meta, Citrix… display adapters
	Packages []gpuDriverPkg `json:"packages"`
}

type gpuDisplay struct {
	Name    string `json:"name"` // \\.\DISPLAY1
	Monitor string `json:"monitor"`
	Adapter string `json:"adapter"`
	Conn    string `json:"conn,omitempty"` // hdmi, dp, dvi, vga, internal
	Primary bool   `json:"primary"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Hz      int    `json:"hz"`
	MaxHz   int    `json:"maxHz"` // at this resolution
	MaxW    int    `json:"maxW"`
	MaxH    int    `json:"maxH"`
	Rates   []int  `json:"rates"` // at this resolution, highest first
}

type gpuBackup struct {
	Dir     string `json:"dir"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
	Version string `json:"version"`
	VendorV string `json:"vendorVer,omitempty"`
	Date    string `json:"date,omitempty"`
	Inf     string `json:"inf"` // the INF inside the copy
	Size    int64  `json:"size"`
	Made    int64  `json:"made"` // unix ms
}

type gpuState struct {
	Devices   []gpuDevice  `json:"devices"`
	Displays  []gpuDisplay `json:"displays"`
	HAGS      string       `json:"hags"`      // on, off, "" (the card or Windows has none)
	WUDrivers bool         `json:"wuDrivers"` // Windows may download a driver for a device by itself
	WUByUs    bool         `json:"wuByUs"`    // the app turned that off (and can turn it back)
	Backups   []gpuBackup  `json:"backups"`
	Admin     bool         `json:"admin"`
	Took      int64        `json:"took"`
	Error     string       `json:"error,omitempty"`
}

// gpuSensors: the card's live numbers. NVIDIA's own library tells the most;
// for other cards Windows' counters give the load and the memory used.
type gpuSensors struct {
	OK          bool     `json:"ok"`
	Src         string   `json:"src,omitempty"` // nvml, pdh
	Name        string   `json:"name,omitempty"`
	Util        int      `json:"util"` // %; -1 unknown
	Temp        int      `json:"temp"` // °C; 0 unknown
	MemUsed     uint64   `json:"memUsed"`
	MemTotal    uint64   `json:"memTotal"`
	Power       float64  `json:"power"`      // W
	PowerLimit  float64  `json:"powerLimit"` // W
	Fan         int      `json:"fan"`        // %; -1 unknown (or no fan reading)
	Clock       int      `json:"clock"`      // MHz
	MemClock    int      `json:"memClock"`
	MaxClock    int      `json:"maxClock"`
	MaxMemClock int      `json:"maxMemClock"`
	PState      int      `json:"pstate"` // 0 = full speed … 12 = idle; -1 unknown
	Limits      []string `json:"limits,omitempty"`
	PCIeGen     int      `json:"pcieGen"`
	PCIeWidth   int      `json:"pcieWidth"`
	PCIeMaxGen  int      `json:"pcieMaxGen"`
	PCIeMaxW    int      `json:"pcieMaxWidth"`
	BAR1        uint64   `json:"bar1"`
	ReBAR       string   `json:"rebar,omitempty"` // on, off ("" unknown)
	Driver      string   `json:"driver,omitempty"`
	VBIOS       string   `json:"vbios,omitempty"`
	CUDA        string   `json:"cuda,omitempty"`
	SlowTemp    int      `json:"slowTemp"` // where the card starts to slow itself down
	Enc         int      `json:"enc"`      // video encoder load %; -1
	Dec         int      `json:"dec"`
}

type gpuResult struct {
	OK      bool     `json:"ok"`
	Reboot  bool     `json:"reboot,omitempty"`
	Error   string   `json:"error,omitempty"`
	Done    []string `json:"done,omitempty"`
	Backup  string   `json:"backup,omitempty"`
	Now     string   `json:"now,omitempty"` // the driver the card has now
	Path    string   `json:"path,omitempty"`
	Signer  string   `json:"signer,omitempty"`
	Seconds int      `json:"seconds,omitempty"`
}

// ---------------------------------------------------------------- what a card is

var reHwID = regexp.MustCompile(`(?i)^PCI\\VEN_([0-9A-F]{4})&DEV_([0-9A-F]{4})(?:&SUBSYS_([0-9A-F]{8}))?`)

// gpuVendor reads the maker from the hardware IDs (PCI\VEN_10DE&DEV_2684…).
func gpuVendor(hwids []string, name string) (vendor, ven, dev, subsys string) {
	for _, h := range hwids {
		if m := reHwID.FindStringSubmatch(h); m != nil {
			ven, dev = strings.ToUpper(m[1]), strings.ToUpper(m[2])
			if m[3] != "" && subsys == "" {
				subsys = strings.ToUpper(m[3])
			}
		}
	}
	switch ven {
	case "10DE":
		vendor = "nvidia"
	case "1002", "1022":
		vendor = "amd"
	case "8086":
		vendor = "intel"
	}
	if vendor == "" {
		n := strings.ToLower(name)
		switch {
		case strings.Contains(n, "nvidia") || strings.Contains(n, "geforce"):
			vendor = "nvidia"
		case strings.Contains(n, "radeon") || strings.HasPrefix(n, "amd"):
			vendor = "amd"
		case strings.Contains(n, "intel"):
			vendor = "intel"
		default:
			vendor = "other"
		}
	}
	return
}

// winDriverVersion: the 64-bit DriverVersion of SetupAPI as a.b.c.d.
func winDriverVersion(v uint64) string {
	return fmt.Sprintf("%d.%d.%d.%d", v>>48, (v>>32)&0xffff, (v>>16)&0xffff, v&0xffff)
}

// nvVersion: Windows' number of an NVIDIA driver (32.0.16.1742) as NVIDIA
// writes it (617.42): the last five digits of the last two parts.
func nvVersion(win string) string {
	p := strings.Split(strings.TrimSpace(win), ".")
	if len(p) != 4 {
		return ""
	}
	c, err1 := strconv.Atoi(p[2])
	d, err2 := strconv.Atoi(p[3])
	if err1 != nil || err2 != nil || c < 10 || c > 99 || d < 0 || d > 9999 { // NVIDIA's third part has two digits
		return ""
	}
	s := fmt.Sprintf("%d%04d", c%10, d)
	if len(s) != 5 {
		return ""
	}
	return s[:3] + "." + s[3:]
}

// nvNumber makes NVIDIA's version comparable (617.42 → 61742).
func nvNumber(v string) int {
	a, b, ok := strings.Cut(strings.TrimSpace(v), ".")
	if !ok || len(b) != 2 {
		return -1
	}
	x, e1 := strconv.Atoi(a)
	y, e2 := strconv.Atoi(b)
	if e1 != nil || e2 != nil {
		return -1
	}
	return x*100 + y
}

// isVirtualAdapter: remote desktop, streaming and VR "display adapters"
// that are no graphics card.
func isVirtualAdapter(name, ven string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"virtual", "parsec", "remote", "indirect", "meta ", "oculus", "citrix", "vmware", "hyper-v", "spacedesk", "duet", "idd", "displaylink", "basic render"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return ven == "" && !strings.Contains(n, "basic display")
}

// gpuProblemKey: what Device Manager's code means, as a text of the page.
func gpuProblemKey(code int) string {
	switch code {
	case 0:
		return ""
	case 10, 12, 14, 18, 19, 21, 22, 24, 28, 31, 32, 37, 39, 43, 45, 52:
		return fmt.Sprintf("gpu.p.%d", code)
	}
	return "gpu.p.x"
}

// rebarState: NVIDIA shows Resizable BAR through the size of its BAR1
// window: 256 MB without it, about the whole memory with it.
func rebarState(bar1 uint64) string {
	switch {
	case bar1 == 0:
		return ""
	case bar1 >= 1<<30:
		return "on"
	}
	return "off"
}

// nvLimits: NVML's "clocks event reasons" in words of the page.
func nvLimits(mask uint64) []string {
	var out []string
	add := func(bit uint64, k string) {
		if mask&bit != 0 {
			out = append(out, k)
		}
	}
	add(0x1, "idle")
	add(0x4, "power")
	add(0x20, "sync")
	add(0x40|0x80, "thermal")
	add(0x8|0x100, "hw")
	add(0x2, "app")
	return out
}

// ---------------------------------------------------------------- NVIDIA's lists

type nvProduct struct {
	Series int
	ID     int
	Name   string
}

// nvProducts reads NVIDIA's list of products (lookupValueSearch, TypeID=3):
// <LookupValue ParentID="120"><Name>GeForce RTX 3070</Name><Value>933</Value></LookupValue>
func nvProducts(b []byte) ([]nvProduct, error) {
	var doc struct {
		Values []struct {
			Parent string `xml:"ParentID,attr"`
			Name   string `xml:"Name"`
			Value  string `xml:"Value"`
		} `xml:"LookupValues>LookupValue"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := make([]nvProduct, 0, len(doc.Values))
	for _, v := range doc.Values {
		s, e1 := strconv.Atoi(strings.TrimSpace(v.Parent))
		id, e2 := strconv.Atoi(strings.TrimSpace(v.Value))
		if e1 != nil || e2 != nil || strings.TrimSpace(v.Name) == "" {
			continue
		}
		out = append(out, nvProduct{Series: s, ID: id, Name: strings.TrimSpace(v.Name)})
	}
	if len(out) == 0 {
		return nil, errors.New("no products")
	}
	return out, nil
}

func nvNorm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "nvidia ")
	s = strings.ReplaceAll(s, "(r)", "")
	s = strings.ReplaceAll(s, "(tm)", "")
	return reSpaces.ReplaceAllString(strings.TrimSpace(s), " ")
}

// nvMatch: the products with the card's name ("NVIDIA GeForce RTX 4090" is
// "GeForce RTX 4090" there; laptops say "… Laptop GPU" in both).
func nvMatch(prods []nvProduct, gpuName string) []nvProduct {
	want := nvNorm(gpuName)
	var out []nvProduct
	for _, p := range prods {
		if nvNorm(p.Name) == want {
			out = append(out, p)
		}
	}
	return out
}

type nvDriver struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Name    string `json:"name"`
	Studio  bool   `json:"studio"`
	Date    string `json:"date"` // 2026-09-30
	Size    string `json:"size"` // as NVIDIA writes it: "895.77 MB"
	URL     string `json:"url"`
	Details string `json:"details"`
	Beta    bool   `json:"beta,omitempty"`
}

// nvDrivers reads NVIDIA's driver list (AjaxDriverService, DriverManualLookup).
// Its texts come URL-encoded ("GeForce%20Game%20Ready%20Driver").
func nvDrivers(b []byte) ([]nvDriver, error) {
	var doc struct {
		Success any `json:"Success"`
		IDS     []struct {
			Info map[string]any `json:"downloadInfo"`
		} `json:"IDS"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	// "Success" is how many drivers came back ("0": none for this card)
	if n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(doc.Success))); err != nil || n < 1 || len(doc.IDS) == 0 {
		return nil, errors.New("NVIDIA: no drivers for this card")
	}
	str := func(m map[string]any, k string) string {
		s, _ := m[k].(string)
		if u, err := url.QueryUnescape(strings.ReplaceAll(s, "+", "%2B")); err == nil {
			s = u
		}
		return strings.TrimSpace(s)
	}
	var out []nvDriver
	seen := map[string]bool{}
	for _, it := range doc.IDS {
		m := it.Info
		if m == nil {
			continue
		}
		d := nvDriver{ID: str(m, "ID"), Version: str(m, "Version"), Name: str(m, "Name"), Size: str(m, "DownloadURLFileSize"),
			URL: str(m, "DownloadURL"), Details: str(m, "DetailsURL"), Beta: str(m, "IsBeta") == "1"}
		if nvNumber(d.Version) < 0 || !nvDownloadOK(d.URL) || seen[d.URL] {
			continue
		}
		seen[d.URL] = true
		d.Studio = strings.Contains(strings.ToLower(d.Name), "studio")
		if t, err := time.Parse("Mon Jan 2, 2006", str(m, "ReleaseDateTime")); err == nil {
			d.Date = t.Format("2006-01-02")
		}
		if !strings.HasPrefix(d.Details, "https://") {
			d.Details = ""
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, k int) bool { return nvNumber(out[i].Version) > nvNumber(out[k].Version) })
	return out, nil
}

// nvDownloadOK: only NVIDIA's download servers, over https, an .exe.
func nvDownloadOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h != "download.nvidia.com" && !strings.HasSuffix(h, ".download.nvidia.com") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".exe")
}

// nvIsNotebook: NVIDIA's notebook packages say so in the file name.
func nvIsNotebook(downloadURL string) bool {
	return strings.Contains(strings.ToLower(downloadURL), "notebook")
}

// nvSigner checks Get-AuthenticodeSignature's answer ("Valid|CN=NVIDIA
// Corporation, O=NVIDIA Corporation, …"): a valid signature by NVIDIA.
func nvSigner(out string) (string, bool) {
	status, subj, _ := strings.Cut(strings.TrimSpace(out), "|")
	if !strings.EqualFold(strings.TrimSpace(status), "Valid") {
		return "", false
	}
	for _, part := range strings.Split(subj, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(k, "O") && strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"`), "NVIDIA Corporation") {
			return "NVIDIA Corporation", true
		}
	}
	return "", false
}

// ---------------------------------------------------------------- the plan to take a driver off

// gpuRemovePlan: the packages to remove from a card, the ones not in use
// first (else Windows would put one of them on the card in between) and
// the one in use last. Microsoft's own drivers are never on the list.
func gpuRemovePlan(d gpuDevice, all bool) []gpuDriverPkg {
	var old, cur []gpuDriverPkg
	for _, p := range d.Packages {
		if p.Basic || !strings.HasPrefix(strings.ToLower(p.Inf), "oem") {
			continue
		}
		switch {
		case p.InUse || strings.EqualFold(p.Inf, d.Driver.Inf):
			cur = append(cur, p)
		case all:
			old = append(old, p)
		}
	}
	if len(cur) == 0 && !d.NoDriver && strings.HasPrefix(strings.ToLower(d.Driver.Inf), "oem") {
		cur = append(cur, d.Driver)
	}
	return append(old, cur...)
}

// gpuBackupDirName: a folder name for a copy of a driver.
func gpuBackupDirName(vendor, version, date string, now time.Time) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' {
				return r
			}
			return '_'
		}, s)
	}
	n := clean(vendor) + "-" + clean(version)
	if date != "" {
		n += "-" + clean(date)
	}
	return n + "-" + now.Format("20060102-150405")
}
