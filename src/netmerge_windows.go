//go:build windows

package main

// The "Merge connections" page drives net-merge (https://github.com/rohit267/net-merge,
// MIT), a separate tool that uses all your network adapters at once for more
// speed. This app does NOT bundle net-merge or its network driver (Wintun):
// bundling a TUN driver would make the Defender false positive much worse. On
// the user's click it downloads net-merge's own portable zip from the latest
// GitHub release (HTTPS, github.com only) and unpacks it into the app's folder,
// so the app finds it without the user hunting for a file. It reads net-merge's
// own live status file and turns it on and off through net-merge's commands.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const nmReleases = "https://github.com/rohit267/net-merge/releases/latest"

// nmZipURL is the latest release's portable zip (a stable alias net-merge
// publishes). It holds net-merge.exe, net-merge-tray.exe and wintun.dll.
const nmZipURL = "https://github.com/rohit267/net-merge/releases/latest/download/net-merge-portable.zip"

const nmZipMax = 80 << 20 // a sane cap for the portable zip

// nmInstallDir is where the app unpacks net-merge (its own folder, so it needs
// no installer and no admin to put the files there).
func nmInstallDir() string {
	base := P.local
	if base == "" {
		base = os.Getenv("LOCALAPPDATA")
	}
	return filepath.Join(base, "xKi4oCleaner", "net-merge")
}

type nmAdapter struct {
	Name    string  `json:"name"`
	IP      string  `json:"ip"`
	State   string  `json:"state"`
	Active  int64   `json:"active"`
	Total   int64   `json:"total"`
	RxMbps  float64 `json:"rx"` // through net-merge
	TxMbps  float64 `json:"tx"`
	Present bool    `json:"present"`
}

type nmStatus struct {
	Installed bool        `json:"installed"`
	Path      string      `json:"path,omitempty"`
	Running   bool        `json:"running"` // the tray/run process is up
	On        bool        `json:"on"`      // merging is active (fresh status file)
	UpSecs    int64       `json:"upSecs,omitempty"`
	V6        string      `json:"v6,omitempty"`
	Adapters  []nmAdapter `json:"adapters"`
	RxMbps    float64     `json:"rx"`
	TxMbps    float64     `json:"tx"`
	Busy      bool        `json:"busy"`
	Error     string      `json:"error,omitempty"`
}

// nmExe finds net-merge.exe: the app's own folder first (where it unpacks it),
// then the installer's folder, then PATH.
func nmExe() string {
	cands := []string{filepath.Join(nmInstallDir(), "net-merge.exe")}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		cands = append(cands, filepath.Join(pf, "net-merge", "net-merge.exe"))
	}
	if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
		cands = append(cands, filepath.Join(pf, "net-merge", "net-merge.exe"))
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("net-merge"); err == nil {
		return p
	}
	return ""
}

// nmTrayExe is the GUI tray build next to net-merge.exe (no console window).
func nmTrayExe(exe string) string {
	if exe == "" {
		return ""
	}
	t := filepath.Join(filepath.Dir(exe), "net-merge-tray.exe")
	if fi, err := os.Stat(t); err == nil && !fi.IsDir() {
		return t
	}
	return ""
}

// nmInstall downloads net-merge's portable zip from its latest GitHub release
// and unpacks the program files into the app's own folder. Only github.com is
// accepted and only the known files are written (no path escapes the folder).
func nmInstall() nmStatus {
	if !nmBusy.CompareAndSwap(false, true) {
		return nmState()
	}
	defer nmBusy.Store(false)
	op := beginOp("netmerge", "", T("log.nmInstall"), nil)
	dir := nmInstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return nmStatus{Error: err.Error()}
	}
	tmp := filepath.Join(dir, "download.zip")
	emit("nm-progress", map[string]any{"phase": "download", "done": 0, "total": 0})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := nmDownload(ctx, nmZipURL, tmp, func(done, total int64) {
		emit("nm-progress", map[string]any{"phase": "download", "done": done, "total": total})
	}); err != nil {
		os.Remove(tmp)
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return nmStatus{Error: T("nm.err.download", nmErrText(err))}
	}
	emit("nm-progress", map[string]any{"phase": "extract"})
	n, err := nmUnzip(tmp, dir)
	os.Remove(tmp)
	if err != nil || nmExe() == "" {
		if err == nil {
			err = errors.New(T("nm.err.noExe"))
		}
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return nmStatus{Error: T("nm.err.unzip", err.Error())}
	}
	op.end(T("log.nmInstalled", n), nil)
	emit("nm-progress", map[string]any{"phase": "done"})
	return nmState()
}

// nmSafeNames: only these files are taken out of net-merge's zip.
var nmSafeNames = map[string]bool{
	"net-merge.exe": true, "net-merge-tray.exe": true, "wintun.dll": true,
	"wintun-license.txt": true, "license": true, "third-party-notices.md": true, "readme.md": true,
}

