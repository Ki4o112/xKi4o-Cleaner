//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows/registry"
)

// what the last ClamAV scan found, and the last check of the risky places
var avClam struct {
	mu      sync.Mutex
	threats []avThreat
	sus     []avSus
	defOn   atomic.Bool // Defender was the working antivirus at the last read
}

type avSus struct {
	ID   string   `json:"id"`
	Kind string   `json:"kind"` // startup, hosts, proxy, pac
	Name string   `json:"name"`
	Path string   `json:"path,omitempty"`
	Why  []string `json:"why,omitempty"` // su.w.* reasons for a startup entry
	Fix  bool     `json:"fix"`
}

func clamExe(name string) string {
	for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)")} {
		if p := filepath.Join(pf, "ClamAV", name); pf != "" && fileExistsAny(p) {
			return p
		}
	}
	return ""
}

func clamDBDir() string { return dataDirPath("clamav") }

// clamDBTime: when the signature database was last updated (unix ms, 0 = none).
func clamDBTime() int64 {
	var t time.Time
	for _, pat := range []string{"*.cvd", "*.cld"} {
		m, _ := filepath.Glob(filepath.Join(clamDBDir(), pat))
		for _, f := range m {
			if fi, err := os.Stat(f); err == nil && fi.ModTime().After(t) {
				t = fi.ModTime()
			}
		}
	}
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// clamInstall gets ClamAV through winget (the page gets the usual "winget" events).
func clamInstall() bool {
	return wingetUpgrade([]string{clamWingetID}, wgOpts{Install: true})
}

// clamUpdate refreshes the database when it is older than 6 hours.
func clamUpdate(ctx context.Context) error {
	if at := clamDBTime(); at > 0 && time.Since(time.UnixMilli(at)) < 6*time.Hour {
		return nil
	}
	exe := clamExe("freshclam.exe")
	if exe == "" {
		return os.ErrNotExist
	}
	db := clamDBDir()
	if err := os.MkdirAll(db, 0o755); err != nil {
		return err
	}
	conf := filepath.Join(db, "freshclam.conf")
	if err := os.WriteFile(conf, []byte(freshclamConf), 0o644); err != nil {
		return err
	}
	emit("av", map[string]any{"state": "update"})
	code, err := runStreamCtx(ctx, exe, []string{"--config-file=" + conf, "--datadir=" + db, "--stdout"}, func(string, bool) {})
	if err == nil && code != 0 {
		err = errors.New(T("qf.code", code))
	}
	if err != nil && clamDBTime() > 0 {
		return nil // an older database still works (the mirror limits how often one may ask)
	}
	return err
}

func clamTargets(kind, path string) []string {
	switch kind {
	case "folder":
		return []string{filepath.Clean(path)}
	case "full":
		var r []string
		for _, v := range diskVolumes() {
			if v.Kind == "fixed" {
				r = append(r, v.Root)
			}
		}
		return r
	}
	var r []string
	for _, d := range clamQuickDirs(os.Getenv("USERPROFILE"), os.Getenv("TEMP"), os.Getenv("ProgramData"), os.Getenv("SystemRoot")) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			r = append(r, d)
		}
	}
	return r
}

// clamScan runs in the background like avScan does with Defender; the page
// gets "av" events: update, run (seconds, files), then end.
func clamScan(ctx context.Context, cancel context.CancelFunc, kind, path string) {
	defer recoverLog("antivirus")
	defer app.working.Store(false)
	op := beginOp("antivirus", "", T("log.avScan", T("av.k."+kind)+" · ClamAV"), map[string]any{"kind": kind, "path": path})
	t0 := time.Now()
	var files atomic.Int64
	var mu sync.Mutex
	var found []avThreat
	finish := func(ev map[string]any) {
		avRun.mu.Lock()
		avRun.cancel = nil
		avRun.mu.Unlock()
		cancel()
		mu.Lock()
		ev["state"], ev["threats"], ev["secs"], ev["files"] = "end", found, int(time.Since(t0).Seconds()), files.Load()
		mu.Unlock()
		if ctx.Err() != nil {
			ev["stopped"] = true
		}
		if e, ok := ev["error"].(string); ok {
			op.fail("", e, nil)
		}
		op.end(T("log.avDone", len(found)), nil)
		emit("av", ev)
	}
	if err := clamUpdate(ctx); err != nil {
		if ctx.Err() != nil {
			finish(map[string]any{})
		} else {
			finish(map[string]any{"error": T("av.err.db", errText(err, 0))})
		}
		return
	}
	tick := time.NewTicker(time.Second)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				mu.Lock()
				n := len(found)
				mu.Unlock()
				emit("av", map[string]any{"state": "run", "secs": int(time.Since(t0).Seconds()), "files": files.Load(), "found": n})
			}
		}
	}()
	args := append([]string{"--database=" + clamDBDir(), "--recursive", "--stdout", "--max-filesize=100M", "--max-scansize=300M"}, clamTargets(kind, path)...)
	code, err := runStreamCtx(ctx, clamExe("clamscan.exe"), args, func(line string, _ bool) {
		p, sig, bad, ok := parseClamLine(line)
		switch {
		case ok:
			files.Add(1)
		case bad:
			files.Add(1)
			mu.Lock()
			found = append(found, avThreat{ID: "clam:" + p, Name: sig, Sev: 4, Status: "active", Active: true, Files: []string{p}, At: time.Now().UnixMilli()})
			mu.Unlock()
		}
	})
	tick.Stop()
	close(done)
	mu.Lock()
	avClam.mu.Lock()
	avClam.threats = append([]avThreat(nil), found...)
	avClam.mu.Unlock()
	mu.Unlock()
	ev := map[string]any{}
	// 1: threats were found; 2: some files could not be read, which a full scan always meets
	if ctx.Err() == nil && err != nil && code != 1 && !(code == 2 && files.Load() > 0) {
		ev["error"] = T("av.err.scan", errText(err, code))
	}
	finish(ev)
}

