package main

import (
	"reflect"
	"strings"
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

func TestGmHelpers(t *testing.T) {
	steam := `C:\Program Files (x86)\Steam\steam.exe`
	web := `C:\Program Files (x86)\Steam\bin\cef\cef.win64\steamwebhelper.exe`
	discord := `C:\Users\x\AppData\Local\Discord\app-1.0.9\Discord.exe`
	brave := `C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`
	gg := `C:\Program Files\SteelSeries\GG\SteelSeriesGG.exe`
	engine := `C:\Program Files\SteelSeries\GG\apps\engine\SteelSeriesEngine.exe`
	tool := `C:\Program Files\tool.exe`
	other := `C:\Program Files\other.exe`
	rows := []gmRow{
		{1, 0, 10, "steam.exe", steam},
		{2, 1, 20, "steamwebhelper.exe", web},
		{3, 2, 30, "steamwebhelper.exe", web}, // a helper of the helper
		{4, 2, 31, "steamwebhelper.exe", web},
		{10, 0, 10, "discord.exe", discord},
		{11, 10, 12, "discord.exe", discord},
		{12, 10, 40, "brave.exe", brave}, // a link opened from Discord: Brave stays on its own
		{13, 12, 41, "brave.exe", brave},
		{20, 0, 5, "steelseriesgg.exe", gg},
		{21, 20, 6, "steelseriesengine.exe", engine},
		{30, 0, 5, "other.exe", other},
		{31, 30, 6, "tool.exe", tool}, // both right in Program Files: not one program's folder
		{40, 99, 50, "late.exe", `C:\Apps\late\late.exe`},
		{41, 40, 1, "child.exe", `C:\Apps\late\child.exe`}, // "started" before its parent: the PID was reused
	}
	generic := func(d string) bool {
		return strings.EqualFold(d, `C:\Program Files`) || strings.EqualFold(d, `C:\Program Files (x86)`)
	}
	got := gmHelpers(rows, generic)
	want := map[string]string{"steamwebhelper.exe": "steam.exe", "steelseriesengine.exe": "steelseriesgg.exe"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// a helper that also runs on its own is offered on its own
	rows = append(rows, gmRow{50, 0, 60, "steamwebhelper.exe", web})
	if got := gmHelpers(rows, generic); got["steamwebhelper.exe"] != "" {
		t.Errorf("alone: %v", got)
	}
}

func TestParentDir(t *testing.T) {
	for in, want := range map[string]string{`C:\A\b.exe`: `C:\A`, `C:\b.exe`: `C:\`, `b.exe`: "", `C:/x/y`: `C:/x`, ``: ""} {
		if got := parentDir(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
