//go:build windows

package main

// The real icons of apps, files and folders, for the page. Windows draws
// them; each is turned into a small PNG (data URL) and kept in memory, so a
// list of hundreds of rows asks Windows only once per program or file type.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	gdi32                    = windows.NewLazySystemDLL("gdi32.dll")
	procPrivateExtractIconsW = user32.NewProc("PrivateExtractIconsW")
	procSHGetFileInfoW       = shell32.NewProc("SHGetFileInfoW")
	procGetIconInfo          = user32.NewProc("GetIconInfo")
	procDestroyIcon          = user32.NewProc("DestroyIcon")
	procGetDC                = user32.NewProc("GetDC")
	procReleaseDC            = user32.NewProc("ReleaseDC")
	procGetDIBits            = gdi32.NewProc("GetDIBits")
	procGetObjectW           = gdi32.NewProc("GetObjectW")
	procDeleteObject         = gdi32.NewProc("DeleteObject")
)

type iconInfo struct { // ICONINFO
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

type bitmapObj struct { // BITMAP
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfoHeader struct { // BITMAPINFOHEADER
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type shFileInfo struct { // SHFILEINFOW
	hIcon       uintptr
	iIcon       int32
	attrs       uint32
	displayName [260]uint16
	typeName    [80]uint16
}

var _ = [1]struct{}{}[unsafe.Sizeof(shFileInfo{})-696]
var _ = [1]struct{}{}[unsafe.Sizeof(iconInfo{})-32]
var _ = [1]struct{}{}[unsafe.Sizeof(bitmapObj{})-32]

const (
	shgfiIcon              = 0x100
	shgfiLargeIcon         = 0x0
	shgfiUseFileAttributes = 0x10
	fileAttrDirectory      = 0x10
	fileAttrNormal         = 0x80
	appIconSize            = 48 // drawn at 18–32 px, sharp on high-DPI screens too
)

// iconPNG turns an icon handle into PNG bytes (straight alpha).
func iconPNG(h uintptr) ([]byte, bool) {
	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(h, uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil, false
	}
	if ii.hbmMask != 0 {
		defer procDeleteObject.Call(ii.hbmMask)
	}
	if ii.hbmColor == 0 {
		return nil, false // a black-and-white icon: not worth drawing
	}
	defer procDeleteObject.Call(ii.hbmColor)
	var bm bitmapObj
	if r, _, _ := procGetObjectW.Call(ii.hbmColor, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil, false
	}
	w, h2 := int(bm.bmWidth), int(bm.bmHeight)
	if w <= 0 || h2 <= 0 || w > 256 || h2 > 256 {
		return nil, false
	}
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return nil, false
	}
	defer procReleaseDC.Call(0, dc)
	bi := bitmapInfoHeader{biSize: uint32(unsafe.Sizeof(bitmapInfoHeader{})), biWidth: int32(w), biHeight: -int32(h2), biPlanes: 1, biBitCount: 32}
	px := make([]byte, w*h2*4)
	if r, _, _ := procGetDIBits.Call(dc, ii.hbmColor, 0, uintptr(h2), uintptr(unsafe.Pointer(&px[0])), uintptr(unsafe.Pointer(&bi)), 0); r == 0 {
		return nil, false
	}
	alpha := false
	for i := 3; i < len(px); i += 4 {
		if px[i] != 0 {
			alpha = true
			break
		}
	}
	if !alpha {
		// an older icon without alpha: its mask says which pixels show
		mask := make([]byte, w*h2*4)
		mi := bitmapInfoHeader{biSize: bi.biSize, biWidth: int32(w), biHeight: -int32(h2), biPlanes: 1, biBitCount: 32}
		ok := ii.hbmMask != 0
		if ok {
			r, _, _ := procGetDIBits.Call(dc, ii.hbmMask, 0, uintptr(h2), uintptr(unsafe.Pointer(&mask[0])), uintptr(unsafe.Pointer(&mi)), 0)
			ok = r != 0
		}
		for i := 0; i < w*h2; i++ {
			if !ok || mask[i*4] == 0 {
				px[i*4+3] = 255
			}
		}
	}
	return encodeBGRA(px, w, h2)
}

// encodeBGRA writes Windows' BGRA pixels as a PNG.
func encodeBGRA(px []byte, w, h int) ([]byte, bool) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = px[i*4+2], px[i*4+1], px[i*4], px[i*4+3]
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// exeIcon reads the program's own icon at the given size (scaled by
// Windows from its best image).
func exeIcon(path string, size int) uintptr {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var h uintptr
	var id uint32
	n, _, _ := procPrivateExtractIconsW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(size), uintptr(size), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&id)), 1, 0)
	if n == 0 || n == 0xFFFFFFFF || h == 0 {
		return 0
	}
	return h
}

