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
	m := map[string]any{}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveSettings(m map[string]any) bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
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
