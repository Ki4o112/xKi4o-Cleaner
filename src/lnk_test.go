package main

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// item-list pieces the way Windows writes them
func idRoot(clsid []byte) []byte {
	it := make([]byte, 20)
	binary.LittleEndian.PutUint16(it, 20)
	it[2], it[3] = 0x1F, 0x50
	copy(it[4:], clsid)
	return it
}

func idDrive(d string) []byte {
	it := make([]byte, 25)
	binary.LittleEndian.PutUint16(it, 25)
	it[2] = 0x2F
	copy(it[3:], d)
	return it
}

func u16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// idFile: a folder (dir) or file item, short 8.3 name and the long name in a
// 0xBEEF0004 block of the given version (0: no block).
func idFile(short, long string, dir bool, ver int) []byte {
	it := []byte{0, 0, 0x32, 0, 0, 0, 0, 0, 0x21, 0x50, 0x14, 0x6B, 0x20, 0}
	if dir {
		it[2], it[12] = 0x31, 0x10
	}
	it = append(it, short...)
	it = append(it, 0)
	if len(it)%2 != 0 {
		it = append(it, 0)
	}
	if ver > 0 {
		off := map[int]int{3: 0x14, 7: 0x26, 8: 0x2A, 9: 0x2E}[ver]
		ext := append(make([]byte, 0, 128), make([]byte, off)...)
		binary.LittleEndian.PutUint16(ext[2:], uint16(ver))
		binary.LittleEndian.PutUint32(ext[4:], 0xBEEF0004)
		binary.LittleEndian.PutUint16(ext[16:], uint16(off))
		ext = append(ext, u16z(long)...)
		ext = append(ext, byte(len(it)), 0) // offset of this block
		binary.LittleEndian.PutUint16(ext, uint16(len(ext)))
		it = append(it, ext...)
	}
	binary.LittleEndian.PutUint16(it, uint16(len(it)))
	return it
}

func idList(items ...[]byte) []byte {
	var b []byte
	for _, it := range items {
		b = append(b, it...)
	}
	return append(b, 0, 0)
}

