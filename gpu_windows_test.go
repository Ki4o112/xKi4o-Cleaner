//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// The structures handed to user32 must have Windows' own sizes.
func TestDisplayStructLayout(t *testing.T) {
	var dm devModeW
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"DEVMODEW", unsafe.Sizeof(dm), 220},
		{"dmFields", unsafe.Offsetof(dm.Fields), 72},
		{"dmBitsPerPel", unsafe.Offsetof(dm.BitsPerPel), 168},
		{"dmPelsWidth", unsafe.Offsetof(dm.PelsWidth), 172},
		{"dmDisplayFrequency", unsafe.Offsetof(dm.DisplayFrequency), 184},
		{"DISPLAY_DEVICEW", unsafe.Sizeof(displayDeviceW{}), 840},
		{"DISPLAYCONFIG_PATH_INFO", unsafe.Sizeof(dcPath{}), 72},
		{"DISPLAYCONFIG_DEVICE_INFO_HEADER", unsafe.Sizeof(dcHeader{}), 20},
		{"DISPLAYCONFIG_SOURCE_DEVICE_NAME", unsafe.Sizeof(dcSourceName{}), 84},
		{"DISPLAYCONFIG_TARGET_DEVICE_NAME", unsafe.Sizeof(dcTargetName{}), 420},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.name, c.got, c.want)
		}
	}
}

// What the Graphics card page shows: read only.
func TestGPUStateRead(t *testing.T) {
	s := gpuStateRead()
	t.Logf("read in %d ms, error %q; HAGS %q, Windows may download drivers: %v (turned off by the app: %v), %d copies",
		s.Took, s.Error, s.HAGS, s.WUDrivers, s.WUByUs, len(s.Backups))
	for _, d := range s.Devices {
		t.Logf("card %q %s VEN_%s DEV_%s virtual=%v problem=%d noDriver=%v vram=%d MB", d.Name, d.Vendor, d.VenID, d.DevID, d.Virtual, d.Problem, d.NoDriver, d.VRAM>>20)
		t.Logf("  in use: %s %s (%s) %s %s, %d MB", d.Driver.Inf, d.Driver.Version, d.Driver.VendorV, d.Driver.Date, d.Driver.Provider, d.Driver.Size>>20)
		for _, p := range d.Packages {
			t.Logf("  on the PC: %-12s %-16s %-7s %s %-10s inUse=%v basic=%v %d MB", p.Inf, p.Version, p.VendorV, p.Date, p.Provider, p.InUse, p.Basic, p.Size>>20)
		}
		if !d.Virtual && !d.NoDriver {
			if dir := gpuStoreDir(d.Driver.Inf); dir == "" {
				t.Errorf("%s: no driver store folder for %s", d.Name, d.Driver.Inf)
			} else {
				t.Logf("  store: %s", dir)
			}
			inUse := 0
			for _, p := range d.Packages {
				if p.InUse {
					inUse++
				}
			}
			if len(d.Packages) > 0 && inUse != 1 {
				t.Errorf("%s: %d packages marked in use", d.Name, inUse)
			}
			t.Logf("  removal plan (all): %d packages", len(gpuRemovePlan(d, true)))
		}
	}
	for _, d := range s.Displays {
		t.Logf("screen %s %q on %q via %q primary=%v: %dx%d %d Hz (max %d Hz; largest %dx%d) rates %v", d.Name, d.Monitor, d.Adapter, d.Conn, d.Primary, d.W, d.H, d.Hz, d.MaxHz, d.MaxW, d.MaxH, d.Rates)
		if d.MaxHz < d.Hz && len(d.Rates) > 0 {
			t.Errorf("%s: the highest rate %d is below the current %d", d.Name, d.MaxHz, d.Hz)
		}
	}
	if _, err := json.Marshal(s); err != nil {
		t.Error(err)
	}
}

func TestGPUSensorsRead(t *testing.T) {
	a := gpuSensorsRead()
	time.Sleep(600 * time.Millisecond)
	s := gpuSensorsRead()
	b, _ := json.Marshal(s)
	t.Logf("first %+v", a)
	t.Logf("sensors %s", b)
	if s.Src == "nvml" {
		if s.PCIeMaxW == 0 || s.Driver == "" {
			t.Errorf("NVIDIA's library answered, but without the PCIe link or the driver: %+v", s)
		}
		if s.PCIeWidth > s.PCIeMaxW {
			t.Errorf("PCIe x%d above its maximum x%d", s.PCIeWidth, s.PCIeMaxW)
		}
	}
}

