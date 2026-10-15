package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mk(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		ts := time.Now().Add(-age)
		_ = os.Chtimes(path, ts, ts)
	}
}

func age(t *testing.T, path string, d time.Duration) {
	ts := time.Now().Add(-d)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestSafeTarget(t *testing.T) {
	root := t.TempDir()
	protect(filepath.Join(root, "prot"))
	cases := map[string]bool{
		"":                               false,
		"relative/path":                  false,
		"/":                              false,
		"/tmp":                           false,
		filepath.Join(root, "a"):         true,
		filepath.Join(root, "prot"):      false,
		filepath.Join(root, "prot", "x"): true,
	}
	// ".." parts are cleaned first: root/a/../b is root/b
	sep := string(os.PathSeparator)
	cases[root+sep+"a"+sep+".."+sep+"b"] = true
	for p, want := range cases {
		if got := safeTarget(p); got != want {
			t.Errorf("safeTarget(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestContentsKeepsFolderAndNeverFollowsLinks(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "app", "Cache")
	outside := filepath.Join(root, "precious")
	mk(t, filepath.Join(cache, "a.bin"), 1000, 0)
	mk(t, filepath.Join(cache, "sub", "b.bin"), 500, 0)
	mk(t, filepath.Join(outside, "keep.txt"), 42, 0)
	if err := os.Symlink(outside, filepath.Join(cache, "link-to-precious")); err != nil {
		t.Skip("symlinks not permitted here:", err)
	}

	b, n := sizeTargets([]target{{Path: cache, Kind: kContents}})
	if b != 1500 || n != 2 {
		t.Fatalf("size = %d bytes / %d files, want 1500 / 2", b, n)
	}
	st := cleanTargets([]target{{Path: cache, Kind: kContents}}, nil)
	if st.bytes != 1500 {
		t.Fatalf("freed %d, want 1500", st.bytes)
	}
	if !exists(cache) {
		t.Fatal("the cache folder itself must stay")
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("cache not empty: %v", entries)
	}
	if !exists(filepath.Join(outside, "keep.txt")) {
		t.Fatal("followed a link and deleted a file outside the target")
	}
}

func TestTargetThatIsALinkIsIgnored(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real", "data")
	mk(t, filepath.Join(real, "x.txt"), 10, 0)
	link := filepath.Join(root, "cache", "Cache")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not permitted here:", err)
	}
	cleanTargets([]target{{Path: link, Kind: kContents}}, nil)
	if !exists(filepath.Join(real, "x.txt")) {
		t.Fatal("a target that is itself a link must not be cleaned")
	}
}

func TestOlderThan24hKeepsLowAndRecent(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, "user", "Temp")
	mk(t, filepath.Join(tmp, "old.tmp"), 100, 72*time.Hour)
	mk(t, filepath.Join(tmp, "new.tmp"), 100, 0)
	mk(t, filepath.Join(tmp, "olddir", "f"), 100, 72*time.Hour)
	age(t, filepath.Join(tmp, "olddir"), 72*time.Hour)
	mk(t, filepath.Join(tmp, "Low", "f"), 100, 72*time.Hour)
	age(t, filepath.Join(tmp, "Low"), 72*time.Hour)

	st := cleanTargets([]target{{Path: tmp, Kind: kOlder}}, nil)
	if st.bytes != 200 {
		t.Fatalf("freed %d, want 200", st.bytes)
	}
	for p, want := range map[string]bool{"old.tmp": false, "olddir": false, "new.tmp": true, "Low": true, "Low/f": true} {
		if got := exists(filepath.Join(tmp, p)); got != want {
			t.Errorf("%s exists=%v, want %v", p, got, want)
		}
	}
}

func TestGlobRecursive(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "Windows", "LiveKernelReports")
	mk(t, filepath.Join(d, "WATCHDOG", "WD-1.dmp"), 300, 0)
	mk(t, filepath.Join(d, "x.DMP"), 200, 0)
	mk(t, filepath.Join(d, "keep.txt"), 50, 0)
	st := cleanTargets([]target{{Path: d, Kind: kGlob, Pattern: "*.dmp", Recurse: true}}, nil)
	if st.bytes != 500 || !exists(filepath.Join(d, "keep.txt")) {
		t.Fatalf("freed %d, keep.txt exists=%v", st.bytes, exists(filepath.Join(d, "keep.txt")))
	}
}

func TestKeepNewest(t *testing.T) {
	root := t.TempDir()
	ota := filepath.Join(root, "NVIDIA App", "ota-artifacts")
	mk(t, filepath.Join(ota, "grd", "v1", "setup.exe"), 100, 0)
	mk(t, filepath.Join(ota, "grd", "v2", "setup.exe"), 100, 0)
	mk(t, filepath.Join(ota, "grd", "v3", "setup.exe"), 100, 0)
	mk(t, filepath.Join(ota, "grd", "v4", "setup.exe"), 100, 0)
	mk(t, filepath.Join(ota, "app", "only", "a"), 100, 0)
	age(t, filepath.Join(ota, "grd", "v1"), 120*time.Hour)
	age(t, filepath.Join(ota, "grd", "v2"), 96*time.Hour)
	age(t, filepath.Join(ota, "grd", "v3"), 2*time.Hour) // recent: stays even though not newest
	age(t, filepath.Join(ota, "grd", "v4"), 1*time.Hour) // newest
	age(t, filepath.Join(ota, "app", "only"), 400*time.Hour)

	cleanTargets([]target{{Path: ota, Kind: kKeepNewest}}, nil)
	want := map[string]bool{"grd/v1": false, "grd/v2": false, "grd/v3": true, "grd/v4": true, "app/only": true}
	for p, w := range want {
		if got := exists(filepath.Join(ota, p)); got != w {
			t.Errorf("%s exists=%v, want %v", p, got, w)
		}
	}
}

func TestWholeAndStop(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "Windows", "SoftwareDistribution.old")
	mk(t, filepath.Join(old, "a", "b"), 10, 0)
	cleanTargets([]target{{Path: old, Kind: kWhole}}, nil)
	if exists(old) {
		t.Fatal("kWhole should remove the folder itself")
	}
	keep := filepath.Join(root, "x", "Cache")
	mk(t, filepath.Join(keep, "f"), 10, 0)
	cleanTargets([]target{{Path: keep, Kind: kContents}}, func() bool { return true })
	if !exists(filepath.Join(keep, "f")) {
		t.Fatal("stop() must prevent further cleaning")
	}
}

