package main

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Automatic cleaning never picks a sensitive category or one that needs a
// restore point, and leaves out what was unticked.
func TestAutoCleanPick(t *testing.T) {
	never := []string{"recycle", "recent", "clipboard", "windowsold", "nvdrivers", "msiorphans", "wucache", "diskcleanup", "dism", "shaders"}
	all := autoCleanPick(nil)
	for _, n := range never {
		for _, id := range all {
			if id == n {
				t.Errorf("%s must never be cleaned automatically", n)
			}
		}
	}
	got := autoCleanPick(map[string]any{"chrome": false, "temp": true, "discord": "junk"})
	if len(got) != len(all)-1 {
		t.Errorf("%d of %d", len(got), len(all))
	}
	for _, id := range got {
		if id == "chrome" {
			t.Error("unticked chrome was picked")
		}
	}
}

func TestAutoTaskXML(t *testing.T) {
	loc := time.FixedZone("EET", 3*3600)
	now := time.Date(2026, 10, 31, 22, 15, 0, 0, loc)
	if s := autoStart(now, 12, "weekly"); s.Format("2006-01-02 15:04") != "2026-11-01 12:00" {
		t.Errorf("weekly start %v", s)
	}
	if s := autoStart(time.Date(2026, 12, 20, 9, 0, 0, 0, loc), 12, "monthly"); s.Format("2006-01-02 15:04") != "2027-01-01 12:00" {
		t.Errorf("monthly start %v", s)
	}
	exe := `C:\Tools & Games\xKi4oCleaner.exe`
	for _, f := range []string{"daily", "weekly", "monthly"} {
		x, err := autoTaskXML(exe, "S-1-5-21-1-2-3-1001", f, autoStart(now, 12, f), "Cleaning <caches>")
		if err != nil {
			t.Fatal(f, err)
		}
		var v struct {
			Triggers struct {
				Cal struct {
					Start string `xml:"StartBoundary"`
					Day   *struct {
						N int `xml:"DaysInterval"`
					} `xml:"ScheduleByDay"`
					Week  *struct{} `xml:"ScheduleByWeek"`
					Month *struct {
						Day int `xml:"DaysOfMonth>Day"`
					} `xml:"ScheduleByMonth"`
				} `xml:"CalendarTrigger"`
			}
			Principal struct {
				User  string `xml:"UserId"`
				Level string `xml:"RunLevel"`
				Logon string `xml:"LogonType"`
			} `xml:"Principals>Principal"`
			Settings struct {
				Wake      bool `xml:"WakeToRun"`
				Battery   bool `xml:"DisallowStartIfOnBatteries"`
				Available bool `xml:"StartWhenAvailable"`
			}
			Exec struct {
				Cmd  string `xml:"Command"`
				Args string `xml:"Arguments"`
			} `xml:"Actions>Exec"`
		}
		// encoding/xml refuses the UTF-16 declaration; the body is plain XML
		body := x[strings.Index(x, "?>")+2:]
		if err := xml.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("%s: %v\n%s", f, err, x)
		}
		if v.Exec.Cmd != exe || v.Exec.Args != "--auto-clean" || taskCommand(x) != exe {
			t.Errorf("%s: command %q %q / %q", f, v.Exec.Cmd, v.Exec.Args, taskCommand(x))
		}
		if v.Principal.User != "S-1-5-21-1-2-3-1001" || v.Principal.Level != "HighestAvailable" || v.Principal.Logon != "InteractiveToken" {
			t.Errorf("%s: principal %+v", f, v.Principal)
		}
		if v.Settings.Wake || !v.Settings.Battery || !v.Settings.Available {
			t.Errorf("%s: settings %+v (never wakes the PC, not on battery, catches up)", f, v.Settings)
		}
		switch f {
		case "daily":
			if v.Triggers.Cal.Day == nil || v.Triggers.Cal.Day.N != 1 {
				t.Errorf("daily %+v", v.Triggers.Cal)
			}
		case "weekly":
			if v.Triggers.Cal.Week == nil || !strings.Contains(x, "<Sunday />") || v.Triggers.Cal.Start != "2026-11-01T12:00:00" {
				t.Errorf("weekly %+v", v.Triggers.Cal)
			}
		case "monthly":
			if v.Triggers.Cal.Month == nil || v.Triggers.Cal.Month.Day != 1 {
				t.Errorf("monthly %+v", v.Triggers.Cal)
			}
		}
	}
	if _, err := autoTaskXML(exe, "", "hourly", now, ""); err == nil {
		t.Error("hourly accepted")
	}
	if _, err := autoTaskXML(exe, "", "monthly", time.Date(2026, 1, 30, 12, 0, 0, 0, loc), ""); err == nil {
		t.Error("the 30th accepted for every month")
	}
	if taskCommand("<Exec><Command>\"C:\\a b\\x.exe\"</Command></Exec>") != `C:\a b\x.exe` || taskCommand("nothing") != "" {
		t.Error("taskCommand")
	}
}

func TestMaintLines(t *testing.T) {
	tr := func(k string, a ...any) string { return k + fmt.Sprint(a...) }
	got := strings.Join(maintLines(maintSum{Freed: 1 << 30, RAM: 0, Disks: 1, AppUpd: 3, Reboot: true}, tr), "|")
	if got != "am.freed1.0 GB|am.disks1|am.apps3|am.reboot" {
		t.Fatal(got)
	}
	if got := strings.Join(maintLines(maintSum{}, tr), "|"); got != "am.freed0 B|am.disksOK" {
		t.Fatal(got)
	}
}
