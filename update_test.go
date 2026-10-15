package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		newer           bool
	}{
		{"v2.6.2", "2.6.1", true}, {"v2.7.0", "2.6.9", true}, {"3.0", "2.10.4", true}, {"v2.10.0", "2.9.9", true},
		{"v2.6.1", "2.6.1", false}, {"v2.6.0", "2.6.1", false}, {"v1.9.9", "2.0.0", false},
		{"latest", "2.6.1", false}, {"v2.6.1-beta", "2.6.0", false}, {"", "2.6.1", false}, {"v2.6.1.4", "2.6.1", false},
	} {
		if got := newerVersion(c.latest, c.current); got != c.newer {
			t.Errorf("%q vs %q: %v", c.latest, c.current, got)
		}
	}
}

func TestReleaseInfo(t *testing.T) {
	good := ghAsset{Name: "xKi4oCleaner.exe", URL: "https://github.com/" + updateRepo + "/releases/download/v2.7.0/xKi4oCleaner.exe", Size: 5 << 20}
	r := ghRelease{Tag: "v2.7.0", Name: "xKi4o Cleaner 2.7.0", Body: "## English\n- new", HTML: "https://github.com/x", Assets: []ghAsset{
		{Name: "xKi4oCleaner.exe", URL: "https://evil.example/xKi4oCleaner.exe", Size: 5 << 20}, good}}
	v, a := releaseInfo(r, "2.6.1")
	if !v.Newer || v.Latest != "2.7.0" || !v.CanInstall || a == nil || a.URL != good.URL || v.Notes != r.Body {
		t.Fatalf("%+v %+v", v, a)
	}
	// a release without the exe can be shown, not installed
	r.Assets = r.Assets[:1]
	if v, a := releaseInfo(r, "2.6.1"); !v.Newer || v.CanInstall || a != nil {
		t.Errorf("foreign asset accepted: %+v", v)
	}
	// drafts and pre-releases are never offered
	r.Pre = true
	if v, _ := releaseInfo(r, "2.6.1"); v.Newer {
		t.Error("pre-release offered")
	}
}

func TestCheckUpdateAgainstAServer(t *testing.T) {
	status, body := 200, `{"tag_name":"v9.0.0","name":"9.0","body":"notes","html_url":"https://github.com/r","assets":[{"name":"xKi4oCleaner.exe","browser_download_url":"https://github.com/`+updateRepo+`/releases/download/v9.0.0/xKi4oCleaner.exe","size":123}]}`
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	old := updateAPI
	updateAPI = srv.URL
	defer func() { updateAPI = old }()
	v := checkUpdate()
	if !v.Newer || v.Latest != "9.0.0" || !v.CanInstall || v.Size != 123 || v.Error != "" || v.Checked == 0 {
		t.Fatalf("%+v", v)
	}
	if !strings.HasPrefix(ua, "xKi4o-Cleaner/") {
		t.Errorf("user agent %q", ua)
	}
	status, body = 404, `{}`
	if v := checkUpdate(); v.Newer || v.Error != T("upd.err.none") {
		t.Errorf("404: %+v", v)
	}
	status = 403
	if v := checkUpdate(); v.Error != T("upd.err.limit") {
		t.Errorf("403: %+v", v)
	}
	updateAPI = "http://127.0.0.1:1/nothing"
	if v := checkUpdate(); v.Newer || !strings.Contains(v.Error, "GitHub") {
		t.Errorf("offline: %+v", v)
	}
}