// clamRemove moves what the last scan found into the quarantine folder.
func clamRemove() {
	op := beginOp("antivirus", "", T("log.avRemove"), nil)
	q := dataDirPath("quarantine")
	_ = os.MkdirAll(q, 0o755)
	avClam.mu.Lock()
	defer avClam.mu.Unlock()
	failed := 0
	for i := range avClam.threats {
		th := &avClam.threats[i]
		if !th.Active || len(th.Files) == 0 {
			continue
		}
		dst := filepath.Join(q, quarName(th.Files[0], time.Now().UnixNano()))
		if err := moveFile(th.Files[0], dst); err != nil && fileExistsAny(th.Files[0]) {
			failed++
			op.fail(th.Files[0], err.Error(), nil)
			continue
		}
		th.Active, th.Status = false, "done"
	}
	op.end(T("av.quarDone", failed), nil)
}

func moveFile(src, dst string) error {
	if os.Rename(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

const inetKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// avCheck looks at the places malware hides in: startup entries with
// warnings, the hosts file and the proxy settings.
func avCheck() []avSus {
	res := []avSus{}
	for _, e := range listStartup().Items {
		if e.Enabled && len(e.Warnings) > 0 {
			p := e.Exe
			if p == "" {
				p = e.Command
			}
			res = append(res, avSus{ID: "su|" + e.Key, Kind: "startup", Name: e.Name, Path: p, Why: e.Warnings, Fix: true})
		}
	}
	if b, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), `System32\drivers\etc\hosts`)); err == nil {
		if r := hostsRedirects(string(b)); len(r) > 0 {
			if len(r) > 5 {
				r = r[:5]
			}
			res = append(res, avSus{ID: "hosts", Kind: "hosts", Name: strings.Join(r, " · ")})
		}
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.QUERY_VALUE); err == nil {
		if on, _, _ := k.GetIntegerValue("ProxyEnable"); on == 1 {
			srv, _, _ := k.GetStringValue("ProxyServer")
			res = append(res, avSus{ID: "proxy", Kind: "proxy", Name: srv, Fix: true})
		}
		if pac, _, _ := k.GetStringValue("AutoConfigURL"); pac != "" {
			res = append(res, avSus{ID: "pac", Kind: "pac", Name: pac, Fix: true})
		}
		k.Close()
	}
	avClam.mu.Lock()
	avClam.sus = res
	avClam.mu.Unlock()
	return res
}

// avFix acts on one item of the last check: a startup entry is turned off
// (it can be turned on again on the Startup page), the proxy is switched off.
func avFix(id string) bool {
	avClam.mu.Lock()
	var it *avSus
	for i := range avClam.sus {
		if avClam.sus[i].ID == id && avClam.sus[i].Fix {
			x := avClam.sus[i]
			it = &x
		}
	}
	avClam.mu.Unlock()
	if it == nil {
		return false
	}
	op := beginOp("antivirus", it.Name, T("log.avFix", it.Name), map[string]any{"id": id})
	ok := false
	switch it.Kind {
	case "startup":
		ok = setStartup(strings.TrimPrefix(id, "su|"), false).OK
	case "proxy", "pac":
		if k, err := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.SET_VALUE); err == nil {
			if it.Kind == "proxy" {
				ok = k.SetDWordValue("ProxyEnable", 0) == nil
			} else {
				ok = k.DeleteValue("AutoConfigURL") == nil
			}
			k.Close()
		}
	}
	if !ok {
		op.fail("", T("av.err.fix"), nil)
	}
	op.end("", nil)
	return ok
}
