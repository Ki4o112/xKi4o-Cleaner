//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type bootState struct {
	On    bool   `json:"on"`
	Moved bool   `json:"moved"` // the task starts a copy of the app that is somewhere else
	Error string `json:"error,omitempty"`
}

func bootTaskFile() string { return filepath.Join(P.windir, "System32", "Tasks", bootTaskName) }

func bootGet() bootState {
	x := readTaskFile(bootTaskFile())
	st := bootState{On: x != ""}
	if exe, err := os.Executable(); err == nil && st.On {
		st.Moved = !strings.EqualFold(taskCommand(x), exe)
	}
	app.trayClose.Store(st.On)
	return st
}

// bootSet makes (or removes) the task that starts the app at sign-in.
func bootSet(on bool) bootState {
	op := beginOp("schedule", "", T("log.boot", T(map[bool]string{true: "boot.on", false: "boot.off"}[on])), nil)
	err := func() error {
		if !app.admin {
			return errors.New(T("blocked.admin"))
		}
		if !on {
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
