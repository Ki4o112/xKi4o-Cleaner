//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var guidDisplayClass = windows.GUID{Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

const (
	displayClassKey = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	driverSearchKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\DriverSearching`
	gpuWUPrevKey    = "gpuWUPrev" // in the settings: SearchOrderConfig before the app changed it (-1: there was none)
)

var (
	modNewdev                    = windows.NewLazySystemDLL("newdev.dll")
	procDiInstallDevice          = modNewdev.NewProc("DiInstallDevice")
	procDiUninstallDriverW       = modNewdev.NewProc("DiUninstallDriverW")
	procDiInstallDriverW         = modNewdev.NewProc("DiInstallDriverW")
	procSetupGetInfStoreLocation = windows.NewLazySystemDLL("setupapi.dll").NewProc("SetupGetInfDriverStoreLocationW")
	procEnumDisplayDevicesW      = user32.NewProc("EnumDisplayDevicesW")
	procEnumDisplaySettingsExW   = user32.NewProc("EnumDisplaySettingsExW")
	procChangeDisplaySettingsExW = user32.NewProc("ChangeDisplaySettingsExW")
	procGetDisplayConfigBufSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig       = user32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetInfo     = user32.NewProc("DisplayConfigGetDeviceInfo")
	procGetSystemPowerStatus     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")
)

// gpuMu: one driver change at a time.
var gpuMu sync.Mutex

// ---------------------------------------------------------------- the cards

func gpuDevices(withPkgs bool) ([]gpuDevice, error) {
	devs, err := windows.SetupDiGetClassDevsEx(&guidDisplayClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, err
	}
	defer devs.Close()
	var out []gpuDevice
	for i := 0; i < 32; i++ {
		di, err := devs.EnumDeviceInfo(i)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			continue
		}
		out = append(out, gpuReadDevice(devs, di, withPkgs))
	}
	return out, nil
}

func devProp(devs windows.DevInfo, di *windows.DevInfoData, p windows.SPDRP) string {
	v, err := devs.DeviceRegistryProperty(di, p)
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func gpuReadDevice(devs windows.DevInfo, di *windows.DevInfoData, withPkgs bool) gpuDevice {
	d := gpuDevice{Name: devProp(devs, di, windows.SPDRP_FRIENDLYNAME)}
	if d.Name == "" {
		d.Name = devProp(devs, di, windows.SPDRP_DEVICEDESC)
	}
	d.ID, _ = devs.DeviceInstanceID(di)
	var hw []string
	if v, err := devs.DeviceRegistryProperty(di, windows.SPDRP_HARDWAREID); err == nil {
		hw, _ = v.([]string)
	}
	d.Vendor, d.VenID, d.DevID, d.SubSys = gpuVendor(hw, d.Name)
	var st, prob uint32
	if windows.CM_Get_DevNode_Status(&st, &prob, di.DevInst, 0) == nil && st&windows.DN_HAS_PROBLEM != 0 {
		d.Problem = int(prob)
		d.Disabled = prob == 22 // CM_PROB_DISABLED
	}
	if key := devProp(devs, di, windows.SPDRP_DRIVER); key != "" {
		d.Driver, d.VRAM = gpuClassKey(key)
	}
	d.NoDriver = d.Driver.Inf == "" || !strings.HasPrefix(strings.ToLower(d.Driver.Inf), "oem")
	if d.NoDriver {
		d.Driver.Basic = true
	}
	d.Virtual = isVirtualAdapter(d.Name, d.VenID)
	if withPkgs && !d.Virtual {
		d.Packages = gpuPackages(devs, di, d.Driver.Inf)
		for i := range d.Packages {
			if d.Vendor == "nvidia" && !d.Packages[i].Basic {
				d.Packages[i].VendorV = nvVersion(d.Packages[i].Version)
			}
		}
	}
	if d.Vendor == "nvidia" && !d.NoDriver {
		d.Driver.VendorV = nvVersion(d.Driver.Version)
	}
	if !d.NoDriver {
		d.Driver.InUse = true
		d.Driver.Size = driverStoreSize(strings.ToLower(d.Driver.Inf))
	}
	return d
}

// gpuClassKey reads the driver in use from the card's driver key.
func gpuClassKey(drvKey string) (gpuDriverPkg, uint64) {
	var p gpuDriverPkg
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Class\`+drvKey, registry.QUERY_VALUE)
	if err != nil {
		return p, 0
	}
	defer k.Close()
	get := func(n string) string { s, _, _ := k.GetStringValue(n); return strings.TrimSpace(s) }
	p.Inf, p.Version, p.Provider, p.Desc = strings.ToLower(get("InfPath")), get("DriverVersion"), get("ProviderName"), get("DriverDesc")
	if t, err := time.Parse("1-2-2006", get("DriverDate")); err == nil {
		p.Date = t.Format("2006-01-02")
	}
	var vram uint64
	if v, _, err := k.GetIntegerValue("HardwareInformation.qwMemorySize"); err == nil {
		vram = v
	} else if b, _, err := k.GetBinaryValue("HardwareInformation.qwMemorySize"); err == nil && len(b) >= 8 {
		vram = leU64(b)
	} else if v, _, err := k.GetIntegerValue("HardwareInformation.MemorySize"); err == nil {
		vram = v
	} else if b, _, err := k.GetBinaryValue("HardwareInformation.MemorySize"); err == nil && len(b) >= 4 {
		vram = uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24
	}
	return p, vram
}

func leU64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// gpuPackages: the drivers on this PC that fit the card (Windows' own list
// of compatible drivers, as Device Manager's "let me pick" shows it).
func gpuPackages(devs windows.DevInfo, di *windows.DevInfoData, inUse string) []gpuDriverPkg {
	if devs.BuildDriverInfoList(di, windows.SPDIT_COMPATDRIVER) != nil {
		return nil
	}
	defer func() { _ = devs.DestroyDriverInfoList(di, windows.SPDIT_COMPATDRIVER) }()
	seen := map[string]int{}
	var out []gpuDriverPkg
	for i := 0; i < 200; i++ {
		drv, err := devs.EnumDriverInfo(di, windows.SPDIT_COMPATDRIVER, i)
		if err != nil {
			break
		}
		det, err := devs.DriverInfoDetail(di, drv)
		if err != nil {
			continue
		}
		inf := strings.ToLower(filepath.Base(det.InfFileName()))
		ver := winDriverVersion(drv.DriverVersion)
		if _, dup := seen[inf+"|"+ver]; dup {
			continue
		}
		p := gpuDriverPkg{Inf: inf, Version: ver, Provider: drv.ProviderName(), Desc: drv.Description()}
		if ns := drv.DriverDate.Nanoseconds(); ns > 0 {
			p.Date = time.Unix(0, ns).UTC().Format("2006-01-02")
		}
		p.Basic = !strings.HasPrefix(inf, "oem")
		p.InUse = inf == strings.ToLower(inUse)
		if !p.Basic {
			p.Size = driverStoreSize(inf)
		}
		seen[inf+"|"+ver] = len(out)
		out = append(out, p)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Basic != out[b].Basic {
			return !out[a].Basic
		}
		return out[a].Date > out[b].Date || out[a].Date == out[b].Date && out[a].Version > out[b].Version
	})
	return out
}

// gpuStoreDir: the driver store folder of a published INF (oem12.inf).
func gpuStoreDir(inf string) string {
	full, _ := windows.UTF16PtrFromString(j(P.windir, "INF", inf))
	buf := make([]uint16, 1024)
	var need uint32
	if r, _, _ := procSetupGetInfStoreLocation.Call(uintptr(unsafe.Pointer(full)), 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&need))); r != 0 {
		if p := windows.UTF16ToString(buf); p != "" {
			return filepath.Dir(p)
		}
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\DriverDatabase\DriverInfFiles\`+inf, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	pkgs, _, _ := k.GetStringsValue("")
	for i := len(pkgs) - 1; i >= 0; i-- {
		if p := pkgs[i]; p != "" && !strings.ContainsAny(p, `\/`) {
			if dir := j(P.windir, `System32\DriverStore\FileRepository`, p); isDir(dir) {
				return dir
			}
		}
	}
	return ""
}

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// ---------------------------------------------------------------- the page's first look

func gpuStateRead() gpuState {
	start := time.Now()
	s := gpuState{Admin: app.admin}
	devs, err := gpuDevices(true)
	if err != nil {
		s.Error = err.Error()
	}
	s.Devices = devs
	if s.Devices == nil {
		s.Devices = []gpuDevice{}
	}
	s.Displays = gpuDisplays()
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\GraphicsDrivers`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetIntegerValue("HwSchMode"); err == nil {
			s.HAGS = map[bool]string{true: "on", false: "off"}[v == 2]
		}
		k.Close()
	}
	s.WUDrivers = wuDriverSearch()
	_, s.WUByUs = getSettings()[gpuWUPrevKey]
	s.Backups = gpuBackups()
	s.Took = time.Since(start).Milliseconds()
	return s
}

