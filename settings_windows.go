//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Settings live in %LOCALAPPDATA%\xKi4oCleaner\settings.json: the restore
// point switch, which categories the user ticked, and the DISM mode.

var settingsMu sync.Mutex

func settingsPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "xKi4oCleaner", "settings.json")
}

func getSettings() map[string]any {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return readSettingsLocked()
}

// Keys the backend owns: the page has a copy of the settings and saves it
// whole, so these are always taken from the file, never from the page.
var backendKeys = []string{"dnsBackup", "autoClean", "autoCleanDay", "autoCleanLast", "gpuWUPrev"}

func readSettingsLocked() map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// updateSettings changes the saved settings in one step (read, change,
// write), so a save of the page in between cannot undo it.
func updateSettings(change func(m map[string]any)) bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	m := readSettingsLocked()
	change(m)
	return writeSettingsLocked(m)
}

// savePageSettings saves what the page sends, keeping the backend's keys.
func savePageSettings(m map[string]any) bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	cur := readSettingsLocked()
	out := make(map[string]any, len(m)+len(backendKeys))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range backendKeys {
		if v, ok := cur[k]; ok {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	return writeSettingsLocked(out)
}

func writeSettingsLocked(m map[string]any) bool {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false
	}
	p := settingsPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) != nil {
		return false
	}
	return os.Rename(tmp, p) == nil
}
