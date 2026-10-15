package main

import (
	"reflect"
	"testing"
)

func TestGmRoots(t *testing.T) {
	procs := []gmProc{
		{PID: 10, PPID: 1, Created: 100, Exe: "chrome.exe", Path: `C:\Chrome\chrome.exe`, Cmd: `"C:\Chrome\chrome.exe"`},
		{PID: 11, PPID: 10, Created: 101, Exe: "chrome.exe", Cmd: `"C:\Chrome\chrome.exe" --type=renderer`},
		{PID: 12, PPID: 10, Created: 102, Exe: "chrome.exe", Cmd: `"C:\Chrome\chrome.exe" --type=gpu-process`},
		{PID: 20, PPID: 5, Created: 50, Exe: "discord.exe", Path: `C:\D\Discord.exe`, Cmd: `"C:\D\Discord.exe" --start-minimized`},
		{PID: 21, PPID: 20, Created: 51, Exe: "discord.exe", Cmd: `"C:\D\Discord.exe" --type=utility`},
		{PID: 30, PPID: 1, Created: 60, Exe: "notepad.exe", Cmd: "notepad a.txt"},
		{PID: 31, PPID: 1, Created: 61, Exe: "notepad.exe", Cmd: "notepad b.txt"},
		{PID: 32, PPID: 1, Created: 62, Exe: "notepad.exe", Cmd: "notepad b.txt"},
		{PID: 40, PPID: 1, Created: 70, Exe: "steam.exe", Cmd: "steam"},
		// a reused PID: the "parent" started after the child, so the child is a root
		{PID: 50, PPID: 60, Created: 80, Exe: "spotify.exe", Cmd: "spotify"},
		{PID: 60, PPID: 1, Created: 90, Exe: "spotify.exe", Cmd: "spotify --x"},
	}
	want := map[string]bool{"chrome.exe": true, "discord.exe": true, "notepad.exe": true, "spotify.exe": true}
	var got []uint32
	for _, p := range gmRoots(procs, want) {
		got = append(got, p.PID)
	}
	if !reflect.DeepEqual(got, []uint32{20, 30, 31, 50, 60, 10}) {
		t.Errorf("roots = %v", got)
	}
}

func TestGmCommand(t *testing.T) {
	cases := []struct {
		c    gmClosed
		want string
	}{
		{gmClosed{Path: `C:\D\Discord.exe`, Cmd: `"C:\D\Discord.exe" --start-minimized`}, `"C:\D\Discord.exe" --start-minimized`},
		{gmClosed{Path: `C:\D\Discord.exe`, Cmd: `"c:\d\discord.exe"`}, `"C:\D\Discord.exe"`},
		{gmClosed{Path: `C:\Tools\app.exe`, Cmd: `C:\Tools\app.exe -tray`}, `"C:\Tools\app.exe" -tray`},
		{gmClosed{Path: `C:\Tools\app.exe`, Cmd: ``}, `"C:\Tools\app.exe"`},
		// a command line that does not start with the program is not used
		{gmClosed{Path: `C:\Tools\app.exe`, Cmd: `cmd.exe /c del x`}, `"C:\Tools\app.exe"`},
		{gmClosed{Path: `C:\Tools\app.exe`, Cmd: `C:\Tools\app.exe.evil.exe`}, `"C:\Tools\app.exe"`},
	}
	for _, c := range cases {
		if got := gmCommand(c.c); got != c.want {
			t.Errorf("gmCommand(%+v) = %s, want %s", c.c, got, c.want)
		}
	}
	st := gmState{Closed: []gmClosed{{Exe: "a.exe", Path: `C:\a.exe`}, {Exe: "B.exe", Path: `C:\b.exe`}, {Exe: "c.exe"}}}
	if got := gmToStart(st, map[string]bool{"b.exe": true}); len(got) != 1 || got[0].Exe != "a.exe" {
		t.Errorf("to start: %+v", got)
	}
}