// ---------------------------------------------------------------- screens

type displayDeviceW struct {
	cb     uint32
	name   [32]uint16
	str    [128]uint16
	flags  uint32
	id     [128]uint16
	keyReg [128]uint16
}

// devModeW is DEVMODEW (the display part of the union).
type devModeW struct {
	DeviceName       [32]uint16
	SpecVersion      uint16
	DriverVersion    uint16
	Size             uint16
	DriverExtra      uint16
	Fields           uint32
	PosX, PosY       int32
	Orientation      uint32
	FixedOutput      uint32
	Color            int16
	Duplex           int16
	YRes             int16
	TTOption         int16
	Collate          int16
	FormName         [32]uint16
	LogPixels        uint16
	BitsPerPel       uint32
	PelsWidth        uint32
	PelsHeight       uint32
	DisplayFlags     uint32
	DisplayFrequency uint32
	_                [8]uint32
}

const (
	ddAttached         = 0x1
	ddPrimary          = 0x4
	ddMirror           = 0x8
	enumCurrent        = 0xFFFFFFFF
	dmBitsPerPel       = 0x40000
	dmPelsWidth        = 0x80000
	dmPelsHeight       = 0x100000
	dmDisplayFrequency = 0x400000
	cdsUpdateRegistry  = 0x1
	cdsTest            = 0x2
)

func enumMode(dev *uint16, i uint32) (devModeW, bool) {
	dm := devModeW{Size: uint16(unsafe.Sizeof(devModeW{}))}
	r, _, _ := procEnumDisplaySettingsExW.Call(uintptr(unsafe.Pointer(dev)), uintptr(i), uintptr(unsafe.Pointer(&dm)), 0)
	return dm, r != 0
}

func gpuDisplays() []gpuDisplay {
	out := []gpuDisplay{}
	if procEnumDisplayDevicesW.Find() != nil || procEnumDisplaySettingsExW.Find() != nil {
		return out
	}
	names := displayConfigNames()
	for i := uint32(0); i < 16; i++ {
		ad := displayDeviceW{cb: uint32(unsafe.Sizeof(displayDeviceW{}))}
		if r, _, _ := procEnumDisplayDevicesW.Call(0, uintptr(i), uintptr(unsafe.Pointer(&ad)), 0); r == 0 {
			break
		}
		if ad.flags&ddAttached == 0 || ad.flags&ddMirror != 0 {
			continue
		}
		name := windows.UTF16ToString(ad.name[:])
		np, _ := windows.UTF16PtrFromString(name)
		cur, ok := enumMode(np, enumCurrent)
		if !ok || cur.PelsWidth == 0 {
			continue
		}
		d := gpuDisplay{Name: name, Adapter: windows.UTF16ToString(ad.str[:]), Primary: ad.flags&ddPrimary != 0,
			W: int(cur.PelsWidth), H: int(cur.PelsHeight), Hz: int(cur.DisplayFrequency)}
		mon := displayDeviceW{cb: uint32(unsafe.Sizeof(displayDeviceW{}))}
		if r, _, _ := procEnumDisplayDevicesW.Call(uintptr(unsafe.Pointer(np)), 0, uintptr(unsafe.Pointer(&mon)), 0); r != 0 {
			d.Monitor = windows.UTF16ToString(mon.str[:])
		}
		if n, ok := names[strings.ToUpper(name)]; ok {
			if n.friendly != "" {
				d.Monitor = n.friendly
			}
			d.Conn = n.conn
		}
		rates := map[int]bool{}
		for m := uint32(0); m < 4000; m++ {
			dm, ok := enumMode(np, m)
			if !ok {
				break
			}
			if dm.DisplayFlags&0x2 != 0 || dm.BitsPerPel != cur.BitsPerPel { // interlaced, or another colour depth
				continue
			}
			if int(dm.PelsWidth)*int(dm.PelsHeight) > d.MaxW*d.MaxH {
				d.MaxW, d.MaxH = int(dm.PelsWidth), int(dm.PelsHeight)
			}
			if dm.PelsWidth == cur.PelsWidth && dm.PelsHeight == cur.PelsHeight && dm.DisplayFrequency > 1 {
				rates[int(dm.DisplayFrequency)] = true
			}
		}
		for r := range rates {
			d.Rates = append(d.Rates, r)
			d.MaxHz = max(d.MaxHz, r)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(d.Rates)))
		if d.Rates == nil {
			d.Rates = []int{}
		}
		out = append(out, d)
	}
	return out
}

type dcPath struct {
	srcAdapter windows.LUID
	srcID      uint32
	srcMode    uint32
	srcFlags   uint32
	tgtAdapter windows.LUID
	tgtID      uint32
	tgtMode    uint32
	tech       uint32
	rotation   uint32
	scaling    uint32
	rateNum    uint32
	rateDen    uint32
	scanline   uint32
	available  int32
	tgtFlags   uint32
	flags      uint32
}

type dcHeader struct {
	typ     uint32
	size    uint32
	adapter windows.LUID
	id      uint32
}

type dcSourceName struct {
	hdr dcHeader
	gdi [32]uint16
}

type dcTargetName struct {
	hdr       dcHeader
	flags     uint32
	tech      uint32
	edidMan   uint16
	edidProd  uint16
	connector uint32
	friendly  [64]uint16
	path      [128]uint16
}

