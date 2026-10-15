package main

// Reading Windows shortcuts (.lnk, the [MS-SHLLINK] format) to know where
// they point: the local path, a network path, or "advertised" shortcuts of
// Windows Installer and Store apps that have no plain path.

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

type lnkInfo struct {
	Target  string `json:"target"`
	Args    string `json:"args,omitempty"`
	Network bool   `json:"network,omitempty"` // \\server\share\...
	Special bool   `json:"special,omitempty"` // Windows Installer or a shell item (Store app, Control Panel): no file to check
}

const (
	lnkHasIDList   = 0x1
	lnkHasLinkInfo = 0x2
	lnkHasName     = 0x4
	lnkHasRelPath  = 0x8
	lnkHasWorkDir  = 0x10
	lnkHasArgs     = 0x20
	lnkHasIcon     = 0x40
	lnkUnicode     = 0x80
	lnkHasDarwin   = 0x1000
)

var errNotLnk = errors.New("not a shortcut")

func readLnkFile(path string) (lnkInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return lnkInfo{}, err
	}
	defer f.Close()
	b := make([]byte, 64<<10)
	n, _ := f.Read(b)
	return parseLnk(b[:n], filepath.Dir(path))
}

// parseLnk reads a shortcut. dir is its folder (for relative targets).
func parseLnk(b []byte, dir string) (lnkInfo, error) {
	var li lnkInfo
	if len(b) < 0x4C || binary.LittleEndian.Uint32(b) != 0x4C || binary.LittleEndian.Uint32(b[4:]) != 0x00021401 {
		return li, errNotLnk
	}
	flags := binary.LittleEndian.Uint32(b[20:])
	pos := 0x4C
	u16 := func(at int) (int, bool) {
		if at+2 > len(b) {
			return 0, false
		}
		return int(binary.LittleEndian.Uint16(b[at:])), true
	}
	u32 := func(at int) (int, bool) {
		if at < 0 || at+4 > len(b) {
			return 0, false
		}
		return int(binary.LittleEndian.Uint32(b[at:])), true
	}
	if flags&lnkHasIDList != 0 {
		n, ok := u16(pos)
		if !ok {
			return li, errNotLnk
		}
		pos += 2 + n
	}
	var local, suffix, network string
	if flags&lnkHasLinkInfo != 0 {
		size, ok := u32(pos)
		if !ok || size < 0x1C || pos+size > len(b) {
			return li, errNotLnk
		}
		info := b[pos : pos+size]
		hdr := int(binary.LittleEndian.Uint32(info[4:]))
		lflags := binary.LittleEndian.Uint32(info[8:])
		off := func(at int) int { return int(binary.LittleEndian.Uint32(info[at:])) }
		if lflags&1 != 0 {
			if hdr >= 0x24 && size >= 0x24 && off(0x1C) > 0 {
				local = utf16z(info, off(0x1C))
			}
			if local == "" {
				local = ansiz(info, off(0x10))
			}
		}
		if lflags&2 != 0 {
			if o := off(0x14); o > 0 && o+0x14 <= len(info) {
				netName := int(binary.LittleEndian.Uint32(info[o+8:]))
				network = ansiz(info, o+netName)
			}
		}
		if hdr >= 0x24 && size >= 0x24 && off(0x20) > 0 {
			suffix = utf16z(info, off(0x20))
		} else {
			suffix = ansiz(info, off(0x18))
		}
		pos += size
	}
	unicode := flags&lnkUnicode != 0
	str := func() string {
		n, ok := u16(pos)
		if !ok {
			return ""
		}
		pos += 2
		if unicode {
			end := pos + 2*n
			if end > len(b) {
				end = len(b)
			}
			s := decodeUTF16(b[pos:end])
			pos = end
			return s
		}
		end := pos + n
		if end > len(b) {
			end = len(b)
		}
		s := string(b[pos:end])
		pos = end
		return s
	}
	var rel, args string
	if flags&lnkHasName != 0 {
		str()
	}
	if flags&lnkHasRelPath != 0 {
		rel = str()
	}
	if flags&lnkHasWorkDir != 0 {
		str()
	}
	if flags&lnkHasArgs != 0 {
		args = str()
	}
	if flags&lnkHasIcon != 0 {
		str()
	}
	li.Args = args
	// extra data: an environment-variable target, or Windows Installer
	var envTarget string
	for pos+8 <= len(b) {
		size := int(binary.LittleEndian.Uint32(b[pos:]))
		if size < 8 || pos+size > len(b) {
			break
		}
		switch binary.LittleEndian.Uint32(b[pos+4:]) {
		case 0xA0000001: // environment variables
			if size >= 8+260+520 {
				envTarget = decodeUTF16(b[pos+8+260 : pos+8+260+520])
				if envTarget == "" {
					envTarget = ansiz(b[pos+8:pos+8+260], 0)
				}
			}
		case 0xA0000006: // Windows Installer ("advertised" shortcut)
			li.Special = true
		}
		pos += size
	}
	if flags&lnkHasDarwin != 0 {
		li.Special = true
	}
	switch {
	case local != "":
		li.Target = joinLnk(local, suffix)
	case network != "":
		li.Target, li.Network = joinLnk(network, suffix), true
	case envTarget != "":
		li.Target = expandPercent(envTarget, os.Getenv)
	case rel != "":
		li.Target = filepath.Clean(filepath.Join(dir, rel))
	default:
		li.Special = true // a shell item only (Store app, Control Panel, ...)
	}
	if strings.HasPrefix(li.Target, `\\`) {
		li.Network = true
	}
	return li, nil
}

func joinLnk(base, suffix string) string {
	if suffix == "" {
		return base
	}
	if strings.HasSuffix(base, `\`) {
		return base + suffix
	}
	return base + `\` + suffix
}

func ansiz(b []byte, at int) string {
	if at <= 0 || at >= len(b) {
		return ""
	}
	end := at
	for end < len(b) && b[end] != 0 {
		end++
	}
	return string(b[at:end])
}

func utf16z(b []byte, at int) string {
	if at <= 0 || at >= len(b) {
		return ""
	}
	return decodeUTF16(b[at:])
}

// decodeUTF16 reads little-endian UTF-16 up to the first 0.
func decodeUTF16(b []byte) string {
	var u []uint16
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}
