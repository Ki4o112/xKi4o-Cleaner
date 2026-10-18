package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"
)

func TestParseIFile(t *testing.T) {
	orig := `C:\Users\Иван\Videos\клип.mp4`
	b := makeIFile(orig, 12345, 1_760_000_000_000)
	p, size, del, ok := parseIFile(b)
	if !ok || p != orig || size != 12345 || del != 1_760_000_000_000 {
		t.Fatalf("v2: %q %d %d %v", p, size, del, ok)
	}
	// format 1: fixed 260 characters
	v1 := make([]byte, 24+520)
	binary.LittleEndian.PutUint64(v1[0:], 1)
	binary.LittleEndian.PutUint64(v1[8:], 77)
	for i, c := range utf16.Encode([]rune(`D:\old.txt`)) {
		binary.LittleEndian.PutUint16(v1[24+i*2:], c)
	}
	if p, size, _, ok := parseIFile(v1); !ok || p != `D:\old.txt` || size != 77 {
		t.Fatalf("v1: %q %d %v", p, size, ok)
	}
	for _, bad := range [][]byte{nil, make([]byte, 30), append([]byte{9}, make([]byte, 40)...)} {
		if _, _, _, ok := parseIFile(bad); ok {
			t.Errorf("accepted %v", bad)
		}
	}
	short := makeIFile(orig, 1, 1)
	if _, _, _, ok := parseIFile(short[:len(short)-6]); ok {
		t.Error("accepted a cut file")
	}
}

// A file the app recycled is found in the Recycle Bin and put back; a file
// that exists again at the old place is never overwritten.
func TestRestoreRecycled(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "$Recycle.Bin", "S-1-5-21-1")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	rlog.mu.Lock()
	oldPath, oldList := rlog.path, rlog.list
	rlog.path = filepath.Join(root, "recycled.json")
	rlog.list = nil
	rlog.mu.Unlock()
	defer func() { rlog.mu.Lock(); rlog.path, rlog.list = oldPath, oldList; rlog.mu.Unlock() }()
	binOf := func(string) string { return bin }

	recycle := func(p, id string) {
		data, _ := os.ReadFile(p)
		if err := os.Rename(p, filepath.Join(bin, "$R"+id+filepath.Ext(p))); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(bin, "$I"+id+filepath.Ext(p)), makeIFile(p, int64(len(data)), time.Now().UnixMilli()), 0o644)
	}
	a := filepath.Join(root, "Videos", "a.mp4")
	b := filepath.Join(root, "Videos", "sub", "b.mp4")
	writeBytes(t, a, []byte("aaaa"), 0)
	writeBytes(t, b, []byte("bb"), 0)
	noteRecycled("dupes", []delResult{{Path: a, OK: true, Size: 4}, {Path: b, OK: true, Size: 2}, {Path: "x", OK: false}})
	recycle(a, "ABC123")
	recycle(b, "DEF456")
	_ = os.RemoveAll(filepath.Join(root, "Videos")) // the folder is gone too

	list := recycledList(binOf)
	if len(list) != 2 || !list[0].InBin || !list[1].InBin || list[0].Path != b {
		t.Fatalf("list: %+v", list)
	}
	if err := restoreRecycled(list[1].ID, binOf); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a); string(got) != "aaaa" {
		t.Fatalf("restored content %q", got)
	}
	if exists(filepath.Join(bin, "$IABC123.mp4")) || exists(filepath.Join(bin, "$RABC123.mp4")) {
		t.Error("bin entries left")
	}
	if err := restoreRecycled(list[1].ID, binOf); err == nil || err.Error() != "rc.err.done" {
		t.Errorf("second restore: %v", err)
	}
	// something new at the old place: never overwritten
	writeBytes(t, b, []byte("new"), 0)
	if err := restoreRecycled(list[0].ID, binOf); err != errBinExists {
		t.Errorf("over an existing file: %v", err)
	}
	if got, _ := os.ReadFile(b); string(got) != "new" {
		t.Error("existing file changed")
	}
	// the log survives a restart
	rlog.mu.Lock()
	rlog.load()
	n := len(rlog.list)
	rlog.mu.Unlock()
	if n != 2 {
		t.Errorf("reloaded %d", n)
	}
	// emptied from the bin: not restorable
	_ = os.Remove(filepath.Join(bin, "$RDEF456.mp4"))
	if l := recycledList(binOf); l[0].InBin {
		t.Error("gone item still restorable")
	}
	if err := restoreRecycled(list[0].ID, binOf); err != errBinMissing {
		t.Errorf("missing: %v", err)
	}
}
