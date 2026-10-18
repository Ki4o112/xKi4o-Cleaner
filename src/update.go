package main

// Updates: the app asks GitHub for the latest release of its own repository
// (one request, when it starts, and only if the setting is on). When a newer
// version is there, the page shows what is new; the new exe is downloaded only
// when the user clicks Update, checked (size, SHA-256, a Windows program) and
// put in place of the running one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateRepo  = "Ki4o112/xKi4o-Cleaner"
	updateAsset = "xKi4oCleaner.exe"
	updateMax   = 200 << 20 // no real build is near this size
)

var (
	updateAPI  = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	updateHTTP = &http.Client{Timeout: 20 * time.Second}
)

type ghAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>" (GitHub adds it to new uploads)
}

type ghRelease struct {
	Tag       string    `json:"tag_name"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	HTML      string    `json:"html_url"`
	Published string    `json:"published_at"`
	Draft     bool      `json:"draft"`
	Pre       bool      `json:"prerelease"`
	Assets    []ghAsset `json:"assets"`
}

// updateInfo is what the page gets.
type updateInfo struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Newer      bool   `json:"newer"`
	Name       string `json:"name,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Page       string `json:"page,omitempty"`
	Published  string `json:"published,omitempty"`
	Size       int64  `json:"size,omitempty"`
	CanInstall bool   `json:"canInstall"` // the release has the exe, so it can be installed from the app
	Error      string `json:"error,omitempty"`
	Checked    int64  `json:"checked"` // unix ms
}

// parseVersion reads "v2.6.1" or "2.6" as major, minor, patch.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(s)), "v")
	if s == "" {
		return v, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// newerVersion: is latest a later version than current?
func newerVersion(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// releaseInfo turns a release into what the page shows, and finds the exe
// (only a file of this repository's releases is ever downloaded).
func releaseInfo(r ghRelease, current string) (updateInfo, *ghAsset) {
	v := updateInfo{Current: current, Latest: strings.TrimPrefix(r.Tag, "v"), Name: r.Name, Notes: r.Body, Page: r.HTML, Published: r.Published}
	if r.Draft || r.Pre {
		return updateInfo{Current: current}, nil
	}
	v.Newer = newerVersion(r.Tag, current)
	prefix := "https://github.com/" + updateRepo + "/releases/download/"
	for i := range r.Assets {
		a := &r.Assets[i]
		if strings.EqualFold(a.Name, updateAsset) && strings.HasPrefix(a.URL, prefix) && a.Size > 0 && a.Size < updateMax {
			v.CanInstall, v.Size = true, a.Size
			return v, a
		}
	}
	return v, nil
}

func fetchLatest(ctx context.Context, current string) (ghRelease, error) {
	var r ghRelease
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateAPI, nil)
	if err != nil {
		return r, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+current)
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return r, errNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return r, errRateLimited
	case resp.StatusCode != http.StatusOK:
		return r, fmt.Errorf("GitHub: %s", resp.Status)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r)
	return r, err
}

var (
	errNoRelease   = errors.New("upd.err.none")
	errRateLimited = errors.New("upd.err.limit")
	errBadFile     = errors.New("upd.err.badFile")
	errBadHash     = errors.New("upd.err.hash")
	errBadHost     = errors.New("upd.err.host")
)

var upd struct {
	mu    sync.Mutex
	info  updateInfo
	asset *ghAsset
	busy  bool
}

// checkUpdate asks GitHub once; the answer is kept for installUpdate.
func checkUpdate() updateInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, err := fetchLatest(ctx, appVersion)
	var v updateInfo
	var a *ghAsset
	if err != nil {
		v = updateInfo{Current: appVersion, Error: updateErrText(err)}
	} else {
		v, a = releaseInfo(r, appVersion)
	}
	v.Checked = time.Now().UnixMilli()
	upd.mu.Lock()
	upd.info, upd.asset = v, a
	upd.mu.Unlock()
	return v
}

// downloadVerified saves the release's exe to dst and checks it: the size
// GitHub gave, the SHA-256 GitHub gave (when it gave one), and that it is a
// Windows program. Only github.com and its download hosts are accepted.
func downloadVerified(ctx context.Context, a ghAsset, dst string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+appVersion)
	client := *updateHTTP
	client.Timeout = 10 * time.Minute
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !downloadHostOK(r.URL) {
			return errBadHost
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errBadHost) {
			return errBadHost
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub: %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(dst)
		}
	}()
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	head := make([]byte, 0, 2)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if len(head) < 2 {
				head = append(head, buf[:min(n, 2-len(head))]...)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			h.Write(buf[:n])
			done += int64(n)
			if done > updateMax || (a.Size > 0 && done > a.Size) {
				return errBadFile
			}
			if progress != nil {
				progress(done, a.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if (a.Size > 0 && done != a.Size) || string(head) != "MZ" {
		return errBadFile
	}
	if want, found := strings.CutPrefix(strings.ToLower(a.Digest), "sha256:"); found && want != hex.EncodeToString(h.Sum(nil)) {
		return errBadHash
	}
	if err := f.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func downloadHostOK(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "github.com" || h == "objects.githubusercontent.com" || h == "release-assets.githubusercontent.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

// swapExe puts the new program where the running one is. Windows lets a
// running exe be renamed (not deleted): it becomes <exe>.old and is removed
// when the new version starts. On any error the old program stays in place.
func swapExe(exe, fresh string) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("%w (and putting the old one back: %w)", err, rerr)
		}
		return err
	}
	return nil
}

// cleanupOldExe removes what an update left: <exe>.old (once the old
// process has ended) and an unfinished download.
func cleanupOldExe(exe string) {
	_ = os.Remove(updateTemp(exe))
	old := exe + ".old"
	for i := 0; i < 30; i++ {
		if err := os.Remove(old); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Second)
	}
}

// updateTemp is where the download goes: next to the exe, so the swap is a
// rename on the same drive.
func updateTemp(exe string) string { return exe + ".new" }

func updateErrText(err error) string {
	if err == nil {
		return ""
	}
	if k := err.Error(); strings.HasPrefix(k, "upd.err.") {
		return T(k)
	}
	return T("upd.err.net", err.Error())
}
