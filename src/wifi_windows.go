//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type wifiList struct {
	Nets  []wifiNet `json:"nets"`
	Error string    `json:"error,omitempty"`
}

// wifiNets exports the saved profiles with their keys into a temporary
// folder, reads them and deletes the folder at once. Needs admin rights for
// the keys; the passwords are never logged.
func wifiNets() wifiList {
	if !app.admin {
		return wifiList{Error: T("blocked.admin")}
	}
	dir, err := os.MkdirTemp("", "xk-wifi-")
	if err != nil {
		return wifiList{Error: err.Error()}
	}
	defer os.RemoveAll(dir)
	out, code, err := runQuiet(30*time.Second, sys32("netsh.exe"), "wlan", "export", "profile", "key=clear", "folder="+dir)
	files, _ := filepath.Glob(filepath.Join(dir, "*.xml"))
	if len(files) == 0 {
		if err != nil || code != 0 {
			logEvent("wifi", "info", "", T("wifi.none"), map[string]any{"netsh": strings.TrimSpace(out), "exit": code})
		}
		return wifiList{Nets: []wifiNet{}}
	}
	res := wifiList{Nets: []wifiNet{}}
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if n, ok := parseWlanProfile(b); ok && !seen[n.Name] { // the same network on two adapters
			seen[n.Name] = true
			res.Nets = append(res.Nets, n)
		}
	}
	sortWifi(res.Nets)
	return res
}
