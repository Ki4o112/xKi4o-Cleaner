package main

// Reading Windows shortcuts (.lnk, the [MS-SHLLINK] format) to know where
// they point: the local path, a network path, or "advertised" shortcuts of
// Windows Installer and Store apps that have no plain path.

import (
	"bytes"
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
	// Alts: the other places the shortcut names for its target (its item
	// list, its environment-variable path, the 32/64-bit twin folder).
	// Windows opens the first one that exists.
	Alts []string `json:"-"`
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
	li, err := parseLnk(b[:n], filepath.Dir(path))
	if err != nil {
		return li, err
	}
	addWow(&li, wowPairs())
	// the target is the first place that exists, like Windows does
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil } // a file or a folder
	if li.Target != "" && !li.Special && !li.Network && !exists(li.Target) {
		for i, a := range li.Alts {
			if exists(a) {
				li.Target, li.Alts[i] = a, li.Target
				break
			}
		}
	}
	return li, nil
}

// wowPairs: folders that 64-bit and 32-bit programs see under two names.
// A shortcut made by a 32-bit setup may name the other one.
func wowPairs() [][2]string {
	var out [][2]string
	add := func(a, b string) {
		if a != "" && b != "" && !strings.EqualFold(a, b) {
			out = append(out, [2]string{a, b})
		}
	}
	first := func(names ...string) string {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				return v
			}
		}
		return ""
	}
	add(first("ProgramW6432", "ProgramFiles"), os.Getenv("ProgramFiles(x86)"))
	add(first("CommonProgramW6432", "CommonProgramFiles"), os.Getenv("CommonProgramFiles(x86)"))
	if w := first("SystemRoot", "WINDIR"); w != "" {
		add(w+`\System32`, w+`\SysWOW64`)
	}
	return out
}

// addWow adds the 32/64-bit twin of every place the shortcut names.
func addWow(li *lnkInfo, pairs [][2]string) {
	if li.Special || li.Network || li.Target == "" {
		return
	}
	seen := map[string]bool{strings.ToLower(li.Target): true}
	for _, a := range li.Alts {
		seen[strings.ToLower(a)] = true
	}
	for _, p := range append([]string{li.Target}, li.Alts...) {
		for _, pr := range pairs {
			for k := 0; k < 2; k++ {
				from, to := pr[k], pr[1-k]
				if !under(p, from) {
					continue
				}
				v := to + p[len(strings.TrimSuffix(from, `\`)):]
				if !seen[strings.ToLower(v)] {
					seen[strings.ToLower(v)] = true
					li.Alts = append(li.Alts, v)
				}
			}
		}
	}
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
	var idPath string
	if flags&lnkHasIDList != 0 {
		n, ok := u16(pos)
		if !ok || pos+2+n > len(b) {
			return li, errNotLnk
		}
		idPath = idListPath(b[pos+2 : pos+2+n])
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
	if envTarget != "" {
		envTarget = expandPercent(envTarget, os.Getenv)
	}
	switch {
	case local != "":
		li.Target = joinLnk(local, suffix)
	case network != "":
		li.Target, li.Network = joinLnk(network, suffix), true
	case envTarget != "":
		li.Target = envTarget
	case idPath != "" && !li.Special:
		li.Target = idPath
	case rel != "":
		li.Target = filepath.Clean(filepath.Join(dir, rel))
	default:
		li.Special = true // a shell item only (Store app, Control Panel, ...)
	}
	if strings.HasPrefix(li.Target, `\\`) {
		li.Network = true
	}
	if !li.Special && !li.Network {
		seen := map[string]bool{strings.ToLower(li.Target): true}
		for _, a := range []string{envTarget, idPath} {
			if a != "" && !seen[strings.ToLower(a)] {
				seen[strings.ToLower(a)] = true
				li.Alts = append(li.Alts, a)
			}
		}
	}
	return li, nil
}

// clsidThisPC is {20D04FE0-3AEA-1069-A2D8-08002B30309D} as stored in an item list.
var clsidThisPC = []byte{0xE0, 0x4F, 0xD0, 0x20, 0xEA, 0x3A, 0x69, 0x10, 0xA2, 0xD8, 0x08, 0x00, 0x2B, 0x30, 0x30, 0x9D}

// idListPath reads a shortcut's item list when it is a plain path
// (This PC, a drive, folders, the file). Anything else gives "".
func idListPath(b []byte) string {
	drive := ""
	var parts []string
	for i := 0; i+2 <= len(b); {
		n := int(binary.LittleEndian.Uint16(b[i:]))
		if n == 0 {
			break
		}
		if n < 3 || i+n > len(b) {
			return ""
		}
		it := b[i : i+n]
		i += n
		switch typ := it[2]; {
		case typ == 0x1F && len(parts) == 0 && drive == "": // the root: only This PC leads to a path
			if len(it) < 20 || !bytes.Equal(it[4:20], clsidThisPC) {
				return ""
			}
		case typ&0x70 == 0x20 && drive == "": // a drive
			s := ansiz(it, 3)
			if len(s) < 2 || s[1] != ':' || !isLetter(s[0]) {
				return ""
			}
			drive = strings.ToUpper(s[:1]) + `:\`
		case typ&0x70 == 0x30 && drive != "": // a folder or a file
			name := fileItemName(it)
			if name == "" || strings.ContainsAny(name, `\/:`) {
				return ""
			}
			parts = append(parts, name)
		default:
			return ""
		}
	}
	if drive == "" {
		return ""
	}
	return drive + strings.Join(parts, `\`)
}

func isLetter(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

// fileItemName: the long name of a folder or file item (in its 0xBEEF0004
// extension block), else its short 8.3 name.
func fileItemName(it []byte) string {
	if len(it) < 15 {
		return ""
	}
	var short string
	if it[2]&0x04 != 0 {
		short = decodeUTF16(it[14:])
	} else {
		short = ansiz(it, 14)
	}
	if k := bytes.Index(it[14:], []byte{0x04, 0x00, 0xEF, 0xBE}); k >= 4 {
		ext := it[14+k-4:]
		size := int(binary.LittleEndian.Uint16(ext))
		ver := binary.LittleEndian.Uint16(ext[2:])
		if size > len(ext) {
			size = len(ext)
		}
		if ver >= 3 && size >= 20 {
			off := int(binary.LittleEndian.Uint16(ext[16:])) // where the long name starts
			if off < 20 || off >= size {
				off = map[bool]int{true: 0x2E, false: 0x14}[ver >= 9]
				switch ver {
				case 7:
					off = 0x26
				case 8:
					off = 0x2A
				}
			}
			if off < size {
				if s := decodeUTF16(ext[off:size]); s != "" {
					return s
				}
			}
		}
	}
	return short
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