type dcName struct{ friendly, conn string }

// displayConfigNames: each screen's real name ("LG ULTRAGEAR") and how it is
// connected, by its GDI name (\\.\DISPLAY1).
func displayConfigNames() map[string]dcName {
	out := map[string]dcName{}
	if procQueryDisplayConfig.Find() != nil || procGetDisplayConfigBufSizes.Find() != nil || procDisplayConfigGetInfo.Find() != nil {
		return out
	}
	const qdcOnlyActive = 2
	var np, nm uint32
	if r, _, _ := procGetDisplayConfigBufSizes.Call(qdcOnlyActive, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 || np == 0 {
		return out
	}
	paths := make([]dcPath, np)
	modes := make([][64]byte, nm+1)
	if r, _, _ := procQueryDisplayConfig.Call(qdcOnlyActive, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return out
	}
	for _, p := range paths[:np] {
		src := dcSourceName{hdr: dcHeader{typ: 1, size: uint32(unsafe.Sizeof(dcSourceName{})), adapter: p.srcAdapter, id: p.srcID}}
		if r, _, _ := procDisplayConfigGetInfo.Call(uintptr(unsafe.Pointer(&src))); r != 0 {
			continue
		}
		tgt := dcTargetName{hdr: dcHeader{typ: 2, size: uint32(unsafe.Sizeof(dcTargetName{})), adapter: p.tgtAdapter, id: p.tgtID}}
		n := dcName{conn: connName(p.tech)}
		if r, _, _ := procDisplayConfigGetInfo.Call(uintptr(unsafe.Pointer(&tgt))); r == 0 {
			n.friendly = strings.TrimSpace(windows.UTF16ToString(tgt.friendly[:]))
		}
		out[strings.ToUpper(windows.UTF16ToString(src.gdi[:]))] = n
	}
	return out
}

func connName(tech uint32) string {
	switch tech {
	case 0:
		return "vga"
	case 4:
		return "dvi"
	case 5:
		return "hdmi"
	case 10, 12:
		return "dp"
	case 6, 11, 13, 0x80000000:
		return "internal"
	case 15:
		return "wireless"
	case 16, 17:
		return "virtual"
	}
	return ""
}

// The refresh rate: set, and put back by itself after 15 s unless the page
// says to keep it (like Windows' own "Keep these display settings?").
var refresh struct {
	sync.Mutex
	timer *time.Timer
	dev   string
	op    *opHandle
}

func gpuSetRefresh(dev string, hz int) gpuResult {
	var d *gpuDisplay
	ds := gpuDisplays()
	for i := range ds {
		if strings.EqualFold(ds[i].Name, dev) {
			d = &ds[i]
		}
	}
	if d == nil {
		return gpuResult{Error: T("gpu.err.noDisplay")}
	}
	okRate := false
	for _, r := range d.Rates {
		okRate = okRate || r == hz
	}
	if !okRate {
		return gpuResult{Error: T("gpu.err.rate", hz)}
	}
	if d.Hz == hz {
		return gpuResult{OK: true}
	}
	refresh.Lock()
	defer refresh.Unlock()
	if refresh.timer != nil { // a change still waiting for "Keep" counts as kept
		refresh.timer.Stop()
		refresh.op.end(T("log.gpuRateKept"), nil)
		refresh.timer, refresh.dev, refresh.op = nil, "", nil
	}
	op := beginOp("gpu", d.Monitor, T("log.gpuRate", d.Monitor, d.Hz, hz), map[string]any{"display": dev, "from": d.Hz, "to": hz})
	if err := applyRate(dev, uint32(hz)); err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return gpuResult{Error: err.Error()}
	}
	old := uint32(d.Hz)
	refresh.dev, refresh.op = dev, op
	var t *time.Timer
	t = time.AfterFunc(15*time.Second, func() {
		refresh.Lock()
		defer refresh.Unlock()
		if refresh.timer != t {
			return
		}
		refresh.timer, refresh.dev, refresh.op = nil, "", nil
		err := applyRate(dev, old)
		if err != nil {
			op.fail("", err.Error(), nil)
		}
		op.warnf("", T("log.gpuRateBack", old), nil)
		op.end("", nil)
		emit("gpu-refresh", map[string]any{"display": dev, "reverted": true, "hz": old, "ok": err == nil})
	})
	refresh.timer = t
	return gpuResult{OK: true, Seconds: 15}
}

func gpuKeepRefresh(dev string) bool {
	refresh.Lock()
	defer refresh.Unlock()
	if refresh.timer == nil || !strings.EqualFold(refresh.dev, dev) {
		return false
	}
	refresh.timer.Stop()
	refresh.op.end(T("log.gpuRateKept"), nil)
	refresh.timer, refresh.dev, refresh.op = nil, "", nil
	return true
}

func applyRate(dev string, hz uint32) error {
	np, _ := windows.UTF16PtrFromString(dev)
	dm, ok := enumMode(np, enumCurrent)
	if !ok {
		return errors.New(T("gpu.err.noDisplay"))
	}
	dm.Fields = dmPelsWidth | dmPelsHeight | dmBitsPerPel | dmDisplayFrequency
	dm.DisplayFrequency = hz
	if r, _, _ := procChangeDisplaySettingsExW.Call(uintptr(unsafe.Pointer(np)), uintptr(unsafe.Pointer(&dm)), 0, cdsTest, 0); int32(r) != 0 {
		return errors.New(T("gpu.err.rateRefused", int32(r)))
	}
	if r, _, _ := procChangeDisplaySettingsExW.Call(uintptr(unsafe.Pointer(np)), uintptr(unsafe.Pointer(&dm)), 0, cdsUpdateRegistry, 0); int32(r) < 0 {
		return errors.New(T("gpu.err.rateRefused", int32(r)))
	}
	return nil
}

// ---------------------------------------------------------------- sensors

var nvx struct {
	once                                          sync.Once
	power, limit, fan, clock, maxClock            *windows.LazyProc
	gen, width, maxGen, maxWidth, bar1, pstate    *windows.LazyProc
	reasons, reasonsOld, vbios, sysDrv, cuda, thr *windows.LazyProc
	enc, dec                                      *windows.LazyProc
	static                                        gpuSensors // what does not change while the app runs
}

func nvmlCall(p *windows.LazyProc, args ...uintptr) bool {
	if p == nil || p.Find() != nil {
		return false
	}
	r, _, _ := p.Call(args...)
	return r == 0
}

