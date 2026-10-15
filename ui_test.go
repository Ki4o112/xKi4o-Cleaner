package main

import (
	"os"
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