// shellIcon asks the shell, as Explorer shows it. With attrs != 0 the file
// does not need to exist (an icon for a kind of file or for a folder).
func shellIcon(path string, attrs uint32) uintptr {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	flags := uintptr(shgfiIcon | shgfiLargeIcon)
	if attrs != 0 {
		flags |= shgfiUseFileAttributes
	}
	var fi shFileInfo
	r, _, _ := procSHGetFileInfoW.Call(uintptr(unsafe.Pointer(p)), uintptr(attrs), uintptr(unsafe.Pointer(&fi)), unsafe.Sizeof(fi), flags)
	if r == 0 {
		return 0
	}
	return fi.hIcon
}

func iconURL(h uintptr) string {
	if h == 0 {
		return ""
	}
	defer procDestroyIcon.Call(h)
	b, ok := iconPNG(h)
	if !ok {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
}

var icons = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// iconsFor answers the page: key -> PNG data URL ("" = none, the page keeps
// its own picture). Keys: "exe:<path>" a program, "file:<path>" a file as
// Explorer shows it, "ext:.mp4" a kind of file, "dir:" a folder,
// "drive:C:\" a drive, "app:<category>" the program of a cleaning category,
// "proc:<name.exe>" a program found by its file name.
func iconsFor(keys []string) map[string]string {
	out := map[string]string{}
	var todo []string
	icons.Lock()
	for _, k := range keys {
		if v, ok := icons.m[k]; ok {
			out[k] = v
		} else if len(todo) < 400 {
			todo = append(todo, k)
		}
	}
	icons.Unlock()
	if len(todo) == 0 {
		return out
	}
	deadline := time.Now().Add(4 * time.Second)
	withCOM(func() {
		for _, k := range todo {
			if time.Now().After(deadline) {
				return // the rest is asked again later
			}
			v := iconOf(k)
			icons.Lock()
			icons.m[k] = v
			icons.Unlock()
			out[k] = v
		}
	})
	return out
}

func iconOf(key string) string {
	kind, arg, _ := strings.Cut(key, ":")
	switch kind {
	case "exe":
		if !filepath.IsAbs(arg) {
			return ""
		}
		if h := exeIcon(arg, appIconSize); h != 0 {
			return iconURL(h)
		}
		return iconURL(shellIcon(arg, 0))
	case "file":
		if !filepath.IsAbs(arg) {
			return ""
		}
		if ext := strings.ToLower(filepath.Ext(arg)); ext == ".exe" || ext == ".ico" {
			if h := exeIcon(arg, appIconSize); h != 0 {
				return iconURL(h)
			}
		}
		return iconURL(shellIcon(arg, 0))
	case "ext":
		ext := strings.ToLower(arg)
		if ext != "" && (!strings.HasPrefix(ext, ".") || strings.ContainsAny(ext, `\/:*?"<>|`) || len(ext) > 16) {
			return ""
		}
		return iconURL(shellIcon("file"+ext, fileAttrNormal))
	case "dir":
		return iconURL(shellIcon("folder", fileAttrDirectory))
	case "drive":
		if len(arg) < 2 || arg[1] != ':' {
			return ""
		}
		return iconURL(shellIcon(arg[:2]+`\`, 0))
	case "app":
		if p := appExePath(arg); p != "" {
			return iconOf("exe:" + p)
		}
	case "proc":
		if p := findExe(arg); p != "" {
			return iconOf("exe:" + p)
		}
	}
	return ""
}

// appExePath: the program of a cleaning category (Chrome, Discord, ...),
// when it is installed.
func appExePath(id string) string {
	it := findItem(id)
	if it == nil {
		return ""
	}
	for _, p := range it.procs {
		if x := findExe(p); x != "" {
			return x
		}
	}
	return ""
}

var exeIndex struct {
	once sync.Once
	mu   sync.Mutex
	m    map[string]string // lower-case file name -> path
	run  map[string]string // found among running programs
}

// findExe finds an installed program by its file name: a running copy, the
// App Paths registration, the uninstall entries, then the program folders.
func findExe(name string) string {
	name = strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if name == "" || name == "." || !strings.HasSuffix(name, ".exe") {
		return ""
	}
	exeIndex.mu.Lock()
	if p, ok := exeIndex.run[name]; ok {
		exeIndex.mu.Unlock()
		return p
	}
	exeIndex.mu.Unlock()
	for pid := range procPIDs(map[string]bool{name: true}) {
		if p := processPath(pid); p != "" {
			exeIndex.mu.Lock()
			if exeIndex.run == nil {
				exeIndex.run = map[string]string{}
			}
			exeIndex.run[name] = p
			exeIndex.mu.Unlock()
			return p
		}
	}
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+name, registry.QUERY_VALUE); err == nil {
			v, _, _ := k.GetStringValue("")
			k.Close()
			if p := cleanExePath(v); p != "" && fileExists(p) {
				return p
			}
		}
	}
	exeIndex.once.Do(buildExeIndex)
	exeIndex.mu.Lock()
	defer exeIndex.mu.Unlock()
	return exeIndex.m[name]
}

// cleanExePath: `"C:\x\app.exe",0` or %VAR%\app.exe -> C:\x\app.exe.
func cleanExePath(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if i := strings.Index(v[1:], `"`); i >= 0 {
			v = v[1 : i+1]
		}
	}
	if i := strings.LastIndex(v, ","); i > 0 && !strings.ContainsAny(v[i:], `\/`) {
		v = v[:i]
	}
	v = strings.Trim(strings.TrimSpace(expandEnv(v)), `"`)
	if !strings.EqualFold(filepath.Ext(v), ".exe") || !filepath.IsAbs(v) {
		return ""
	}
	return v
}

func buildExeIndex() {
	m := map[string]string{}
	newer := map[string]time.Time{}
	add := func(p string) {
		n := strings.ToLower(filepath.Base(p))
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return
		}
		if old, ok := m[n]; ok && old != "" && !fi.ModTime().After(newer[n]) {
			return
		}
		m[n], newer[n] = p, fi.ModTime()
	}
	// uninstall entries name the main program of most apps
	for _, base := range []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
	} {
		k, err := registry.OpenKey(base.root, base.path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, sub := range names {
			sk, err := registry.OpenKey(base.root, base.path+`\`+sub, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			icon, _, _ := sk.GetStringValue("DisplayIcon")
			sk.Close()
			if p := cleanExePath(icon); p != "" {
				add(p)
			}
		}
	}
	// then the program folders, a few levels deep (Discord keeps its program
	// in %LOCALAPPDATA%\Discord\app-1.0.x\Discord.exe)
	type root struct {
		dir   string
		depth int
	}
	roots := []root{
		{os.Getenv("ProgramFiles"), 3}, {os.Getenv("ProgramFiles(x86)"), 3},
		{filepath.Join(P.local, "Programs"), 3}, {P.local, 3}, {P.roaming, 2},
	}
	skip := map[string]bool{"windowsapps": true, "packages": true, "microsoft": true, "temp": true, "common files": true, "windows defender": true,
		"windows nt": true, "windowspowershell": true, "internet explorer": true, "modifiablewindowsapps": true,
		"crashdumps": true, "d3dscache": true, "connecteddevicesplatform": true, "comms": true}
	visited, deadline := 0, time.Now().Add(2500*time.Millisecond)
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if visited > 30000 || time.Now().After(deadline) {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		visited += len(ents)
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() {
				if depth > 1 && !skip[strings.ToLower(n)] && e.Type()&os.ModeSymlink == 0 {
					walk(filepath.Join(dir, n), depth-1)
				}
			} else if strings.EqualFold(filepath.Ext(n), ".exe") {
				if _, ok := m[strings.ToLower(n)]; !ok || strings.HasPrefix(strings.ToLower(filepath.Base(dir)), "app-") {
					add(filepath.Join(dir, n))
				}
			}
		}
	}
	for _, r := range roots {
		if r.dir != "" {
			walk(r.dir, r.depth)
		}
	}
	exeIndex.mu.Lock()
	exeIndex.m = m
	exeIndex.mu.Unlock()
}
