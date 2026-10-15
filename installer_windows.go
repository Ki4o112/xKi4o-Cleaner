//go:build windows

package main

// Orphaned Windows Installer packages.
//
// C:\Windows\Installer keeps a copy (.msi / .msp) of every installed program
// and update, because Windows needs it to repair, update or remove them. Over
// the years copies of programs and patches that are no longer installed can
// stay behind. A file is called orphaned here only when it is proven unused:
//
//   - Windows Installer itself is asked for the cached package of every
//     installed product and patch, for all users, and
//   - the Installer's own registry records are read as a second source;
//   - only .msi / .msp files directly in C:\Windows\Installer that neither
//     source mentions, and that were not changed in the last 7 days, count;
//   - if either source cannot be read, or reports nothing at all, nothing is
//     treated as orphaned.

import (
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	msidll                  = windows.NewLazySystemDLL("msi.dll")
	procMsiEnumProductsEx   = msidll.NewProc("MsiEnumProductsExW")
	procMsiGetProductInfoEx = msidll.NewProc("MsiGetProductInfoExW")
	procMsiEnumPatchesEx    = msidll.NewProc("MsiEnumPatchesExW")
	procMsiGetPatchInfoEx   = msidll.NewProc("MsiGetPatchInfoExW")
)

const (
	msiContextAll    = 7  // MSIINSTALLCONTEXT_ALL
	msiPatchStateAll = 15 // MSIPATCHSTATE_ALL
	errNoMoreItems   = 259
	errMoreDataMsi   = 234
)

func u16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

// msiString calls one of the Msi*InfoEx functions that fill a string buffer.
func msiString(call func(buf *uint16, n *uint32) uintptr) (string, bool) {
	n := uint32(260)
	for i := 0; i < 3; i++ {
		buf := make([]uint16, n+1)
		m := uint32(len(buf))
		r := call(&buf[0], &m)
		switch r {
		case 0:
			return windows.UTF16ToString(buf[:m]), true
		case errMoreDataMsi:
			n = m + 1
		default:
			return "", false
		}
	}
	return "", false
}

// msiLocalPackages asks Windows Installer for the cached package of every
// installed product and patch. ok is false when the API is not usable.
func msiLocalPackages() (pkgs map[string]bool, ok bool) {
	if procMsiEnumProductsEx.Find() != nil || procMsiGetProductInfoEx.Find() != nil ||
		procMsiEnumPatchesEx.Find() != nil || procMsiGetPatchInfoEx.Find() != nil {
		return nil, false
	}
	pkgs = map[string]bool{}
	allUsers := u16("s-1-1-0")
	localPackage := u16("LocalPackage")
	for i := uint32(0); ; i++ {
		var code [39]uint16
		var ctx uint32
		var sid [256]uint16
		sidLen := uint32(len(sid))
		r, _, _ := procMsiEnumProductsEx.Call(0, uintptr(unsafe.Pointer(allUsers)), msiContextAll, uintptr(i),
			uintptr(unsafe.Pointer(&code[0])), uintptr(unsafe.Pointer(&ctx)), uintptr(unsafe.Pointer(&sid[0])), uintptr(unsafe.Pointer(&sidLen)))
		if r == errNoMoreItems {
			break
		}
		if r != 0 {
			return nil, false
		}
		var sidPtr uintptr
		if ctx != 4 && sid[0] != 0 { // not per-machine: the user's SID is needed
			sidPtr = uintptr(unsafe.Pointer(&sid[0]))
		}
		if v, ok := msiString(func(buf *uint16, n *uint32) uintptr {
			r, _, _ := procMsiGetProductInfoEx.Call(uintptr(unsafe.Pointer(&code[0])), sidPtr, uintptr(ctx),
				uintptr(unsafe.Pointer(localPackage)), uintptr(unsafe.Pointer(buf)), uintptr(unsafe.Pointer(n)))
			return r
		}); ok && v != "" {
			pkgs[normPath(v)] = true
		}
	}
	for i := uint32(0); ; i++ {
		var patch, product [39]uint16
		var ctx uint32
		var sid [256]uint16
		sidLen := uint32(len(sid))
		r, _, _ := procMsiEnumPatchesEx.Call(0, uintptr(unsafe.Pointer(allUsers)), msiContextAll, msiPatchStateAll, uintptr(i),
			uintptr(unsafe.Pointer(&patch[0])), uintptr(unsafe.Pointer(&product[0])), uintptr(unsafe.Pointer(&ctx)),
			uintptr(unsafe.Pointer(&sid[0])), uintptr(unsafe.Pointer(&sidLen)))
		if r == errNoMoreItems {
			break
		}
		if r != 0 {
			return nil, false
		}
		var sidPtr uintptr
		if ctx != 4 && sid[0] != 0 {
			sidPtr = uintptr(unsafe.Pointer(&sid[0]))
		}
		if v, ok := msiString(func(buf *uint16, n *uint32) uintptr {
			r, _, _ := procMsiGetPatchInfoEx.Call(uintptr(unsafe.Pointer(&patch[0])), uintptr(unsafe.Pointer(&product[0])), sidPtr, uintptr(ctx),
				uintptr(unsafe.Pointer(localPackage)), uintptr(unsafe.Pointer(buf)), uintptr(unsafe.Pointer(n)))
			return r
		}); ok && v != "" {
			pkgs[normPath(v)] = true
		}
	}
	return pkgs, true
}