// nmUnzip writes the known net-merge files from the zip into dir (flat, by base
// name only — a zip entry can never write outside dir).
func nmUnzip(zipPath, dir string) (int, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	var total int64
	n := 0
	for _, f := range zr.File {
		base := filepath.Base(filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() || !nmSafeNames[strings.ToLower(base)] {
			continue
		}
		if total += int64(f.UncompressedSize64); total > nmZipMax {
			return n, errors.New("zip too large")
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		out, err := os.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return n, err
		}
		_, cErr := io.Copy(out, io.LimitReader(rc, nmZipMax))
		rc.Close()
		out.Close()
		if cErr != nil {
			return n, cErr
		}
		n++
	}
	return n, nil
}

// nmDownload saves url to dst, HTTPS and github.com only, with progress. It
// reuses the app's update host check so no other host is ever fetched.
func nmDownload(ctx context.Context, url, dst string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "xKi4o-Cleaner/"+appVersion)
	if !downloadHostOK(req.URL) {
		return errBadHost
	}
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
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errNoRelease
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(dst)
		}
	}()
	// a zip starts with "PK"
	head := make([]byte, 0, 2)
	buf := make([]byte, 256<<10)
	var done int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if len(head) < 2 {
				head = append(head, buf[:min(n, 2-len(head))]...)
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if done > nmZipMax {
				return errors.New("file too large")
			}
			if progress != nil {
				progress(done, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if len(head) < 2 || head[0] != 'P' || head[1] != 'K' {
		return errBadFile
	}
	ok = true
	return nil
}

func nmErrText(err error) string {
	switch {
	case errors.Is(err, errBadHost):
		return T("upd.err.host")
	case errors.Is(err, errBadFile):
		return T("upd.err.badFile")
	}
	return err.Error()
}

// nmStateFile is net-merge's own status file (it writes it every second while
// merging; state.DefaultDir on Windows is %LOCALAPPDATA%\net-merge).
func nmStateFile() string {
	base := P.local
	if base == "" {
		base = os.Getenv("LOCALAPPDATA")
	}
	return filepath.Join(base, "net-merge", "status.json")
}

// what net-merge writes into status.json (state.Status / engine.SlotStats).
type nmFile struct {
	PID     int
	Mode    string
	V6      string
	Since   time.Time
	Updated time.Time
	NICs    []struct {
		Name            string
		IP, IP6         string
		State, State6   string
		Present         bool
		Active, Total   int64
		RateIn, RateOut float64 // bytes/s through net-merge
	}
}

func nmState() nmStatus {
	s := nmStatus{Adapters: []nmAdapter{}, Busy: nmBusy.Load()}
	s.Path = nmExe()
	s.Installed = s.Path != ""
	s.Running = len(procPIDs(map[string]bool{"net-merge.exe": true, "net-merge-tray.exe": true})) > 0
	if b, err := os.ReadFile(nmStateFile()); err == nil {
		var f nmFile
		if json.Unmarshal(b, &f) == nil && time.Since(f.Updated) <= 3*time.Second {
			s.On, s.Running = true, true
			s.V6 = f.V6
			s.UpSecs = int64(time.Since(f.Since).Seconds())
			for _, n := range f.NICs {
				a := nmAdapter{Name: n.Name, IP: n.IP, State: n.State, Active: n.Active, Total: n.Total,
					RxMbps: n.RateIn * 8 / 1e6, TxMbps: n.RateOut * 8 / 1e6, Present: n.Present}
				s.RxMbps += a.RxMbps
				s.TxMbps += a.TxMbps
				s.Adapters = append(s.Adapters, a)
			}
		}
	}
	return s
}

var nmBusy atomic.Bool

// nmSet turns merging on or off. On: if the tray is not up, it is started
// first (it asks for admin once, through Windows' own prompt). net-merge's
// "on"/"off" only write a small marker file and need no admin.
func nmSet(on bool) nmStatus {
	exe := nmExe()
	if exe == "" {
		return nmStatus{Error: T("nm.err.notInstalled")}
	}
	if !nmBusy.CompareAndSwap(false, true) {
		return nmState()
	}
	defer nmBusy.Store(false)
	op := beginOp("netmerge", "", T("log.nmSet", map[bool]string{true: "on", false: "off"}[on]), nil)
	if on {
		if len(procPIDs(map[string]bool{"net-merge.exe": true, "net-merge-tray.exe": true})) == 0 {
			// start net-merge's tray; it self-elevates with Windows' own UAC
			// prompt and turns merging on by itself. Prefer the GUI build so
			// no console window flashes.
			start := exe
			if t := nmTrayExe(exe); t != "" {
				start = t
			}
			cmd := exec.CommandContext(context.Background(), start, "tray")
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			if err := cmd.Start(); err != nil {
				op.fail("", err.Error(), nil)
			} else {
				_ = cmd.Process.Release()
			}
			for i := 0; i < 50; i++ {
				time.Sleep(150 * time.Millisecond)
				if len(procPIDs(map[string]bool{"net-merge.exe": true, "net-merge-tray.exe": true})) > 0 {
					break
				}
			}
		}
		_, _, err := runQuiet(20*time.Second, exe, "on")
		if err != nil {
			op.fail("", err.Error(), nil)
		}
	} else {
		_, _, err := runQuiet(20*time.Second, exe, "off")
		if err != nil {
			op.fail("", err.Error(), nil)
		}
	}
	op.end("", map[string]any{"on": on})
	// give net-merge a moment to write/clear its status file
	time.Sleep(400 * time.Millisecond)
	return nmState()
}

func nmOpenSite() bool {
	shellOpen(nmReleases)
	return true
}
