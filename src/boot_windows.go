//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type bootState struct {
	On    bool   `json:"on"`
	Moved bool   `json:"moved"` // the task starts a copy of the app that is somewhere else
	Off   bool   `json:"off"`   // turned off in Task Manager's Startup apps
	Error string `json:"error,omitempty"`
}

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
		if !fileExistsAny(bootLnk()) || strings.Contains(x, "<LogonTrigger>") {
			st.Moved = true // made by 4.15 (a sign-in trigger, no shortcut): made again
		}
		if en, _, _, ok := approvedState(registry.CURRENT_USER, "StartupFolder", bootLnkName); ok && !en {
			st.On, st.Off = false, true
		}
	}
	app.trayClose.Store(st.On)
	return st
}

// bootLnkScript writes the Startup shortcut: schtasks.exe runs the task, in
// a minimized window, with the app's icon.
const bootLnkScript = `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:XK_LNK)
$s.TargetPath = $env:XK_SCHTASKS; $s.Arguments = $env:XK_ARGS; $s.WindowStyle = 7
$s.IconLocation = $env:XK_EXE + ',0'; $s.Description = $env:XK_DESC; $s.WorkingDirectory = Split-Path $env:XK_EXE
$s.Save()`

// bootSet makes (or removes) the task and the Startup shortcut.
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
		if err := runTool(sys32("schtasks.exe"), "/Create", "/TN", bootTaskName, "/XML", tmp, "/F"); err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(bootLnk()), 0o755)
		for k, v := range map[string]string{"XK_LNK": bootLnk(), "XK_SCHTASKS": sys32("schtasks.exe"), "XK_ARGS": bootLnkArgs(), "XK_EXE": exe, "XK_DESC": T("boot.taskDesc")} {
			_ = os.Setenv(k, v)
		}
		if _, code, err := runPS(30*time.Second, bootLnkScript); err != nil || code != 0 || !fileExistsAny(bootLnk()) {
			return errors.New(T("boot.err.lnk"))
		}
		bootApprovedClear() // turned off in Task Manager before: on again
		return nil
	}()
	st := bootGet()
	if err != nil {
		op.fail("", err.Error(), nil)
		st.Error = err.Error()
	}
	op.end("", nil)
	return st
}

// bootApprovedClear forgets Task Manager's on/off for the shortcut.
func bootApprovedClear() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, approvedKey+`\StartupFolder`, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(bootLnkName)
		k.Close()
	}
}