func TestLineSplitterDISM(t *testing.T) {
	var got []string
	sp := &lineSplitter{onLine: func(s string, r bool) {
		if r {
			s = "~" + s
		}
		got = append(got, s)
	}}
	out := "Deployment Image Servicing\r\n\r\n\r\n[=  10.0%  ]" // chunk 1
	sp.feed(out)
	sp.feed("\r[==  20.0%  ]\r[===100.0%===]\r\nThe operation completed successfully.\r\n")
	sp.flush()
	want := []string{"Deployment Image Servicing", "", "~[=  10.0%  ]", "~[==  20.0%  ]", "~[===100.0%===]", "The operation completed successfully."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if p := percentIn("[==  42,5%  ]"); p != 42.5 {
		t.Fatalf("percent = %v", p)
	}
	if p := percentIn("Проверката е завършена на 37%."); p != 37 {
		t.Fatalf("percent = %v", p)
	}
	if p := percentIn("no number"); p != -1 {
		t.Fatalf("percent = %v", p)
	}
}

func TestUTF16Chunks(t *testing.T) {
	src := "Verification 5% complete.\r\nПроверка"
	var b []byte
	for _, r := range src {
		b = append(b, byte(r), byte(r>>8))
	}
	if !looksUTF16(b) {
		t.Fatal("should detect UTF-16")
	}
	if looksUTF16([]byte("plain ascii output here")) {
		t.Fatal("ascii detected as UTF-16")
	}
	var d utf16Decoder
	got := d.decode(b[:7]) + d.decode(b[7:20]) + d.decode(b[20:])
	if got != src {
		t.Fatalf("got %q", got)
	}
}

func TestSteamLibraries(t *testing.T) {
	vdf := "\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\"C:\\\\Program Files (x86)\\\\Steam\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\"D:\\\\SteamLibrary\"\n\t}\n}\n"
	got := mergeLibraries(`C:\Program Files (x86)\Steam`, vdf)
	if len(got) != 2 || got[1] != `D:\SteamLibrary` {
		t.Fatalf("got %q", got)
	}
}

func TestTexts(t *testing.T) {
	setLang("en")
	if T("err.dism", 5) != "DISM returned code 5" {
		t.Fatal(T("err.dism", 5))
	}
	setLang("bg")
	if T("blk.session") != "Работи под друг потребител — не се затваря." {
		t.Fatal(T("blk.session"))
	}
	// every text has both languages, and the same placeholders in both
	for k, v := range texts {
		if v[0] == "" || v[1] == "" {
			t.Errorf("%s: missing translation", k)
		}
		if strings.Count(v[0], "%") != strings.Count(v[1], "%") {
			t.Errorf("%s: placeholders differ", k)
		}
	}
	if T("missing.key") != "missing.key" {
		t.Fatal("missing key")
	}
}