func TestIDListPath(t *testing.T) {
	epic := idList(idRoot(clsidThisPC), idDrive(`C:\`),
		idFile("PROGRA~2", "Program Files (x86)", true, 9),
		idFile("EPICGA~1", "Epic Games", true, 3),
		idFile("Launcher", "Launcher", true, 7),
		idFile("Win64", "", true, 0),
		idFile("EPICGA~1.EXE", "EpicGamesLauncher.exe", false, 8))
	if got := idListPath(epic); got != `C:\Program Files (x86)\Epic Games\Launcher\Win64\EpicGamesLauncher.exe` {
		t.Errorf("epic: %q", got)
	}
	other := make([]byte, 16) // some other root (Control Panel, a library, ...)
	other[0] = 1
	for name, b := range map[string][]byte{
		"other root": idList(idRoot(other), idDrive(`C:\`), idFile("A", "A", true, 9)),
		"no drive":   idList(idRoot(clsidThisPC), idFile("A", "A", true, 9)),
		"cut":        idList(idRoot(clsidThisPC), idDrive(`C:\`))[:30],
		"empty":      nil,
	} {
		if got := idListPath(b); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
	// a shortcut written by another tool (pylnk3): an item list only
	b, _ := hex.DecodeString(epicLnkHex)
	li, err := parseLnk(b, `C:\Users\x\Desktop`)
	if err != nil || li.Special || li.Target != `C:\Program Files (x86)\Epic Games\Launcher\Portal\Binaries\Win64\EpicGamesLauncher.exe` {
		t.Errorf("pylnk3: %+v %v", li, err)
	}
}

func TestAddWow(t *testing.T) {
	pairs := [][2]string{{`C:\Program Files`, `C:\Program Files (x86)`}, {`C:\Windows\System32`, `C:\Windows\SysWOW64`}}
	cases := map[string][]string{
		`C:\Program Files\Epic Games\l.exe`: {`C:\Program Files (x86)\Epic Games\l.exe`},
		`c:\program files (x86)\A\b.exe`:    {`C:\Program Files\A\b.exe`},
		`C:\Windows\System32\notepad.exe`:   {`C:\Windows\SysWOW64\notepad.exe`},
		`C:\Program Files`:                  {`C:\Program Files (x86)`},
		`C:\Games\x.exe`:                    nil,
		`C:\Program Files Extra\x.exe`:      nil,
	}
	for target, want := range cases {
		li := lnkInfo{Target: target}
		addWow(&li, pairs)
		if !reflect.DeepEqual(li.Alts, want) {
			t.Errorf("%s: %q, want %q", target, li.Alts, want)
		}
	}
	li := lnkInfo{Target: `C:\Program Files\A\a.exe`, Alts: []string{`C:\Program Files (x86)\A\a.exe`}}
	addWow(&li, pairs)
	if len(li.Alts) != 1 {
		t.Errorf("twice: %q", li.Alts)
	}
	li = lnkInfo{Target: `C:\Program Files\A\a.exe`, Special: true}
	if addWow(&li, pairs); li.Alts != nil {
		t.Errorf("special: %q", li.Alts)
	}
}

// A shortcut whose recorded path is gone but whose other path exists opens
// that one (and is not broken).
func TestLnkAlternativeTarget(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "app.exe")
	mk(t, real, 10, 0)
	p := filepath.Join(dir, "x.lnk")
	makeLnk(t, p, filepath.Join(dir, "gone", "app.exe"))
	b, _ := os.ReadFile(p)
	b = b[:len(b)-4] // before the end of the extra data: an environment-variable target
	env := make([]byte, 8+260+520)
	binary.LittleEndian.PutUint32(env, uint32(len(env)))
	binary.LittleEndian.PutUint32(env[4:], 0xA0000001)
	copy(env[8+260:], u16z(real))
	b = append(append(b, env...), 0, 0, 0, 0)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	li, err := readLnkFile(p)
	if err != nil || li.Target != real || len(li.Alts) == 0 || !strings.HasSuffix(li.Alts[0], filepath.Join("gone", "app.exe")) {
		t.Fatalf("%+v %v", li, err)
	}
}

// written by pylnk3 for C:\Program Files (x86)\Epic Games\Launcher\Portal\Binaries\Win64\EpicGamesLauncher.exe
const epicLnkHex = "" +
	"4c0000000114020000000000c0000000000000468101000000000000007eb53e885ddd01007eb53e885ddd01007eb53e" +
	"885ddd010000000000000000010000000000000000000000000000002b0214001f50e04fd020ea3a6910a2d808002b30" +
	"309d19002f433a5c000000000000000000000000000000000000006000320000000000505dc498100050726f6772616d" +
	"2046696c6573202878383629003e0003000400efbe505dc498505dc49814000000500072006f006700720061006d0020" +
	"00460069006c0065007300200028007800380036002900000022004600320000000000505dc498100045706963204761" +
	"6d657300002c0003000400efbe505dc498505dc4981400000045007000690063002000470061006d006500730000001a" +
	"004000320000000000505dc49810004c61756e636865720000280003000400efbe505dc498505dc498140000004c0061" +
	"0075006e006300680065007200000018003a00320000000000505dc4981000506f7274616c0000240003000400efbe50" +
	"5dc498505dc4981400000050006f007200740061006c00000016004000320000000000505dc498100042696e61726965" +
	"730000280003000400efbe505dc498505dc49814000000420069006e0061007200690065007300000018003600320000" +
	"000000505dc498100057696e363400220003000400efbe505dc498505dc49814000000570069006e0036003400000014" +
	"006600320000000000505dc49810004570696347616d65734c61756e636865722e65786500420003000400efbe505dc4" +
	"98505dc498140000004500700069006300470061006d00650073004c00610075006e0063006800650072002e00650078" +
	"00650000002400000000000000"