func nvxStart() {
	if !nvml.ok || nvml.lib == nil {
		return
	}
	l := nvml.lib
	nvx.power, nvx.limit, nvx.fan = l.NewProc("nvmlDeviceGetPowerUsage"), l.NewProc("nvmlDeviceGetEnforcedPowerLimit"), l.NewProc("nvmlDeviceGetFanSpeed")
	nvx.clock, nvx.maxClock = l.NewProc("nvmlDeviceGetClockInfo"), l.NewProc("nvmlDeviceGetMaxClockInfo")
	nvx.gen, nvx.width = l.NewProc("nvmlDeviceGetCurrPcieLinkGeneration"), l.NewProc("nvmlDeviceGetCurrPcieLinkWidth")
	nvx.maxGen, nvx.maxWidth = l.NewProc("nvmlDeviceGetMaxPcieLinkGeneration"), l.NewProc("nvmlDeviceGetMaxPcieLinkWidth")
	nvx.bar1, nvx.pstate = l.NewProc("nvmlDeviceGetBAR1MemoryInfo"), l.NewProc("nvmlDeviceGetPerformanceState")
	nvx.reasons, nvx.reasonsOld = l.NewProc("nvmlDeviceGetCurrentClocksEventReasons"), l.NewProc("nvmlDeviceGetCurrentClocksThrottleReasons")
	nvx.vbios, nvx.sysDrv, nvx.cuda = l.NewProc("nvmlDeviceGetVbiosVersion"), l.NewProc("nvmlSystemGetDriverVersion"), l.NewProc("nvmlSystemGetCudaDriverVersion_v2")
	nvx.thr, nvx.enc, nvx.dec = l.NewProc("nvmlDeviceGetTemperatureThreshold"), l.NewProc("nvmlDeviceGetEncoderUtilization"), l.NewProc("nvmlDeviceGetDecoderUtilization")
	dev := nvml.dev
	st := &nvx.static
	var u uint32
	if nvmlCall(nvx.maxClock, dev, 0, uintptr(unsafe.Pointer(&u))) {
		st.MaxClock = int(u)
	}
	if nvmlCall(nvx.maxClock, dev, 2, uintptr(unsafe.Pointer(&u))) {
		st.MaxMemClock = int(u)
	}
	if nvmlCall(nvx.maxGen, dev, uintptr(unsafe.Pointer(&u))) {
		st.PCIeMaxGen = int(u)
	}
	if nvmlCall(nvx.maxWidth, dev, uintptr(unsafe.Pointer(&u))) {
		st.PCIeMaxW = int(u)
	}
	if nvmlCall(nvx.thr, dev, 1, uintptr(unsafe.Pointer(&u))) && u < 150 { // NVML_TEMPERATURE_THRESHOLD_SLOWDOWN
		st.SlowTemp = int(u)
	}
	var b1 struct{ total, free, used uint64 }
	if nvmlCall(nvx.bar1, dev, uintptr(unsafe.Pointer(&b1))) {
		st.BAR1, st.ReBAR = b1.total, rebarState(b1.total)
	}
	buf := make([]byte, 96)
	if nvmlCall(nvx.vbios, dev, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) {
		st.VBIOS = windows.ByteSliceToString(buf)
	}
	buf = make([]byte, 96)
	if nvmlCall(nvx.sysDrv, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) {
		st.Driver = windows.ByteSliceToString(buf)
	}
	var cv int32
	if nvmlCall(nvx.cuda, uintptr(unsafe.Pointer(&cv))) && cv > 0 {
		st.CUDA = fmt.Sprintf("%d.%d", cv/1000, cv%1000/10)
	}
}

// gpuSensorsRead: the card's live numbers (NVIDIA in full, others: load
// and memory from Windows' counters).
func gpuSensorsRead() gpuSensors {
	g := gpuReading()
	s := gpuSensors{OK: g.OK, Src: g.Src, Name: g.Name, Util: g.Util, Temp: g.Temp, MemUsed: g.MemUsed, MemTotal: g.MemTotal, Fan: -1, PState: -1, Enc: -1, Dec: -1}
	if g.Src == "nvml" {
		nvx.once.Do(nvxStart)
		st := nvx.static
		s.MaxClock, s.MaxMemClock, s.PCIeMaxGen, s.PCIeMaxW, s.SlowTemp = st.MaxClock, st.MaxMemClock, st.PCIeMaxGen, st.PCIeMaxW, st.SlowTemp
		s.BAR1, s.ReBAR, s.VBIOS, s.Driver, s.CUDA = st.BAR1, st.ReBAR, st.VBIOS, st.Driver, st.CUDA
		dev := nvml.dev
		var u, per uint32
		if nvmlCall(nvx.power, dev, uintptr(unsafe.Pointer(&u))) {
			s.Power = float64(u) / 1000
		}
		if nvmlCall(nvx.limit, dev, uintptr(unsafe.Pointer(&u))) {
			s.PowerLimit = float64(u) / 1000
		}
		if nvmlCall(nvx.fan, dev, uintptr(unsafe.Pointer(&u))) && u <= 100 {
			s.Fan = int(u)
		}
		if nvmlCall(nvx.clock, dev, 0, uintptr(unsafe.Pointer(&u))) {
			s.Clock = int(u)
		}
		if nvmlCall(nvx.clock, dev, 2, uintptr(unsafe.Pointer(&u))) {
			s.MemClock = int(u)
		}
		if nvmlCall(nvx.gen, dev, uintptr(unsafe.Pointer(&u))) {
			s.PCIeGen = int(u)
		}
		if nvmlCall(nvx.width, dev, uintptr(unsafe.Pointer(&u))) {
			s.PCIeWidth = int(u)
		}
		var ps int32
		if nvmlCall(nvx.pstate, dev, uintptr(unsafe.Pointer(&ps))) && ps >= 0 && ps <= 15 {
			s.PState = int(ps)
		}
		var mask uint64
		if nvmlCall(nvx.reasons, dev, uintptr(unsafe.Pointer(&mask))) || nvmlCall(nvx.reasonsOld, dev, uintptr(unsafe.Pointer(&mask))) {
			s.Limits = nvLimits(mask)
		}
		if nvmlCall(nvx.enc, dev, uintptr(unsafe.Pointer(&u)), uintptr(unsafe.Pointer(&per))) && u <= 100 {
			s.Enc = int(u)
		}
		if nvmlCall(nvx.dec, dev, uintptr(unsafe.Pointer(&u)), uintptr(unsafe.Pointer(&per))) && u <= 100 {
			s.Dec = int(u)
		}
		return s
	}
	if _, mem, ok := pdhRead(); ok && mem > 0 {
		s.MemUsed = mem
	}
	return s
}

// ---------------------------------------------------------------- Windows Update and drivers

// wuDriverSearch: may Windows download a driver for a device by itself?
// (System → Device installation settings; DDU turns the same switch off.)
func wuDriverSearch() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, driverSearchKey, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SearchOrderConfig")
	return err != nil || v != 0
}

