//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

var delayMu sync.Mutex

func delaysPath() string { return dataDirPath("startup-delays.json") }

func delaysRead() []startDelay {
	var l []startDelay
	if b, err := os.ReadFile(delaysPath()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

func delaysWrite(l []startDelay) error {
	b, _ := json.Marshal(l)
	_ = os.MkdirAll(filepath.Dir(delaysPath()), 0o755)
	return os.WriteFile(delaysPath(), b, 0o644)
}

// startupDelay delays a startup program by secs (0: starts with Windows again).
func startupDelay(key string, secs int) startupResult {
	delayMu.Lock()
	defer delayMu.Unlock()
	var e *startupEntry
	for _, x := range listStartup().Items { // (with the file and the kind of each)
		if x.Key == key {
			x := x
			e = &x
		}
	}
	list := delaysRead()
	var old *startDelay
	for i := range list {
		if list[i].Key == key {
			old = &list[i]
		}
	}
	name := key
	if e != nil {
		name = e.Name
	}
	op := beginOp("startup", name, T("log.suDelay", name, secs), map[string]any{"key": key, "secs": secs})
	err := func() error {
		if !app.admin {
			return errors.New(T("blocked.admin"))
		}
		if secs == 0 {
			if old == nil {
				return nil
			}
			_ = runTool(sys32("schtasks.exe"), "/Delete", "/TN", old.Task, "/F")
			if e != nil {
				if r := setStartup(key, true); !r.OK {
					return errors.New(r.Error)
				}
			}
			return delaysWrite(setDelay(list, startDelay{Key: key}))
		}
		if !delaySecsOK(secs) {
			return errors.New(T("su.err.delay"))
		}
		if e == nil || (e.Source != "registry" && e.Source != "folder") || e.Kind != "app" || e.Exe == "" || !e.Exists {
			return errors.New(T("su.err.noDelay"))
		}
		sid := ""
		if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
			sid = tu.User.Sid.String()
		}
		task := delayTaskName(e.Name)
		if old != nil {
			task = old.Task
		}
		tmp := dataDirPath("delay-task.xml")
		_ = os.MkdirAll(filepath.Dir(tmp), 0o755)
		if err := utf16File(tmp, delayTaskXML(e.Exe, e.Args, filepath.Dir(e.Exe), sid, secs, T("su.delayDesc", e.Name))); err != nil {
			return err
		}
		defer os.Remove(tmp)
		if err := runTool(sys32("schtasks.exe"), "/Create", "/TN", task, "/XML", tmp, "/F"); err != nil {
			return err
		}
		if e.Enabled {
			if r := setStartup(key, false); !r.OK {
				_ = runTool(sys32("schtasks.exe"), "/Delete", "/TN", task, "/F")
				return errors.New(r.Error)
			}
		}
		return delaysWrite(setDelay(list, startDelay{Key: key, Name: e.Name, Task: task, Secs: secs}))
	}()
	if err != nil {
		op.fail("", err.Error(), nil)
		op.end("", nil)
		return startupResult{Error: err.Error()}
	}
	op.end("", nil)
	return startupResult{OK: true}
}

// delayMark: on the Startup page a delayed program shows as on, with its
// delay; the app's own tasks for it are not listed again.
func delayMark(items []startupEntry) []startupEntry {
	ds := delaysRead()
	if len(ds) == 0 {
		return items
	}
	by := map[string]startDelay{}
	tasks := map[string]bool{}
	for _, d := range ds {
		by[d.Key] = d
		tasks[strings.ToLower(`task|\`+d.Task)] = true
	}
	out := items[:0]
	for _, e := range items {
		if tasks[strings.ToLower(e.Key)] {
			continue
		}
		if d, ok := by[e.Key]; ok {
			e.Delayed = d.Secs
			e.Enabled = true
		}
		out = append(out, e)
	}
	return out
}
