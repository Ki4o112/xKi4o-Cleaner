package main

// System information (like Speccy): what is in the PC. Only read; serial
// numbers and network addresses are left out so the report can be shared.
// The helpers here are free of Windows calls so they are tested anywhere.

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

type siOS struct {
	Name      string `json:"name"`
	Ver       string `json:"ver"` // 24H2
	Build     string `json:"build"`
	UBR       int    `json:"ubr"`
	Arch      string `json:"arch"`
	Installed string `json:"installed"`
	Boot      string `json:"boot"`
	PC        string `json:"pc"`
	Uptime    int64  `json:"uptime"` // seconds
}

type siCPU struct {
	Name    string `json:"name"`
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
	MHz     int    `json:"mhz"`
	L2      int    `json:"l2"` // KB
	L3      int    `json:"l3"` // KB
	Socket  string `json:"socket"`
	Virt    bool   `json:"virt"`
}

type siGPU struct {
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	Date    string `json:"date"`
	RAM     int64  `json:"ram"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Hz      int    `json:"hz"`
	Virtual bool   `json:"virtual"`
}

type siRAM struct {
	Size  int64  `json:"size"`
	MHz   int    `json:"mhz"`
	Cfg   int    `json:"cfg"` // the speed it runs at
	Maker string `json:"maker"`
	Part  string `json:"part"`
	Slot  string `json:"slot"`
	Type  string `json:"type"` // DDR4
	TypeN int    `json:"typeN"`
}

type siBoard struct {
	Maker     string `json:"maker"`
	Model     string `json:"model"`
	PCMaker   string `json:"pcMaker"`
	PCModel   string `json:"pcModel"`
	Laptop    bool   `json:"laptop"`
	Total     int64  `json:"total"`
	BIOS      string `json:"bios"`
	BIOSMaker string `json:"biosMaker"`
	BIOSDate  string `json:"biosDate"`
}

type siSecurity struct {
	Firmware   string   `json:"firmware"`   // UEFI / Legacy
	SecureBoot string   `json:"secureBoot"` // on / off / ""
	TPM        string   `json:"tpm"`        // 2.0, "" when none
	VBS        bool     `json:"vbs"`
	HVCI       bool     `json:"hvci"`
	AV         []string `json:"av"`
}

type siMonitor struct {
	Name  string  `json:"name"`
	Maker string  `json:"maker"`
	Inch  float64 `json:"inch"`
	Year  int     `json:"year"`
}

type siNet struct {
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Speed int64  `json:"speed"` // bits per second
	WiFi  bool   `json:"wifi"`
}

type siDisk struct {
	Name  string `json:"name"`
	Media string `json:"media"`
	Bus   string `json:"bus"`
	Size  int64  `json:"size"`
}

type siBattery struct {
	Name   string `json:"name"`
	Charge int    `json:"charge"`
	Full   int64  `json:"full"`   // mWh
	Design int64  `json:"design"` // mWh
	Health int    `json:"health"` // % of the capacity it had new; 0 when unknown
}

type sysInfo struct {
	OS       siOS        `json:"os"`
	CPU      []siCPU     `json:"cpu"`
	GPU      []siGPU     `json:"gpu"`
	RAM      []siRAM     `json:"ram"`
	Slots    int         `json:"slots"`
	Board    siBoard     `json:"board"`
	Security siSecurity  `json:"security"`
	Monitors []siMonitor `json:"monitors"`
	Audio    []string    `json:"audio"`
	Net      []siNet     `json:"net"`
	Disks    []siDisk    `json:"disks"`
	Battery  *siBattery  `json:"battery,omitempty"`
	Error    string      `json:"error,omitempty"`
	Took     int64       `json:"took"` // ms
}

// memTypeName: the SMBIOS memory type as people know it.
func memTypeName(n int) string {
	switch n {
	case 18:
		return "DDR"
	case 19, 20:
		return "DDR2"
	case 24:
		return "DDR3"
	case 26:
		return "DDR4"
	case 27:
		return "LPDDR"
	case 28:
		return "LPDDR2"
	case 29:
		return "LPDDR3"
	case 30:
		return "LPDDR4"
	case 34:
		return "DDR5"
	case 35:
		return "LPDDR5"
	}
	return ""
}

// JEDEC codes some boards report instead of the maker's name.
var ramMakers = map[string]string{
	"80CE": "Samsung", "CE00": "Samsung", "802C": "Micron", "2C00": "Micron", "80AD": "SK hynix", "AD00": "SK hynix",
	"859B": "Crucial", "9B05": "Crucial", "04CD": "G.Skill", "CD04": "G.Skill", "029E": "Corsair", "9E02": "Corsair",
	"0198": "Kingston", "9801": "Kingston", "04CB": "ADATA", "CB04": "ADATA", "014F": "Transcend", "4F01": "Transcend",
	"0B03": "Nanya", "830B": "Nanya", "8551": "Qimonda", "0194": "Team Group", "04EF": "Team Group", "0443": "Ramaxel", "8A76": "Lexar",
}

// ramMaker turns a JEDEC code ("80CE", "CE00000000000000") into a name;
// a name is kept as it is.
func ramMaker(s string) string {
	s = strings.TrimSpace(s)
	u := strings.ToUpper(s)
	if len(u) >= 4 && strings.Trim(u, "0123456789ABCDEF") == "" {
		if n, ok := ramMakers[u[:4]]; ok {
			return n
		}
		if strings.Trim(u, "0") == "" {
			return ""
		}
		return s // a code this list does not know: shown as it is
	}
	if strings.EqualFold(s, "Unknown") {
		return ""
	}
	return s
}

// Plug and Play codes of monitor makers.
var monitorMakers = map[string]string{
	"SAM": "Samsung", "SEC": "Samsung", "SDC": "Samsung", "DEL": "Dell", "AUS": "ASUS", "ACR": "Acer", "ACI": "ASUS", "GSM": "LG", "LGD": "LG",
	"BNQ": "BenQ", "AOC": "AOC", "HWP": "HP", "HPN": "HP", "LEN": "Lenovo", "MSI": "MSI", "PHL": "Philips", "VSC": "ViewSonic",
	"GBT": "Gigabyte", "SNY": "Sony", "IVM": "iiyama", "AUO": "AU Optronics", "BOE": "BOE", "CMN": "Innolux", "SHP": "Sharp",
	"APP": "Apple", "MEI": "Panasonic", "NEC": "NEC", "EIZ": "EIZO", "XMI": "Xiaomi", "HKC": "HKC", "KTC": "KTC", "TCL": "TCL", "HSD": "HannStar",
}

func monitorMaker(code string) string {
	code = strings.TrimSpace(code)
	if n, ok := monitorMakers[strings.ToUpper(code)]; ok {
		return n
	}
	return code
}

var (
	reCPUAt    = regexp.MustCompile(`(?i)\s+(CPU\s+)?@\s*[\d.]+\s*GHz.*$`)
	reCPUCores = regexp.MustCompile(`(?i)\s+\d+-Core\s+Processor\s*$|\s+(Processor|CPU)\s*$`)
	reCPUWord  = regexp.MustCompile(`(?i)\s+CPU\s+`)
	reSpaces   = regexp.MustCompile(`\s+`)
)

// cpuName makes "Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz" into
// "Intel Core i7-9700K" and "AMD Ryzen 7 5800X 8-Core Processor" into
// "AMD Ryzen 7 5800X" (the speed and cores are shown on their own).
func cpuName(s string) string {
	s = strings.NewReplacer("(R)", "", "(r)", "", "(TM)", "", "(tm)", "", "®", "", "™", "").Replace(s)
	s = reCPUAt.ReplaceAllString(s, "")
	s = reCPUCores.ReplaceAllString(s, "")
	s = reCPUWord.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// shortMaker: the name people use for a maker ("ASUSTeK COMPUTER INC." →
// "ASUS"); others stay as they are.
func shortMaker(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, m := range []struct{ pre, name string }{
		{"asustek", "ASUS"}, {"micro-star", "MSI"}, {"gigabyte", "Gigabyte"}, {"dell", "Dell"}, {"hewlett-packard", "HP"}, {"hp ", "HP"},
		{"lenovo", "Lenovo"}, {"acer", "Acer"}, {"asrock", "ASRock"}, {"samsung electronics", "Samsung"}, {"microsoft corporation", "Microsoft"},
		{"intel corporation", "Intel"}, {"biostar", "Biostar"}, {"evga", "EVGA"}, {"huawei", "Huawei"}, {"xiaomi", "Xiaomi"}, {"razer", "Razer"},
	} {
		if strings.HasPrefix(l, m.pre) || l == strings.TrimSpace(m.pre) {
			return m.name
		}
	}
	return strings.TrimSpace(s)
}

// winName: "Microsoft Windows 11 Pro" → "Windows 11 Pro".
func winName(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Microsoft "))
}

// virtualGPU: display adapters that are not a graphics card.
func virtualGPU(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"basic display", "basic render", "remote display", "virtual", "parsec", "idd", "spacedesk", "citrix", "vmware svga", "hyper-v"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// batteryHealth: how much of its first capacity the battery still holds.
func batteryHealth(full, design int64) int {
	if full <= 0 || design <= 0 {
		return 0
	}
	h := int((full*100 + design/2) / design)
	if h > 100 {
		h = 100
	}
	return h
}

// tidySysInfo cleans what Windows reports: names, duplicates, derived
// values. now is when it was read (for the uptime).
func tidySysInfo(si *sysInfo, now time.Time) {
	si.OS.Name = winName(si.OS.Name)
	if t, err := time.Parse(time.RFC3339, si.OS.Boot); err == nil && now.After(t) {
		si.OS.Uptime = int64(now.Sub(t).Seconds())
	}
	for i := range si.CPU {
		si.CPU[i].Name = cpuName(si.CPU[i].Name)
		si.CPU[i].Socket = strings.TrimSpace(si.CPU[i].Socket)
	}
	gpus := si.GPU[:0]
	seen := map[string]bool{}
	for _, g := range si.GPU {
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" || seen[g.Name+"|"+g.Driver] {
			continue
		}
		seen[g.Name+"|"+g.Driver] = true
		g.Virtual = virtualGPU(g.Name)
		gpus = append(gpus, g)
	}
	sort.SliceStable(gpus, func(a, b int) bool { return !gpus[a].Virtual && gpus[b].Virtual })
	si.GPU = gpus
	for i := range si.RAM {
		r := &si.RAM[i]
		r.Maker = ramMaker(r.Maker)
		r.Part = strings.TrimSpace(r.Part)
		r.Slot = strings.TrimSpace(r.Slot)
		if r.Type == "" {
			r.Type = memTypeName(r.TypeN)
		}
	}
	if si.Slots < len(si.RAM) {
		si.Slots = len(si.RAM)
	}
	for _, p := range []*string{&si.Board.Maker, &si.Board.Model, &si.Board.PCMaker, &si.Board.PCModel, &si.Board.BIOS, &si.Board.BIOSMaker} {
		*p = strings.TrimSpace(*p)
		if l := strings.ToLower(*p); l == "to be filled by o.e.m." || l == "default string" || l == "system product name" || l == "system manufacturer" || l == "not applicable" {
			*p = ""
		}
	}
	si.Board.Maker, si.Board.PCMaker = shortMaker(si.Board.Maker), shortMaker(si.Board.PCMaker)
	mons := si.Monitors[:0]
	for _, m := range si.Monitors {
		m.Name = strings.TrimSpace(m.Name)
		m.Maker = monitorMaker(m.Maker)
		if m.Name != "" || m.Maker != "" {
			mons = append(mons, m)
		}
	}
	si.Monitors = mons
	audio := []string{}
	seenA := map[string]bool{}
	for _, a := range si.Audio {
		a = strings.TrimSpace(a)
		if a != "" && !seenA[strings.ToLower(a)] {
			seenA[strings.ToLower(a)] = true
			audio = append(audio, a)
		}
	}
	si.Audio = audio
	av := []string{}
	seenV := map[string]bool{}
	for _, a := range si.Security.AV {
		a = strings.TrimSpace(a)
		if a != "" && !seenV[strings.ToLower(a)] {
			seenV[strings.ToLower(a)] = true
			av = append(av, a)
		}
	}
	si.Security.AV = av
	if si.Battery != nil {
		si.Battery.Health = batteryHealth(si.Battery.Full, si.Battery.Design)
	}
	if si.CPU == nil {
		si.CPU = []siCPU{}
	}
	if si.GPU == nil {
		si.GPU = []siGPU{}
	}
	if si.RAM == nil {
		si.RAM = []siRAM{}
	}
	if si.Monitors == nil {
		si.Monitors = []siMonitor{}
	}
	if si.Net == nil {
		si.Net = []siNet{}
	}
	if si.Disks == nil {
		si.Disks = []siDisk{}
	}
}
