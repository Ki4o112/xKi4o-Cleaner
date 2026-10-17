package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The page is a classic script: a top-level function or var with the same
// name as a Go binding replaces window[name]. That once made "startClean"
// call itself until the stack ran out (the cleaning never reached Go) and
// kept "saveSettings" from ever saving. The page calls the bindings through
// window.__go, and no page global may take a binding's name.
func TestUIDoesNotShadowBindings(t *testing.T) {
	goSrc, err := os.ReadFile("main_windows.go")
	if os.IsNotExist(err) {
		t.Skip("the source files are not next to this test program")
	}
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile("ui/app.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(goSrc)
	i, j := strings.Index(src, "binds := map[string]any{"), strings.Index(src, "for name, f := range binds")
	if i < 0 || j < i {
		t.Fatal("bindings table not found")
	}
	names := regexp.MustCompile(`(?m)^\s*"(\w+)":\s`).FindAllStringSubmatch(src[i:j], -1)
	if len(names) < 50 {
		t.Fatalf("only %d bindings found", len(names))
	}
	page := string(html)
	at := strings.Index(page, "<script>")
	if at < 0 {
		t.Fatal("no script in the page")
	}
	script := page[at:]
	for _, m := range names {
		n := m[1]
		for _, p := range []string{`(?m)^\s*(async\s+)?function\s+` + n + `\s*\(`, `(?m)^\s*var\s+` + n + `\b`, `window\.` + n + `\s*=[^=]`} {
			if regexp.MustCompile(p).MatchString(script) {
				t.Errorf("the page declares %q, which replaces the Go binding of the same name", n)
			}
		}
	}
	if !strings.Contains(script, "const GO = window.__go || {}") || !strings.Contains(script, "GO[name](...a)") {
		t.Error("the page does not call the bindings through window.__go")
	}
	wv, err := os.ReadFile("third_party/go-webview2/webview.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wv), "(window.__go = window.__go || {})[name] = window[name];") {
		t.Error("go-webview2 does not keep the bindings in window.__go")
	}
}

// every tweak of the catalogue has its name and description in both
// languages, and is in the page's demo list
func TestTweaksHaveTexts(t *testing.T) {
	src, err1 := os.ReadFile("tweaklist_windows.go")
	page, err2 := os.ReadFile(filepath.Join("ui", "app.html"))
	if err1 != nil || err2 != nil {
		t.Skip("the source files are not here")
	}
	ids := regexp.MustCompile(`\{id: "([a-z0-9-]+)", group:`).FindAllStringSubmatch(string(src), -1)
	if len(ids) < 60 {
		t.Fatalf("only %d tweaks found", len(ids))
	}
	p := string(page)
	for _, m := range ids {
		id := m[1]
		for _, k := range []string{`"tw.` + id + `.n":`, `"tw.` + id + `.d":`} {
			if n := strings.Count(p, k); n != 2 {
				t.Errorf("%s: %d texts (want Bulgarian and English)", k, n)
			}
		}
		if !strings.Contains(p, `["`+id+`", "`) {
			t.Errorf("%s: not in the page's demo list", id)
		}
	}
}

// The page's demo has the same presets as the app (so what the tests and
// the screenshots show is what the app does).
func TestPresetsMatchThePage(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("ui", "app.html"))
	if err != nil {
		t.Skip("the source files are not here")
	}
	s := string(page)
	i := strings.Index(s, "tweakPresets: async () => [")
	if i < 0 {
		t.Fatal("no tweakPresets in the page's demo")
	}
	block := s[i : i+strings.Index(s[i:], "\n    ],")]
	re := regexp.MustCompile(`\{ id: "([a-z]+)", icon: "([a-z]+)", ids: \[([^\]]*)\] \}`)
	got := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		ids := strings.ReplaceAll(strings.ReplaceAll(m[3], `"`, ""), " ", "")
		got[m[1]] = m[2] + ":" + ids
	}
	for _, p := range tweakPresets() {
		if want := p.Icon + ":" + strings.Join(p.IDs, ","); got[p.ID] != want {
			t.Errorf("preset %s:\n page %s\n app  %s", p.ID, got[p.ID], want)
		}
	}
	if len(got) != len(tweakPresets()) {
		t.Errorf("%d presets in the page", len(got))
	}
}
