package main

import (
	"strings"
	"testing"
	"time"
)

func TestApprovedBytes(t *testing.T) {
	on := approvedBytes(true, time.Now())
	off := approvedBytes(false, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if e, k := approvedEnabled(on); !e || !k || len(on) != 12 {
		t.Fatal("enabled value wrong")
	}
	if e, _ := approvedEnabled(off); e || off[0] != 3 {
		t.Fatal("disabled value wrong")
	}
	if e, _ := approvedEnabled([]byte{6, 0, 0, 0}); !e { // other "on" values Windows writes
		t.Fatal("06 should be enabled")
	}
	if e, _ := approvedEnabled([]byte{7, 0, 0, 0}); e {
		t.Fatal("07 should be disabled")
	}
}

func TestCommandParts(t *testing.T) {
	cases := []struct{ cmd, exe, args string }{
		{`"C:\Program Files\App\app.exe" --min`, `C:\Program Files\App\app.exe`, "--min"},
		{`C:\Program Files\App\app.exe -tray`, `C:\Program Files\App\app.exe`, "-tray"},
		{`C:\Windows\system32\rundll32.exe shell32.dll,Control_RunDLL`, `C:\Windows\system32\rundll32.exe`, "shell32.dll,Control_RunDLL"},
		{`ctfmon.exe`, `ctfmon.exe`, ""},
		{`  `, ``, ""},
		{`"C:\no end quote`, `C:\no end quote`, ""},
	}
	for _, c := range cases {
		exe, args := commandParts(c.cmd)
		if exe != c.exe || args != c.args {
			t.Errorf("%q -> %q / %q", c.cmd, exe, args)
		}
	}
	env := map[string]string{"ProgramFiles": `C:\Program Files`}
	if got := expandPercent(`%ProgramFiles%\x\%UNKNOWN%\y`, func(k string) string { return env[k] }); got != `C:\Program Files\x\%UNKNOWN%\y` {
		t.Errorf("expand: %q", got)
	}
}

func TestStartupWarnings(t *testing.T) {
	base := startupFacts{WinDir: "/win", Temp: "/tmp/u", Profile: "/home/u", Exists: true}
	f := base
	f.Exe, f.Signed = "/home/u/AppData/Roaming/svchost.exe", "NotSigned"
	if w := strings.Join(startupWarnings(f), ","); !strings.Contains(w, "sysname") {
		t.Errorf("fake system name not flagged: %s", w)
	}
	f = base
	f.Exe, f.Signed = "/tmp/u/x.exe", "NotSigned"
	if w := startupWarnings(f); len(w) != 1 || w[0] != "tempdir" {
		t.Errorf("temp: %v", w)
	}
	f = base
	f.Exe, f.Args = "/win/System32/WindowsPowerShell/v1.0/powershell.exe", "-w hidden -enc AAAA"
	if w := startupWarnings(f); len(w) != 1 || w[0] != "encoded" {
		t.Errorf("encoded: %v", w)
	}
	f = base
	f.Exe, f.Signed = "/opt/app/report.pdf.exe", "Valid"
	if w := startupWarnings(f); len(w) != 1 || w[0] != "doubleext" {
		t.Errorf("double ext: %v", w)
	}
	f = base
	f.Exe, f.Signed = "/opt/app/app.exe", "Valid"
	if w := startupWarnings(f); len(w) != 0 {
		t.Errorf("clean entry flagged: %v", w)
	}
}