// NVIDIA's list of drivers for this PC's card (reads from the internet,
// changes nothing; skipped without an NVIDIA card or a connection).
func TestNvidiaDriverList(t *testing.T) {
	l := gpuNvDrivers(true)
	if l.Card == "" {
		t.Skip("no NVIDIA card")
	}
	if strings.HasPrefix(l.Error, strings.SplitN(T("gpu.err.nvNet", ""), ":", 2)[0]) {
		t.Skipf("no answer from NVIDIA: %s", l.Error)
	}
	if l.Error != "" {
		t.Fatalf("%q: %s", l.Card, l.Error)
	}
	t.Logf("%q → NVIDIA product %q (psid %d, pfid %d, notebook %v); installed %s, latest %s, newer %v, %d drivers",
		l.Card, l.Product, l.Series, l.ID, l.Notebook, l.Installed, l.Latest, l.Newer, len(l.Drivers))
	for i, d := range l.Drivers {
		if i < 8 {
			t.Logf("  %s %s %-28q %s %s", d.Version, d.Date, d.Name, d.Size, d.URL)
		}
		if !nvDownloadOK(d.URL) {
			t.Errorf("a download that is not NVIDIA's: %s", d.URL)
		}
		if l.Notebook != nvIsNotebook(d.URL) {
			t.Logf("  (a %s package for a %s)", map[bool]string{true: "notebook", false: "desktop"}[nvIsNotebook(d.URL)], map[bool]string{true: "laptop", false: "desktop"}[l.Notebook])
		}
	}
	if len(l.Drivers) < 3 {
		t.Errorf("only %d drivers", len(l.Drivers))
	}
}

// Nothing outside the app's own lists can be started or downloaded.
func TestGPURefusesUnknownFiles(t *testing.T) {
	if r := gpuRunInstaller(`C:\Windows\notepad.exe`); r.OK {
		t.Error("an exe the app did not download was started")
	}
	if r := gpuNvDownload("https://us.download.nvidia.com/Windows/1.00/not-in-the-list.exe"); r.OK || r.Error == "" {
		t.Error("a download not from NVIDIA's last list")
	}
	if r := gpuSetRefresh(`\\.\DISPLAY99`, 60); r.OK {
		t.Error("a screen that does not exist")
	}
	if gpuKeepRefresh(`\\.\DISPLAY99`) {
		t.Error("nothing to keep")
	}
	was := app.admin
	app.admin = true
	defer func() { app.admin = was }()
	for _, bad := range []string{"display.inf", `..\..\x.inf`, "oem1.exe", ""} {
		if r := gpuDeletePackage(bad); r.OK {
			t.Errorf("deleted %q", bad)
		}
	}
	if r := gpuRestoreBackup(`C:\Windows\INF`, false); r.OK {
		t.Error("put back a folder that is no copy")
	}
	if gpuDeleteBackup(`C:\Windows\Temp`) {
		t.Error("deleted a folder that is no copy")
	}
}

// The copies list only knows folders in the app's own folder with its note.
func TestGPUBackupsList(t *testing.T) {
	was := P.local
	P.local = t.TempDir()
	defer func() { P.local = was }()
	if n := len(gpuBackups()); n != 0 {
		t.Fatalf("%d copies in an empty folder", n)
	}
	dir := filepath.Join(gpuBackupRoot(), "nvidia-32.0.16.1742-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "nv_dispi.inf"), []byte("[Version]"), 0o644)
	b, _ := json.Marshal(gpuBackup{Name: "NVIDIA GeForce RTX 4090", Vendor: "nvidia", Version: "32.0.16.1742", Inf: "nv_dispi.inf", Size: 9, Made: 1})
	_ = os.WriteFile(filepath.Join(dir, "xki4o-driver.json"), b, 0o644)
	_ = os.MkdirAll(filepath.Join(gpuBackupRoot(), "no-note"), 0o755)
	bs := gpuBackups()
	if len(bs) != 1 || bs[0].Dir != dir || bs[0].Version != "32.0.16.1742" {
		t.Fatalf("copies: %+v", bs)
	}
	if !gpuDeleteBackup(dir) {
		t.Error("the copy was not deleted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the folder is still there")
	}
}

// Windows' own driver downloads off and back on, with the value it had
// before (only under Wine: this switch is never touched on a real PC by a
// test).
func TestGPUWUSwitch(t *testing.T) {
	if !underWine() {
		t.Skip("changes a Windows setting: runs only under Wine")
	}
	was := app.admin
	app.admin = true
	defer func() { app.admin = was }()
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, driverSearchKey, registry.ALL_ACCESS)
	if err != nil {
		t.Skip(err)
	}
	defer k.Close()
	_ = k.SetDWordValue("SearchOrderConfig", 1)
	updateSettings(func(m map[string]any) { delete(m, gpuWUPrevKey) })
	if r := gpuWU(false); !r.OK {
		t.Fatal(r.Error)
	}
	if wuDriverSearch() {
		t.Error("still on")
	}
	if _, ok := getSettings()[gpuWUPrevKey]; !ok {
		t.Error("the value before was not kept")
	}
	if r := gpuWU(false); !r.OK {
		t.Fatal(r.Error)
	}
	if v, _ := getSettings()[gpuWUPrevKey].(float64); v != 1 {
		t.Errorf("a second 'off' must keep the first value before (1), got %v", v)
	}
	if r := gpuWU(true); !r.OK {
		t.Fatal(r.Error)
	}
	if v, _, err := k.GetIntegerValue("SearchOrderConfig"); err != nil || v != 1 {
		t.Errorf("not put back: %v %v", v, err)
	}
	if _, ok := getSettings()[gpuWUPrevKey]; ok {
		t.Error("the note stayed")
	}
	// it had no value at all: it gets none again
	_ = k.DeleteValue("SearchOrderConfig")
	_ = gpuWU(false)
	_ = gpuWU(true)
	if _, _, err := k.GetIntegerValue("SearchOrderConfig"); err == nil {
		t.Error("a value was left where there was none")
	}
}
