//go:build windows

package main

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileVersionStrings reads strings (FileDescription, CompanyName,
// ProductName, ...) from a program's version resource with one file read.
// Missing strings are left out.
func fileVersionStrings(path string, names ...string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 || size > 16<<20 {
		return out
	}
	buf := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])) != nil {
		return out
	}
	// language/code-page pairs the file has; fall back to the usual ones
	var langs []string
	var tp unsafe.Pointer
	var tl uint32
	if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&tp), &tl) == nil && tl >= 4 {
		n := int(tl / 4)
		pairs := unsafe.Slice((*[2]uint16)(tp), n)
		for _, p := range pairs {
			langs = append(langs, fmt.Sprintf("%04x%04x", p[0], p[1]))
		}
	}
	langs = append(langs, "040904b0", "040904e4", "04090000", "000004b0")
	for _, name := range names {
		for _, l := range langs {
			var vp unsafe.Pointer
			var vl uint32
			if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\`+l+`\`+name, unsafe.Pointer(&vp), &vl) != nil || vl == 0 {
				continue
			}
			s := strings.TrimSpace(windows.UTF16PtrToString((*uint16)(vp)))
			if s != "" {
				out[name] = s
				break
			}
		}
	}
	return out
}