// gpuWU turns Windows' own driver downloads off (on = false) or back on.
// Only on a click: the value it had before is kept and put back.
func gpuWU(allow bool) gpuResult {
	if !app.admin {
		return gpuResult{Error: T("blocked.admin")}
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, driverSearchKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return gpuResult{Error: err.Error()}
	}
	defer k.Close()
	if !allow {
		prev := int64(-1)
		if v, _, err := k.GetIntegerValue("SearchOrderConfig"); err == nil {
			prev = int64(v)
		}
		if err := k.SetDWordValue("SearchOrderConfig", 0); err != nil {
			return gpuResult{Error: err.Error()}
		}
		updateSettings(func(m map[string]any) {
			if _, had := m[gpuWUPrevKey]; !had {
				m[gpuWUPrevKey] = prev
			}
		})
		logEvent("gpu", "info", "", T("log.gpuWUOff"), map[string]any{"before": prev})
		return gpuResult{OK: true}
	}
	prev := int64(1)
	if v, ok := getSettings()[gpuWUPrevKey].(float64); ok {
		prev = int64(v)
	}
	switch {
	case prev < 0:
		err = k.DeleteValue("SearchOrderConfig")
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
	case prev == 0:
		err = k.SetDWordValue("SearchOrderConfig", 1)
	default:
		err = k.SetDWordValue("SearchOrderConfig", uint32(prev))
	}
	if err != nil {
		return gpuResult{Error: err.Error()}
	}
	updateSettings(func(m map[string]any) { delete(m, gpuWUPrevKey) })
	logEvent("gpu", "info", "", T("log.gpuWUOn"), nil)
	return gpuResult{OK: true}
}

// ---------------------------------------------------------------- driver changes

type gpuOpts struct {
	Device       string `json:"device"` // the device instance
	Inf          string `json:"inf"`
	Version      string `json:"version"`
	All          bool   `json:"all"`     // every version of the driver on this PC, not only the one in use
	Keep         bool   `json:"keep"`    // a copy of the driver in use first
	BlockWU      bool   `json:"blockWU"` // and Windows does not download one by itself
	RestorePoint bool   `json:"restorePoint"`
}

func gpuStep(step string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["step"] = step
	emit("gpu-step", data)
}

// gpuFind opens the display devices and finds one by its instance ID.
func gpuFind(id string) (windows.DevInfo, *windows.DevInfoData, error) {
	devs, err := windows.SetupDiGetClassDevsEx(&guidDisplayClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return devs, nil, err
	}
	for i := 0; i < 32; i++ {
		di, err := devs.EnumDeviceInfo(i)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			continue
		}
		if got, _ := devs.DeviceInstanceID(di); strings.EqualFold(got, id) {
			return devs, di, nil
		}
	}
	devs.Close()
	return 0, nil, errors.New(T("gpu.err.noCard"))
}

func gpuRP(op *opHandle, want bool) {
	if !want || !app.admin {
		return
	}
	gpuStep("rp", nil)
	app.critical.Store(true)
	ok := restorePointTicking(func(sec int) { gpuStep("rp", map[string]any{"secs": sec}) })
	app.critical.Store(false)
	if ok {
		op.info("", T("log.rpDone"), nil)
	} else {
		op.warnf("", T("log.rpFail"), nil)
	}
}

// gpuNow: the driver the card has now (after a change).
func gpuNow(id string) string {
	ds, _ := gpuDevices(false)
	for _, d := range ds {
		if strings.EqualFold(d.ID, id) {
			if d.NoDriver {
				return T("gpu.basic")
			}
			v := d.Driver.Version
			if d.Driver.VendorV != "" {
				v = d.Driver.VendorV + " (" + v + ")"
			}
			return strings.TrimSpace(d.Driver.Provider + " " + v)
		}
	}
	return ""
}

// gpuUseDriver puts a driver that is on this PC already on the card (going
// back to an older version), as Device Manager does with "Have disk".
func gpuUseDriver(o gpuOpts) (res gpuResult) {
	if !app.admin {
		return gpuResult{Error: T("blocked.admin")}
	}
	if !gpuMu.TryLock() {
		return gpuResult{Error: T("gpu.err.busy")}
	}
	defer gpuMu.Unlock()
	app.working.Store(true)
	defer app.working.Store(false)
	op := beginOp("gpu", o.Version, T("log.gpuUse", o.Version), map[string]any{"device": o.Device, "inf": o.Inf, "restorePoint": o.RestorePoint})
	defer func() {
		if r := recover(); r != nil {
			res = gpuResult{Error: T("err.unexpected", r)}
			op.panicked(r)
		}
		if res.OK {
			op.info("", T("log.gpuNow", res.Now), map[string]any{"reboot": res.Reboot})
		} else {
			op.fail("", res.Error, nil)
		}
		op.end("", nil)
	}()
	if procDiInstallDevice.Find() != nil {
		return gpuResult{Error: T("gpu.err.api")}
	}
	devs, di, err := gpuFind(o.Device)
	if err != nil {
		return gpuResult{Error: err.Error()}
	}
	defer devs.Close()
	if err := devs.BuildDriverInfoList(di, windows.SPDIT_COMPATDRIVER); err != nil {
		return gpuResult{Error: err.Error()}
	}
	defer func() { _ = devs.DestroyDriverInfoList(di, windows.SPDIT_COMPATDRIVER) }()
	var pick *windows.DrvInfoData
	for i := 0; i < 200 && pick == nil; i++ {
		drv, err := devs.EnumDriverInfo(di, windows.SPDIT_COMPATDRIVER, i)
		if err != nil {
			break
		}
		det, err := devs.DriverInfoDetail(di, drv)
		if err == nil && strings.EqualFold(filepath.Base(det.InfFileName()), o.Inf) && winDriverVersion(drv.DriverVersion) == o.Version {
			pick = drv
		}
	}
	if pick == nil {
		return gpuResult{Error: T("gpu.err.noPkg")}
	}
	gpuRP(op, o.RestorePoint)
	gpuStep("install", map[string]any{"version": o.Version})
	app.critical.Store(true)
	var reboot int32
	r, _, e := procDiInstallDevice.Call(0, uintptr(devs), uintptr(unsafe.Pointer(di)), uintptr(unsafe.Pointer(pick)), 2, uintptr(unsafe.Pointer(&reboot))) // DIIDFLAG_NOFINISHINSTALLUI
	app.critical.Store(false)
	if r == 0 {
		return gpuResult{Error: T("gpu.err.install", e)}
	}
	return gpuResult{OK: true, Reboot: reboot != 0, Now: gpuNow(o.Device)}
}

// gpuDeletePackage removes a driver that no card uses from the driver store
// (Windows refuses one that is in use).
func gpuDeletePackage(inf string) gpuResult {
	if !app.admin {
		return gpuResult{Error: T("blocked.admin")}
	}
	inf = strings.ToLower(filepath.Base(inf))
	if !strings.HasPrefix(inf, "oem") || !strings.HasSuffix(inf, ".inf") {
		return gpuResult{Error: T("gpu.err.noPkg")}
	}
	if displayInfsInUse()[inf] {
		return gpuResult{Error: T("gpu.err.inUse")}
	}
	if !gpuMu.TryLock() {
		return gpuResult{Error: T("gpu.err.busy")}
	}
	defer gpuMu.Unlock()
	size := driverStoreSize(inf)
	op := beginOp("gpu", inf, T("log.gpuDelete", inf), map[string]any{"size": size})
	if err := windows.SetupUninstallOEMInf(inf, 0); err != nil {
		op.fail(inf, err.Error(), nil)
		op.end("", nil)
		return gpuResult{Error: T("gpu.err.delete", err.Error())}
	}
	op.info(inf, T("log.gpuDeleted", sizeText(size)), map[string]any{"freed": size})
	op.end("", nil)
	return gpuResult{OK: true}
}

