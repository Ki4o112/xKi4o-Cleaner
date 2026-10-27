//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type bootState struct {
	On    bool   `json:"on"`
	Moved bool   `json:"moved"` // the task starts a copy of the app that is somewhere else
	Reset bool   `json:"reset"` // the 4.15.1 task was removed: the user turns it on again
	Error string `json:"error,omitempty"`
}

// bootLnkName: the Startup shortcut 4.15.1 made (it ran the task); removed.
const bootLnkName = "xKi4o Cleaner.lnk"

func bootTaskFile() string { return filepath.Join(P.windir, "System32", "Tasks", bootTaskName) }

func bootLnk() string {
	return filepath.Join(P.roaming, `Microsoft\Windows\Start Menu\Programs\Startup`, bootLnkName)
}

func bootGet() bootState {
	x := readTaskFile(bootTaskFile())
	st := bootState{On: x != ""}
	if st.On {
		if exe, err := os.Executable(); err == nil {
			st.Moved = !strings.EqualFold(taskCommand(x), exe)
		}
		if !strings.Contains(x, "<LogonTrigger>") && app.admin {
			// the 4.15.1 task (run by a shortcut, which Defender flagged): removed, and only
			// made again when the user turns the start on, never by itself
			_ = runTool(sys32("schtasks.exe"), "/Delete", "/TN", bootTaskName, "/F")
			st.On, st.Moved, st.Reset = false, false, true
		}
	}
	if fileExistsAny(bootLnk()) && app.admin {
		_ = os.Remove(bootLnk()) // the 4.15.1 shortcut
		bootApprovedClear()
	}
	app.trayClose.Store(st.On)
	return st
}

// bootSet makes (or removes) the sign-in task.
func bootSet(on bool) bootState {
	op := beginOp("schedule", "", T("log.boot", T(map[bool]string{true: "boot.on", false: "boot.off"}[on])), nil)
	err := func() error {
		if !app.admin {
			return errors.New(T("blocked.admin"))
		}
		if !on {
			_ = os.Remove(bootLnk())
			bootApprovedClear()
			if _, err := os.Stat(bootTaskFile()); errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return runTool(sys32("schtasks.exe"), "/Delete", "/TN", bootTaskName, "/F")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		sid := ""
		if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
			sid = tu.User.Sid.String()
		}
		tmp := dataDirPath("boot-task.xml")
		_ = os.MkdirAll(filepath.Dir(tmp), 0o755)
		if err := utf16File(tmp, bootTaskXML(exe, sid, T("boot.taskDesc"))); err != nil {
			return err
		}
		defer os.Remove(tmp)
		return runTool(sys32("schtasks.exe"), "/Create", "/TN", bootTaskName, "/XML", tmp, "/F")
	}()
	st := bootGet()
	if err != nil {
		op.fail("", err.Error(), nil)
		st.Error = err.Error()
	}
	op.end("", nil)
	return st
}

// bootApprovedClear forgets Task Manager's on/off for the old shortcut.
func bootApprovedClear() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey+`\StartupFolder`, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(bootLnkName)
		k.Close()
	}
}
