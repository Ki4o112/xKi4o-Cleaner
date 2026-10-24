//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// shortcut: only the registry Run keys and the Startup folders are watched
// (scheduled tasks need PowerShell); upgrade when tasks can be read cheaply.

func startupKnownPath() string { return dataDirPath("startup-known.json") }

// startupWatchLoop looks every 5 minutes while the app runs.
func startupWatchLoop() {
	time.Sleep(20 * time.Second) // after the opening
	for {
		startupWatchStep()
		time.Sleep(5 * time.Minute)
	}
}

func startupWatchStep() {
	defer recoverLog("startup watch")
	items := listStartupQuick()
	var keys []string
	by := map[string]startupEntry{}
	for _, e := range items {
		keys = append(keys, e.Key)
		by[e.Key] = e
	}
	known := map[string]bool{}
	if b, err := os.ReadFile(startupKnownPath()); err == nil {
		var ks []string
		if json.Unmarshal(b, &ks) == nil {
			for _, k := range ks {
				known[k] = true
			}
		}
	}
	fresh := newStartupKeys(known, keys)
	// what is there now becomes known (removed entries drop out of the list)
	b, _ := json.Marshal(append(keys, "\x00seen")) // marks the first look done, even with no entries
	_ = os.MkdirAll(filepath.Dir(startupKnownPath()), 0o755)
	_ = os.WriteFile(startupKnownPath(), b, 0o644)
	if len(fresh) == 0 {
		return
	}
	var names []string
	list := make([]map[string]string, 0, len(fresh))
	for _, k := range fresh {
		e := by[k]
		names = append(names, e.Name)
		list = append(list, map[string]string{"key": k, "name": e.Name, "command": e.Command})
	}
	logEvent("startup", "warn", "", T("log.suNew", strings.Join(names, ", ")), map[string]any{"items": list})
	emit("startup-new", map[string]any{"items": list})
	if app.w != nil {
		app.w.Dispatch(func() { trayNotify(T("su.newT"), strings.Join(names, ", ")) })
	}
}