// gpuRemove takes the driver off a card, like DDU: (a copy first), the
// older versions on this PC (if asked), then the one in use; the card then
// runs on Windows' basic driver (or an older version left on the PC).
func gpuRemove(o gpuOpts) (res gpuResult) {
	if !app.admin {
		return gpuResult{Error: T("blocked.admin")}
	}
	if !gpuMu.TryLock() {
		return gpuResult{Error: T("gpu.err.busy")}
	}
	defer gpuMu.Unlock()
	ds, err := gpuDevices(true)
	if err != nil {
		return gpuResult{Error: err.Error()}
	}
	var dev *gpuDevice
	for i := range ds {
		if strings.EqualFold(ds[i].ID, o.Device) {
			dev = &ds[i]
		}
	}
	if dev == nil {
		return gpuResult{Error: T("gpu.err.noCard")}
	}
	plan := gpuRemovePlan(*dev, o.All)
	if len(plan) == 0 {
		return gpuResult{Error: T("gpu.err.nothing")}
	}
	app.working.Store(true)
	defer app.working.Store(false)
	op := beginOp("gpu", dev.Name, T("log.gpuRemove", dev.Name), map[string]any{"device": dev.ID, "all": o.All, "keep": o.Keep, "blockWU": o.BlockWU, "restorePoint": o.RestorePoint, "packages": len(plan)})
	defer func() {
		if r := recover(); r != nil {
			res = gpuResult{Error: T("err.unexpected", r)}
			op.panicked(r)
		}
		if !res.OK {
			op.fail("", res.Error, nil)
		}
		op.end(T("log.gpuNow", res.Now), map[string]any{"reboot": res.Reboot})
	}()
	gpuRP(op, o.RestorePoint)
	if o.Keep && !dev.NoDriver {
		gpuStep("copy", map[string]any{"done": 0, "total": dev.Driver.Size})
		dir, err := gpuBackupDriver(*dev)
		if err != nil {
			return gpuResult{Error: T("gpu.err.copy", err.Error())}
		}
		res.Backup = dir
		op.info("", T("log.gpuCopied", dir), map[string]any{"dir": dir})
	}
	app.critical.Store(true)
	defer app.critical.Store(false)
	for i, p := range plan {
		gpuStep("remove", map[string]any{"n": i + 1, "of": len(plan), "version": p.Version})
		inUse := p.InUse || strings.EqualFold(p.Inf, dev.Driver.Inf)
		reboot, err := removeDriverPkg(p.Inf, inUse)
		if err != nil {
			op.fail(p.Inf, T("gpu.err.remove", p.Version, err.Error()), nil)
			if inUse {
				res.Error = T("gpu.err.remove", p.Version, err.Error())
				res.Now = gpuNow(dev.ID)
				return res
			}
			continue
		}
		res.Reboot = res.Reboot || reboot
		res.Done = append(res.Done, p.Version)
		op.info(p.Inf, T("log.gpuRemoved", p.Version), map[string]any{"inUse": inUse, "reboot": reboot})
	}
	if o.BlockWU {
		gpuStep("wu", nil)
		if r := gpuWU(false); !r.OK {
			op.warnf("", r.Error, nil)
		}
	}
	res.OK = true
	res.Now = gpuNow(dev.ID)
	return res
}

// removeDriverPkg: one driver package off the PC. The one in use is taken
// off the card first (Windows' DiUninstallDriver, as "pnputil /delete-driver
// /uninstall" does); one not in use is only removed from the store.
func removeDriverPkg(inf string, inUse bool) (reboot bool, err error) {
	if !inUse {
		return false, windows.SetupUninstallOEMInf(inf, 0)
	}
	if procDiUninstallDriverW.Find() == nil {
		p, _ := windows.UTF16PtrFromString(j(P.windir, "INF", inf))
		var rb int32
		r, _, e := procDiUninstallDriverW.Call(0, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&rb)))
		if r != 0 {
			return rb != 0, nil
		}
		err = e
	}
	out, code, rerr := runQuiet(10*time.Minute, sys32("pnputil.exe"), "/delete-driver", inf, "/uninstall", "/force")
	switch {
	case code == 0:
		return false, nil
	case code == 3010:
		return true, nil
	case rerr != nil && code < 0:
		return false, rerr
	}
	if err == nil {
		err = fmt.Errorf("pnputil %d: %s", code, firstLine(out))
	}
	return false, err
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// ---------------------------------------------------------------- copies of drivers

func gpuBackupRoot() string { return j(P.local, "xKi4oCleaner", "drivers") }

// gpuBackupDriver copies the driver store folder of the driver in use.
func gpuBackupDriver(d gpuDevice) (string, error) {
	src := gpuStoreDir(strings.ToLower(d.Driver.Inf))
	if src == "" {
		return "", errors.New(T("gpu.err.noStore"))
	}
	size, _ := sizeOf(planned{src, mustStat(src)})
	if free := freeOn(gpuBackupRoot()); free > 0 && free < uint64(size)+(1<<30) {
		return "", errors.New(T("gpu.err.space", sizeText(size)))
	}
	dst := j(gpuBackupRoot(), gpuBackupDirName(d.Vendor, d.Driver.Version, d.Driver.Date, time.Now()))
	var done int64
	last := time.Now()
	err := filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := j(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		if e.Type()&os.ModeSymlink != 0 {
			return nil
		}
		n, err := copyOne(p, to)
		done += n
		if time.Since(last) > 250*time.Millisecond {
			last = time.Now()
			gpuStep("copy", map[string]any{"done": done, "total": size})
		}
		return err
	})
	if err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	infs, _ := filepath.Glob(j(dst, "*.inf"))
	if len(infs) == 0 {
		_ = os.RemoveAll(dst)
		return "", errors.New(T("gpu.err.noStore"))
	}
	meta := gpuBackup{Name: d.Name, Vendor: d.Vendor, Version: d.Driver.Version, VendorV: d.Driver.VendorV, Date: d.Driver.Date,
		Inf: filepath.Base(infs[0]), Size: done, Made: time.Now().UnixMilli()}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(j(dst, "xki4o-driver.json"), b, 0o644); err != nil {
		_ = os.RemoveAll(dst)
		return "", err
	}
	return dst, nil
}

func mustStat(p string) os.FileInfo { fi, _ := os.Lstat(p); return fi }