func TestDownloadVerified(t *testing.T) {
	exe := append([]byte("MZ"), []byte(strings.Repeat("x", 300000))...)
	sum := sha256.Sum256(exe)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write(exe)
		case "/notexe":
			_, _ = w.Write(append([]byte("PK"), exe[2:]...))
		case "/away":
			http.Redirect(w, r, "https://evil.example/x.exe", http.StatusFound)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "new.exe")
	var calls int
	err := downloadVerified(context.Background(), ghAsset{URL: srv.URL + "/ok", Size: int64(len(exe)), Digest: digest}, dst, func(done, total int64) { calls++ })
	if err != nil || calls == 0 {
		t.Fatalf("%v (%d progress calls)", err, calls)
	}
	if b, _ := os.ReadFile(dst); len(b) != len(exe) {
		t.Fatalf("saved %d bytes", len(b))
	}
	for name, c := range map[string]struct {
		a    ghAsset
		want error
	}{
		"wrong hash":  {ghAsset{URL: srv.URL + "/ok", Size: int64(len(exe)), Digest: "sha256:" + strings.Repeat("0", 64)}, errBadHash},
		"wrong size":  {ghAsset{URL: srv.URL + "/ok", Size: int64(len(exe)) + 1}, errBadFile},
		"too big":     {ghAsset{URL: srv.URL + "/ok", Size: 1000}, errBadFile},
		"not an exe":  {ghAsset{URL: srv.URL + "/notexe", Size: int64(len(exe))}, errBadFile},
		"other place": {ghAsset{URL: srv.URL + "/away", Size: int64(len(exe))}, errBadHost},
	} {
		bad := filepath.Join(dir, "bad.exe")
		err := downloadVerified(context.Background(), c.a, bad, nil)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if _, err := os.Stat(bad); !os.IsNotExist(err) {
			t.Errorf("%s: a refused download was left on disk", name)
		}
	}
}

func TestSwapExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "xKi4oCleaner.exe")
	fresh := updateTemp(exe)
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	_ = os.WriteFile(fresh, []byte("new"), 0o755)
	_ = os.WriteFile(exe+".old", []byte("older"), 0o755) // left from an earlier update
	if err := swapExe(exe, fresh); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Errorf("exe: %s", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old" {
		t.Errorf("old: %s", b)
	}
	// the new file is missing: the running one is put back
	if err := swapExe(exe, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("no error")
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Errorf("not put back: %s", b)
	}
	_ = os.WriteFile(fresh, []byte("half"), 0o644)
	cleanupOldExe(exe)
	for _, p := range []string{exe + ".old", fresh} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there", p)
		}
	}
}

func TestDownloadHosts(t *testing.T) {
	for _, c := range []struct {
		u  string
		ok bool
	}{
		{"https://github.com/a", true}, {"https://objects.githubusercontent.com/x", true}, {"https://release-assets.githubusercontent.com/x", true},
		{"http://github.com/a", false}, {"https://github.com.evil.example/a", false}, {"https://evilgithubusercontent.com/x", false},
	} {
		u, _ := url.Parse(c.u)
		if downloadHostOK(u) != c.ok {
			t.Errorf("%s", c.u)
		}
	}
}

// The changelog shipped inside the exe has this version, in both languages,
// newest first; the release workflow takes its notes from the same section.
func TestChangelog(t *testing.T) {
	heads := regexp.MustCompile(`(?m)^## (\d+\.\d+\.\d+)\b`).FindAllStringSubmatch(changelogMD, -1)
	if len(heads) == 0 || heads[0][1] != appVersion {
		t.Fatalf("the newest section is not %s: %v", appVersion, heads)
	}
	for i := 1; i < len(heads); i++ {
		if !newerVersion(heads[i-1][1], heads[i][1]) {
			t.Errorf("%s is listed before %s", heads[i-1][1], heads[i][1])
		}
	}
	at := strings.Index(changelogMD, "## "+appVersion)
	if at < 0 {
		t.Fatal("no section")
	}
	sec := changelogMD[at:]
	if i := strings.Index(sec[3:], "\n## "); i >= 0 {
		sec = sec[:i+3]
	}
	if !strings.Contains(sec, "### English") || !strings.Contains(sec, "### Български") {
		t.Errorf("section %s needs both languages", appVersion)
	}
}
