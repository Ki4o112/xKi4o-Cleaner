package main

import (
	"strings"
	"testing"
)

func TestGameVerdict(t *testing.T) {
	cases := []struct {
		exe, path     string
		known, full3D bool
		want          bool
	}{
		{"Cyberpunk2077.exe", `D:\SteamLibrary\steamapps\common\Cyberpunk 2077\bin\x64\Cyberpunk2077.exe`, false, false, true},
		{"FortniteClient-Win64-Shipping.exe", `C:\Program Files\Epic Games\Fortnite\FortniteGame\Binaries\Win64\FortniteClient-Win64-Shipping.exe`, false, false, true},
		{"minecraft.exe", `C:\Games\Minecraft\minecraft.exe`, true, false, true},         // Windows knows it
		{"indiegame.exe", `C:\Users\a\Downloads\indie\indiegame.exe`, false, true, true}, // full screen with 3D
		{"steam.exe", `C:\Program Files (x86)\Steam\steam.exe`, false, true, false},
		{"steamwebhelper.exe", `C:\Program Files (x86)\Steam\bin\cef\steamwebhelper.exe`, false, false, false},
		{"UnityCrashHandler64.exe", `D:\SteamLibrary\steamapps\common\Game\UnityCrashHandler64.exe`, false, false, false},
		{"chrome.exe", `C:\Program Files\Google\Chrome\Application\chrome.exe`, false, true, false}, // a full-screen video
		{"vlc.exe", `C:\Program Files\VideoLAN\VLC\vlc.exe`, true, true, false},
		{"notepad.exe", `C:\Windows\notepad.exe`, false, false, false},
		{"xKi4oCleaner.exe", `C:\Tools\xKi4oCleaner.exe`, false, true, false},
		{"FortniteLauncher.exe", `C:\Program Files\Epic Games\Fortnite\FortniteGame\Binaries\Win64\FortniteLauncher.exe`, false, false, false},
		{"REDprelauncher.exe", `D:\SteamLibrary\steamapps\common\Cyberpunk 2077\REDprelauncher.exe`, true, false, false},
		{"LeagueClientUx.exe", `C:\Riot Games\League of Legends\LeagueClientUx.exe`, false, true, false},
		{"League of Legends.exe", `C:\Riot Games\League of Legends\Game\League of Legends.exe`, false, false, true},
		{"PlayGTAV.exe", `D:\SteamLibrary\steamapps\common\Grand Theft Auto V\PlayGTAV.exe`, false, false, false},
	}
	for _, c := range cases {
		if got := gameVerdict(c.exe, c.path, c.known, c.full3D); got != c.want {
			t.Errorf("%s: %v, want %v", c.exe, got, c.want)
		}
	}
}

func TestGameLaunchersAreKept(t *testing.T) {
	ps := []gmProc{
		{PID: 4, PPID: 0, Created: 1, Exe: "explorer.exe"},
		{PID: 10, PPID: 4, Created: 2, Exe: "steam.exe"},
		{PID: 20, PPID: 10, Created: 3, Exe: "eldenring.exe"},
		{PID: 30, PPID: 4, Created: 4, Exe: "discord.exe"},
		{PID: 40, PPID: 99, Created: 5, Exe: "orphan.exe"},
	}
	keep := gameLaunchers(ps, 20)
	if !keep["eldenring.exe"] || !keep["steam.exe"] || keep["discord.exe"] {
		t.Fatalf("%v", keep)
	}
	apps := withoutExes([]string{"Steam.exe", "discord.exe", "spotify.exe"}, keep)
	if strings.Join(apps, ",") != "discord.exe,spotify.exe" {
		t.Fatalf("the launcher must never be closed: %v", apps)
	}
	// a reused PID: a "parent" started after its child is not its parent
	ps = append(ps, gmProc{PID: 50, PPID: 60, Created: 1, Exe: "game.exe"}, gmProc{PID: 60, PPID: 4, Created: 9, Exe: "steam.exe"})
	if k := gameLaunchers(ps, 50); k["steam.exe"] {
		t.Fatalf("%v", k)
	}
}

func TestGameReportBits(t *testing.T) {
	for name, want := range map[string]uint32{"pid_1234_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D": 1234, "pid_7_x": 7, "luid_0x0": 0, "pid_x_1": 0, "pid_": 0} {
		if got := gpuInstancePID(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	for _, c := range []struct {
		s    gameSession
		want string
	}{
		{gameSession{TempMax: 88, GPUAvg: 99}, "hot"},
		{gameSession{MemMax: 93, GPUAvg: 60}, "ram"},
		{gameSession{GPUAvg: 97, CPUMax: 90}, "gpu"},
		{gameSession{GPUAvg: 55, CPUMax: 92}, "cpu"},
		{gameSession{GPUAvg: -1, CPUMax: 95}, "cpu"},
		{gameSession{GPUAvg: 70, CPUMax: 60, MemMax: 60, TempMax: 70}, "ok"},
	} {
		if got := gameLimit(c.s); got != c.want {
			t.Errorf("%+v: %s, want %s", c.s, got, c.want)
		}
	}
	var s gameSession
	s.addFPS(0, 0)
	s.addFPS(100, 60)
	s.addFPS(140, 80)
	if s.FPSAvg != 120 || s.FPSLow != 60 {
		t.Errorf("fps %v %v", s.FPSAvg, s.FPSLow)
	}
}

func TestBootTaskXML(t *testing.T) {
	x := bootTaskXML(`C:\Tools & Apps\xKi4oCleaner.exe`, "S-1-5-21-1", "desc")
	for _, want := range []string{"<LogonTrigger>", "<UserId>S-1-5-21-1</UserId>", "<RunLevel>HighestAvailable</RunLevel>", "<Arguments>--tray</Arguments>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", "<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>", `C:\Tools &amp; Apps\xKi4oCleaner.exe`} {
		if !strings.Contains(x, want) {
			t.Errorf("missing %s", want)
		}
	}
	if taskCommand(x) != `C:\Tools & Apps\xKi4oCleaner.exe` {
		t.Errorf("command %q", taskCommand(x))
	}
}