func copyOne(from, to string) (int64, error) {
	in, err := os.Open(from)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// freeOn: free bytes on the drive of a folder (made if needed).
func freeOn(dir string) uint64 {
	_ = os.MkdirAll(dir, 0o755)
	_, f := diskSpace(filepath.VolumeName(dir) + `\`)
	return f
}

func gpuBackups() []gpuBackup {
	out := []gpuBackup{}
	dirs, _ := os.ReadDir(gpuBackupRoot())
	for _, e := range dirs {
		if !e.IsDir() {
			continue
		}
		dir := j(gpuBackupRoot(), e.Name())
		b, err := os.ReadFile(j(dir, "xki4o-driver.json"))
		if err != nil {
			continue
		}
		var m gpuBackup
		if json.Unmarshal(b, &m) != nil || m.Inf == "" {
			continue
		}
		m.Dir = dir
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Made > out[b].Made })
	return out
}

// gpuBackupPath: only folders in the app's own copies folder.
func gpuBackupPath(dir string) (gpuBackup, bool) {
	for _, b := range gpuBackups() {
		if strings.EqualFold(filepath.Clean(b.Dir), filepath.Clean(dir)) {
			return b, true
		}
	}
	return gpuBackup{}, false
}

// gpuRestoreBackup puts a copied driver back: into the driver store and on
// the cards it fits (even if Windows has a "better" one).
func gpuRestoreBackup(dir string, rp bool) (res gpuResult) {
	if !app.admin {
		return gpuResult{Error: T("blocked.admin")}
	}
	b, ok := gpuBackupPath(dir)
	if !ok {
		return gpuResult{Error: T("gpu.err.noCopy")}
	}
	if !gpuMu.TryLock() {
		return gpuResult{Error: T("gpu.err.busy")}
	}
	defer gpuMu.Unlock()
	app.working.Store(true)
	defer app.working.Store(false)
	op := beginOp("gpu", b.Version, T("log.gpuPutBack", b.Version), map[string]any{"dir": b.Dir})
	defer func() {
		if res.OK {
			op.info("", T("log.gpuNow", res.Now), map[string]any{"reboot": res.Reboot})
		} else {
			op.fail("", res.Error, nil)
		}
		op.end("", nil)
	}()
	gpuRP(op, rp)
	gpuStep("install", map[string]any{"version": b.Version})
	app.critical.Store(true)
	defer app.critical.Store(false)
	inf := j(b.Dir, b.Inf)
	if procDiInstallDriverW.Find() == nil {
		p, _ := windows.UTF16PtrFromString(inf)
		var rb int32
		if r, _, _ := procDiInstallDriverW.Call(0, uintptr(unsafe.Pointer(p)), 0x2, uintptr(unsafe.Pointer(&rb))); r != 0 { // DIIRFLAG_FORCE_INF
			return gpuResult{OK: true, Reboot: rb != 0, Now: gpuNowAny()}
		}
	}
	out, code, err := runQuiet(15*time.Minute, sys32("pnputil.exe"), "/add-driver", inf, "/install")
	if code == 0 || code == 3010 {
		return gpuResult{OK: true, Reboot: code == 3010, Now: gpuNowAny()}
	}
	if err != nil && code < 0 {
		return gpuResult{Error: T("gpu.err.install", err)}
	}
	return gpuResult{Error: T("gpu.err.install", firstLine(out))}
}

// gpuNowAny: the first real card's driver.
func gpuNowAny() string {
	ds, _ := gpuDevices(false)
	for _, d := range ds {
		if !d.Virtual {
			return gpuNow(d.ID)
		}
	}
	return ""
}

// gpuOpenBackups shows the copies' folder in Explorer.
func gpuOpenBackups() bool {
	_ = os.MkdirAll(gpuBackupRoot(), 0o755)
	shellOpen(gpuBackupRoot())
	return true
}

func gpuDeleteBackup(dir string) bool {
	b, ok := gpuBackupPath(dir)
	if !ok {
		return false
	}
	err := os.RemoveAll(b.Dir)
	logEvent("gpu", map[bool]string{true: "info", false: "warn"}[err == nil], b.Version, T("log.gpuCopyDeleted", b.Dir), nil)
	return err == nil
}

// ---------------------------------------------------------------- NVIDIA's driver archive

var nvHTTP = &http.Client{Timeout: 25 * time.Second}

const (
	nvProductsURL = "https://www.nvidia.com/Download/API/lookupValueSearch.aspx?TypeID=3"
	nvLookupURL   = "https://gfwsl.geforce.com/services_toolkit/services/com/nvidia/services/AjaxDriverService.php?func=DriverManualLookup&psid=%d&pfid=%d&osID=57&languageCode=1033&isWHQL=1&dch=1&sort1=0&numberOfResults=%d"
	nvMaxSize     = 3 << 30
)

type nvList struct {
	Card      string     `json:"card"`
	Product   string     `json:"product,omitempty"`
	Series    int        `json:"psid,omitempty"`
	ID        int        `json:"pfid,omitempty"`
	Notebook  bool       `json:"notebook"`
	Installed string     `json:"installed,omitempty"`
	Latest    string     `json:"latest,omitempty"`
	Newer     bool       `json:"newer"`
	Drivers   []nvDriver `json:"drivers"`
	Error     string     `json:"error,omitempty"`
	At        int64      `json:"at"`
}

var nvCache struct {
	sync.Mutex
	prods []nvProduct
	list  *nvList
	urls  map[string]string // download URL → version, from the last list
}

func nvGet(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+appVersion)
	resp, err := nvHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVIDIA: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func hasBattery() bool {
	var st struct {
		acLine, flag, percent, saver uint8
		life, full                   uint32
	}
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return false
	}
	return st.flag != 128 && st.flag != 255
}

// gpuNvDrivers: the drivers NVIDIA has for this card, newest first.
func gpuNvDrivers(force bool) nvList {
	nvCache.Lock()
	defer nvCache.Unlock()
	if !force && nvCache.list != nil && time.Since(time.UnixMilli(nvCache.list.At)) < 30*time.Minute {
		return *nvCache.list
	}
	l := nvList{Drivers: []nvDriver{}, At: time.Now().UnixMilli()}
	ds, _ := gpuDevices(false)
	for _, d := range ds {
		if d.Vendor == "nvidia" && !d.Virtual {
			l.Card, l.Installed = d.Name, d.Driver.VendorV
			break
		}
	}
	if l.Card == "" {
		l.Error = T("gpu.err.noNvidia")
		return l
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if nvCache.prods == nil {
		b, err := nvGet(ctx, nvProductsURL, 8<<20)
		if err == nil {
			nvCache.prods, err = nvProducts(b)
		}
		if err != nil {
			l.Error = T("gpu.err.nvNet", err.Error())
			return l
		}
	}
	cands := nvMatch(nvCache.prods, l.Card)
	if len(cands) == 0 {
		l.Error = T("gpu.err.nvCard", l.Card)
		return l
	}
	pick := cands[0]
	l.Notebook = hasBattery() || strings.Contains(strings.ToLower(l.Card), "laptop")
	if len(cands) > 1 {
		for _, c := range cands {
			b, err := nvGet(ctx, fmt.Sprintf(nvLookupURL, c.Series, c.ID, 1), 2<<20)
			if err != nil {
				continue
			}
			if ds, err := nvDrivers(b); err == nil && len(ds) > 0 && nvIsNotebook(ds[0].URL) == l.Notebook {
				pick = c
				break
			}
		}
	}
	l.Product, l.Series, l.ID = pick.Name, pick.Series, pick.ID
	b, err := nvGet(ctx, fmt.Sprintf(nvLookupURL, pick.Series, pick.ID, 30), 8<<20)
	if err == nil {
		l.Drivers, err = nvDrivers(b)
	}
	if err != nil {
		l.Error = T("gpu.err.nvNet", err.Error())
		l.Drivers = []nvDriver{}
		return l
	}
	nvCache.urls = map[string]string{}
	for _, d := range l.Drivers {
		nvCache.urls[d.URL] = d.Version
	}
	if len(l.Drivers) > 0 {
		l.Latest = l.Drivers[0].Version
		for _, d := range l.Drivers {
			if !d.Studio {
				l.Latest = d.Version
				break
			}
		}
		l.Newer = l.Installed != "" && nvNumber(l.Latest) > nvNumber(l.Installed)
	}
	nvCache.list = &l
	return l
}

var nvDL struct {
	sync.Mutex
	busy   bool
	cancel context.CancelFunc
	files  map[string]bool // what this app downloaded and checked (only those can be started)
}

func nvSafeName(v string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r == '.' {
			return r
		}
		return -1
	}, v)
}

// gpuNvDownload saves one of the drivers of the last list in Downloads and
// checks that NVIDIA signed it.
func gpuNvDownload(u string) gpuResult {
	nvCache.Lock()
	ver, listed := nvCache.urls[u]
	nvCache.Unlock()
	if !listed || !nvDownloadOK(u) {
		return gpuResult{Error: T("gpu.err.nvURL")}
	}
	nvDL.Lock()
	if nvDL.busy {
		nvDL.Unlock()
		return gpuResult{Error: T("gpu.err.busy")}
	}
	ctx, cancel := context.WithCancel(context.Background())
	nvDL.busy, nvDL.cancel = true, cancel
	nvDL.Unlock()
	defer func() {
		cancel()
		nvDL.Lock()
		nvDL.busy, nvDL.cancel = false, nil
		nvDL.Unlock()
	}()
	op := beginOp("gpu", ver, T("log.gpuDownload", ver), map[string]any{"url": u})
	res := nvDownloadTo(ctx, u, ver)
	if res.OK {
		op.info("", T("log.gpuDownloaded", res.Path, res.Signer), map[string]any{"path": res.Path})
		nvDL.Lock()
		if nvDL.files == nil {
			nvDL.files = map[string]bool{}
		}
		nvDL.files[strings.ToLower(res.Path)] = true
		nvDL.Unlock()
	} else {
		op.fail("", res.Error, nil)
	}
	op.end("", nil)
	return res
}

func nvDownloadTo(ctx context.Context, u, ver string) gpuResult {
	dir := knownFolder(windows.FOLDERID_Downloads, j(P.profile, "Downloads"))
	dst := j(dir, "NVIDIA driver "+nvSafeName(ver)+".exe")
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 50<<20 {
		if signer, ok := nvCheckSignature(ctx, dst); ok {
			return gpuResult{OK: true, Path: dst, Signer: signer}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return gpuResult{Error: err.Error()}
	}
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+appVersion)
	client := http.Client{Timeout: 2 * time.Hour, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !nvDownloadOK(r.URL.String()) {
			return errBadHost
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return gpuResult{Error: T("gpu.err.stopped")}
		}
		return gpuResult{Error: T("gpu.err.nvNet", err.Error())}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return gpuResult{Error: T("gpu.err.nvNet", resp.Status)}
	}
	total := resp.ContentLength
	if total > nvMaxSize {
		return gpuResult{Error: T("gpu.err.nvFile")}
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return gpuResult{Error: err.Error()}
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(part)
		}
	}()
	buf := make([]byte, 512<<10)
	var done int64
	head := make([]byte, 0, 2)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if len(head) < 2 {
				head = append(head, buf[:min(n, 2-len(head))]...)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return gpuResult{Error: err.Error()}
			}
			done += int64(n)
			if done > nvMaxSize || total > 0 && done > total {
				return gpuResult{Error: T("gpu.err.nvFile")}
			}
			if time.Since(last) > 300*time.Millisecond {
				last = time.Now()
				emit("gpu-dl", map[string]any{"done": done, "total": total, "version": ver})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return gpuResult{Error: T("gpu.err.stopped")}
			}
			return gpuResult{Error: T("gpu.err.nvNet", rerr.Error())}
		}
	}
	emit("gpu-dl", map[string]any{"done": done, "total": total, "version": ver, "checking": true})
	if string(head) != "MZ" || total > 0 && done != total {
		return gpuResult{Error: T("gpu.err.nvFile")}
	}
	if err := f.Close(); err != nil {
		return gpuResult{Error: err.Error()}
	}
	ok = true
	signer, valid := nvCheckSignature(ctx, part)
	if !valid {
		_ = os.Remove(part)
		return gpuResult{Error: T("gpu.err.sign")}
	}
	_ = os.Remove(dst)
	if err := os.Rename(part, dst); err != nil {
		_ = os.Remove(part)
		return gpuResult{Error: err.Error()}
	}
	return gpuResult{OK: true, Path: dst, Signer: signer}
}

// nvCheckSignature: Windows checks the file's digital signature; it must be
// valid and NVIDIA's.
func nvCheckSignature(ctx context.Context, path string) (string, bool) {
	out, _, err := runPSCtx(ctx, 3*time.Minute, "$s = Get-AuthenticodeSignature -LiteralPath "+psQuote(path)+"; \"$($s.Status)|$($s.SignerCertificate.Subject)\"")
	if err != nil {
		return "", false
	}
	return nvSigner(out)
}

func gpuCancelDownload() bool {
	nvDL.Lock()
	defer nvDL.Unlock()
	if nvDL.cancel == nil {
		return false
	}
	nvDL.cancel()
	return true
}

// gpuRunInstaller starts NVIDIA's installer that this app downloaded and
// checked (its own window: "Custom → Clean installation" is offered there).
func gpuRunInstaller(path string) gpuResult {
	nvDL.Lock()
	ok := nvDL.files[strings.ToLower(path)]
	nvDL.Unlock()
	if !ok {
		return gpuResult{Error: T("gpu.err.nvURL")}
	}
	if _, valid := nvCheckSignature(context.Background(), path); !valid {
		return gpuResult{Error: T("gpu.err.sign")}
	}
	cmd := exec.CommandContext(context.Background(), path) // its own window; it is not stopped with the app
	cmd.Dir = filepath.Dir(path)
	if err := cmd.Start(); err != nil {
		return gpuResult{Error: err.Error()}
	}
	go func() { _ = cmd.Wait() }()
	logEvent("gpu", "info", filepath.Base(path), T("log.gpuInstaller"), map[string]any{"path": path})
	return gpuResult{OK: true}
}

// gpuOpenDetails opens NVIDIA's page about one driver of the last list.
func gpuOpenDetails(u string) bool {
	nvCache.Lock()
	defer nvCache.Unlock()
	if nvCache.list == nil || !strings.HasPrefix(u, "https://www.nvidia.com/") {
		return false
	}
	for _, d := range nvCache.list.Drivers {
		if d.Details == u {
			shellOpen(u)
			return true
		}
	}
	return false
}

// restartPC restarts Windows after a driver change (only on a click, after
// the page asked; programs get Windows' usual chance to save).
func restartPC() bool {
	if !app.admin {
		return false
	}
	logEvent("gpu", "info", "", T("log.restart"), nil)
	_, code, err := runQuiet(30*time.Second, sys32("shutdown.exe"), "/r", "/t", "5", "/d", "p:2:4")
	return err == nil && code == 0
}