// registryLocalPackages reads the same information from the Installer's
// registry records (all users, products and patches).
func registryLocalPackages() (map[string]bool, bool) {
	const base = `SOFTWARE\Microsoft\Windows\CurrentVersion\Installer\UserData`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return nil, false
	}
	sids, _ := k.ReadSubKeyNames(-1)
	k.Close()
	out := map[string]bool{}
	read := func(path, name string) {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			return
		}
		defer k.Close()
		if v, _, err := k.GetStringValue(name); err == nil && v != "" {
			out[normPath(v)] = true
		}
	}
	subs := func(path string) []string {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
		if err != nil {
			return nil
		}
		defer k.Close()
		n, _ := k.ReadSubKeyNames(-1)
		return n
	}
	for _, sid := range sids {
		for _, p := range subs(base + `\` + sid + `\Products`) {
			read(base+`\`+sid+`\Products\`+p+`\InstallProperties`, "LocalPackage")
		}
		for _, p := range subs(base + `\` + sid + `\Patches`) {
			read(base+`\`+sid+`\Patches\`+p, "LocalPackage")
		}
	}
	return out, true
}

// orphanedInstallerFiles returns the provably unused .msi / .msp files.
func orphanedInstallerFiles() []planned {
	dir := j(P.windir, "Installer")
	if !dirExists(dir) {
		return nil
	}
	api, ok1 := msiLocalPackages()
	reg, ok2 := registryLocalPackages()
	if !ok1 || !ok2 || len(api) == 0 || len(reg) == 0 {
		return nil // cannot prove anything
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	var out []planned
	for _, e := range readDir(dir) {
		if isLink(e.info) || e.info.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.info.Name()))
		if ext != ".msi" && ext != ".msp" {
			continue
		}
		key := normPath(e.path)
		if api[key] || reg[key] || !e.info.ModTime().Before(cutoff) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func msiTargets() []target {
	var ts []target
	for _, f := range orphanedInstallerFiles() {
		ts = append(ts, target{Path: f.path, Kind: kWhole})
	}
	return ts
}

// installerBusy: while Windows Installer runs (an installation, a repair or
// the few minutes it stays loaded afterwards) its cache is left alone.
func installerBusy(procs map[string]bool) string {
	if procs["msiexec.exe"] {
		return T("msi.busy")
	}
	return ""
}
