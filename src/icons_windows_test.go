//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// decodeIcon checks a data URL is a real, visible PNG picture.
func decodeIcon(t *testing.T, key, v string) {
	t.Helper()
	const pre = "data:image/png;base64,"
	if !strings.HasPrefix(v, pre) {
		t.Fatalf("%s: no PNG (%.40q)", key, v)
	}
	b, err := base64.StdEncoding.DecodeString(v[len(pre):])
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	r := img.Bounds()
	if r.Dx() < 16 || r.Dy() < 16 {
		t.Fatalf("%s: %v", key, r)
	}
	seen := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				seen++
			}
		}
	}
	if seen < r.Dx()*r.Dy()/20 {
		t.Fatalf("%s: almost empty picture (%d visible pixels)", key, seen)
	}
}

func TestIconsFor(t *testing.T) {
	cmd := sys32("cmd.exe")
	keys := []string{"exe:" + cmd, "ext:.txt", "dir:", "file:" + cmd}
	start := time.Now()
	got := iconsFor(keys)
	for _, k := range keys {
		decodeIcon(t, k, got[k])
	}
	t.Logf("%d icons in %v", len(keys), time.Since(start))
	// asked again: from memory
	start = time.Now()
	again := iconsFor(keys)
	if again["exe:"+cmd] != got["exe:"+cmd] || time.Since(start) > 200*time.Millisecond {
		t.Errorf("not cached (%v)", time.Since(start))
	}
	// nothing is read for wrong keys
	for _, k := range []string{"exe:relative.exe", "ext:bad/x", "ext:" + strings.Repeat("x", 30), "app:no-such-category", "proc:nothing-like-this-xyz.exe", "proc:notepad", "weird"} {
		if v := iconsFor([]string{k})[k]; v != "" {
			t.Errorf("%s gave an icon", k)
		}
	}
}

func TestCleanExePath(t *testing.T) {
	win := windowsDir()
	for in, want := range map[string]string{
		`"C:\Program Files\App\app.exe",0`:  `C:\Program Files\App\app.exe`,
		`C:\Program Files\App\app.exe,-101`: `C:\Program Files\App\app.exe`,
		`"C:\a b\x.exe"`:                    `C:\a b\x.exe`,
		`%SystemRoot%\System32\cmd.exe`:     filepath.Join(win, `System32\cmd.exe`),
		`C:\x\setup.ico`:                    "",
		`app.exe`:                           "",
		``:                                  "",
	} {
		if got := cleanExePath(in); !strings.EqualFold(got, want) {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if os.Getenv("SystemRoot") == "" {
		t.Skip("no SystemRoot")
	}
}
