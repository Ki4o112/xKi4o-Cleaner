//go:build windows

package main

// The "Merge connections" page drives net-merge (https://github.com/rohit267/net-merge,
// MIT), a separate tool that uses all your network adapters at once for more
// speed. This app does NOT bundle net-merge or its network driver (Wintun):
// bundling a TUN driver would make the Defender false positive much worse. It
// detects an installed net-merge, reads its own live status file, and turns it
// on and off through net-merge's documented commands. If it is not installed,
// the page opens net-merge's own page so the user installs it from the source.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

const nmReleases = "https://github.com/rohit267/net-merge/releases/latest"

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

// nmExe finds an installed net-merge.exe: the installer's folder, or PATH.
func nmExe() string {
	cands := []string{}
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
			// start the tray; it self-elevates with Windows' UAC prompt
			shellOpen(exe) // "net-merge tray" is the default when launched with no console
			for i := 0; i < 40; i++ {
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
