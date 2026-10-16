//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	dwmapi  = windows.NewLazySystemDLL("dwmapi.dll")

	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procMessageBoxW           = user32.NewProc("MessageBoxW")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	procSHQueryRecycleBinW    = shell32.NewProc("SHQueryRecycleBinW")
	procSHEmptyRecycleBinW    = shell32.NewProc("SHEmptyRecycleBinW")
)

// ---------------------------------------------------------------- window

func dpiScale() float64 {
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d >= 96 {
			return float64(d) / 96
		}
	}
	return 1
}

func workArea() (int, int) {
	var r struct{ L, T, R, B int32 }
	ok, _, _ := procSystemParametersInfoW.Call(0x0030, 0, uintptr(unsafe.Pointer(&r)), 0) // SPI_GETWORKAREA
	if ok == 0 {
		return 0, 0
	}
	return int(r.R - r.L), int(r.B - r.T)
}

// styleWindow gives the native title bar the app's dark colours.
func styleWindow(hwnd uintptr) {
	if procDwmSetWindowAttribute.Find() != nil {
		return
	}
	set := func(attr uintptr, v uint32) {
		_, _, _ = procDwmSetWindowAttribute.Call(hwnd, attr, uintptr(unsafe.Pointer(&v)), 4)
	}
	set(20, 1)          // DWMWA_USE_IMMERSIVE_DARK_MODE (Windows 10 20H1+, 11)
	set(19, 1)          // same attribute on older Windows 10 builds
	set(35, 0x0020100b) // DWMWA_CAPTION_COLOR  #0b1020 (Windows 11)
	set(36, 0x00faf2ee) // DWMWA_TEXT_COLOR     #eef2fa
	set(34, 0x0020100b) // DWMWA_BORDER_COLOR
}

const (
	mbOK          = 0x0
	mbYesNo       = 0x4
	mbIconWarning = 0x30
	mbIconError   = 0x10
	idYes         = 6
)

func messageBox(owner uintptr, text, title string, flags uint32) int {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	r, _, _ := procMessageBoxW.Call(owner, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), uintptr(flags))
	return int(r)
}

func shellOpen(target string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(target)
	_ = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// showInFolder opens File Explorer with the file or folder selected.
func showInFolder(path string) bool {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\"\r\n") {
		return false
	}
	if _, err := os.Lstat(path); err != nil {
		dir := filepath.Dir(path) // gone meanwhile: open its folder if it is still there
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return false
		}
		shellOpen(dir)
		return true
	}
	verb, _ := windows.UTF16PtrFromString("open")
	exe, _ := windows.UTF16PtrFromString(filepath.Join(os.Getenv("SystemRoot"), "explorer.exe"))
	args, _ := windows.UTF16PtrFromString(`/select,"` + path + `"`)
	return windows.ShellExecute(0, verb, exe, args, nil, windows.SW_SHOWNORMAL) == nil
}

// ---------------------------------------------------------------- process

var instanceMutex windows.Handle

// acquireSingleInstance returns false (and focuses the open window) when
// the app is already running.
func acquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\xKi4oCleaner.SingleInstance`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		signalRunningCopy()
		return false
	}
	instanceMutex = h
	return true
}

func isElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// runningProcs returns the lower-case image names of all running processes.
func runningProcs() map[string]bool {
	m := map[string]bool{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return m
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		m[strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))] = true
	}
	return m
}

// ---------------------------------------------------------------- system

type driveInfo struct {
	Letter string `json:"letter"`
	Total  uint64 `json:"total"`
	Free   uint64 `json:"free"`
}

func systemDrive() string {
	d := os.Getenv("SystemDrive")
	if len(d) < 2 {
		d = "C:"
	}
	return d[:2]
}

func diskSpace(root string) (total, free uint64) {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0
	}
	var avail, tot, totFree uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totFree) != nil {
		return 0, 0
	}
	return tot, avail
}

func sysDriveInfo() driveInfo {
	d := systemDrive()
	t, f := diskSpace(d + `\`)
	return driveInfo{Letter: d[:1], Total: t, Free: f}
}

func freeBytes() int64 {
	_, f := diskSpace(systemDrive() + `\`)
	return int64(f)
}

func osName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer k.Close()
	name, _, _ := k.GetStringValue("ProductName")
	build, _, _ := k.GetStringValue("CurrentBuild")
	ver, _, _ := k.GetStringValue("DisplayVersion")
	if b, _ := strconv.Atoi(build); b >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	if name == "" {
		name = "Windows"
	}
	if ver != "" {
		name += " " + ver
	}
	return name
}

func keyExists(root registry.Key, path string) bool {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// wuBusy explains why Windows Update / servicing files must not be touched
// right now, or returns "" when it is safe. Anything that could interrupt an
// update that is being downloaded, installed or finished by a restart counts.
func wuBusy(procs map[string]bool) string {
	switch {
	case keyExists(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`),
		keyExists(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`):
		return T("wu.reboot")
	case fileExists(filepath.Join(os.Getenv("SystemRoot"), `WinSxS\pending.xml`)):
		return T("wu.servicing")
	case procs["tiworker.exe"] || procs["trustedinstaller.exe"]:
		return T("wu.installing")
	case procs["mousocoreworker.exe"] || procs["wuauclt.exe"] || procs["usoclient.exe"]:
		return T("wu.active")
	case wuRecentDownload(15 * time.Minute):
		return T("wu.downloading")
	}
	return ""
}

// wuRecentDownload: Windows Update wrote to its download folder recently.
func wuRecentDownload(within time.Duration) bool {
	dir := filepath.Join(os.Getenv("SystemRoot"), `SoftwareDistribution\Download`)
	cutoff := time.Now().Add(-within)
	for _, e := range readDir(dir) {
		if e.info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// updatesPending is true while Windows is busy with updates.
func updatesPending(procs map[string]bool) bool { return wuBusy(procs) != "" }

func sys32(exe string) string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", exe)
}

// ---------------------------------------------------------------- recycle bin

type shQueryRBInfo struct {
	cbSize   uint32
	_        uint32
	size     int64
	numItems int64
}

// withCOM runs f on a locked OS thread with COM initialised (the shell
// recycle-bin functions expect it).
func withCOM(f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done) // the caller never waits forever, even if f panics
		defer recoverLog("shell")
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED) == nil {
			defer windows.CoUninitialize()
		}
		f()
	}()
	<-done
}

func recycleBinInfo() (size, items int64) {
	withCOM(func() {
		info := shQueryRBInfo{cbSize: uint32(unsafe.Sizeof(shQueryRBInfo{}))}
		if r, _, _ := procSHQueryRecycleBinW.Call(0, uintptr(unsafe.Pointer(&info))); r == 0 {
			size, items = info.size, info.numItems
		}
	})
	return
}

func emptyRecycleBin() bool {
	ok := false
	withCOM(func() {
		const flags = 0x1 | 0x2 | 0x4 // no confirmation, no progress UI, no sound
		r, _, _ := procSHEmptyRecycleBinW.Call(0, 0, flags)
		ok = r == 0
	})
	return ok
}

// osBuild returns the Windows build with its update revision, e.g. 26100.4652.
func osBuild() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	build, _, _ := k.GetStringValue("CurrentBuild")
	if ubr, _, err := k.GetIntegerValue("UBR"); err == nil && build != "" {
		return build + "." + strconv.FormatUint(ubr, 10)
	}
	return build
}
