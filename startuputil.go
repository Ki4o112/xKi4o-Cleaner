package main

// Startup Manager helpers that do not depend on Windows (unit-tested on any OS).

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StartupApproved values (what Task Manager writes): 12 bytes, the lowest
// bit of the first byte set = disabled, followed by the time it was
// disabled as a FILETIME.
func approvedEnabled(b []byte) (enabled, known bool) {
	if len(b) < 1 {
		return true, false
	}
	return b[0]&1 == 0, true
}

func approvedBytes(enable bool, now time.Time) []byte {
	b := make([]byte, 12)
	if enable {
		b[0] = 2
		return b
	}
	b[0] = 3
	// FILETIME: 100 ns intervals since 1601-01-01 UTC
	ft := uint64(now.UTC().UnixNano()/100) + 116444736000000000
	binary.LittleEndian.PutUint64(b[4:], ft)
	return b
}

// expandPercent expands %VAR% the way Windows does (unknown names stay).
func expandPercent(s string, get func(string) string) string {
	var out strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		j := strings.IndexByte(s[i+1:], '%')
		if j < 0 {
			out.WriteString(s)
			return out.String()
		}
		name := s[i+1 : i+1+j]
		out.WriteString(s[:i])
		if v := get(name); v != "" && name != "" {
			out.WriteString(v)
		} else {
			out.WriteString(s[i : i+2+j])
		}
		s = s[i+2+j:]
	}
}

var reExeInCmd = regexp.MustCompile(`(?i)^(.+?\.(exe|com|bat|cmd|vbs|js|ps1|scr|lnk))(\s|$)`)

// commandParts splits a startup command line into the program and its
// arguments. Unquoted paths with spaces ("C:\Program Files\x\x.exe -min")
// are handled the way Windows resolves them for well-formed entries.
func commandParts(cmd string) (exe, args string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", ""
	}
	if cmd[0] == '"' {
		if k := strings.IndexByte(cmd[1:], '"'); k >= 0 {
			return cmd[1 : 1+k], strings.TrimSpace(cmd[2+k:])
		}
		return strings.Trim(cmd, `"`), ""
	}
	if m := reExeInCmd.FindStringSubmatch(cmd); m != nil {
		return m[1], strings.TrimSpace(cmd[len(m[1]):])
	}
	if i := strings.IndexAny(cmd, " \t"); i > 0 {
		return cmd[:i], strings.TrimSpace(cmd[i+1:])
	}
	return cmd, ""
}

// System programs startup entries use as launchers: the file they start
// is in the arguments.
var launchers = map[string]bool{"rundll32.exe": true, "regsvr32.exe": true, "wscript.exe": true, "cscript.exe": true,
	"mshta.exe": true, "powershell.exe": true, "pwsh.exe": true, "cmd.exe": true, "conhost.exe": true}

// Names of Windows programs that malware likes to copy.
var systemNames = map[string]bool{"svchost.exe": true, "csrss.exe": true, "lsass.exe": true, "winlogon.exe": true, "explorer.exe": true,
	"services.exe": true, "smss.exe": true, "wininit.exe": true, "spoolsv.exe": true, "taskhostw.exe": true, "dwm.exe": true,
	"rundll32.exe": true, "conhost.exe": true, "ctfmon.exe": true, "lsm.exe": true, "system.exe": true}

// startupFacts are what the warning heuristics look at.
type startupFacts struct {
	Exe     string // expanded path of the program
	Args    string
	Exists  bool
	Signed  string // Valid, NotSigned, HashMismatch, ... ("" = unknown)
	Hidden  bool
	WinDir  string
	Temp    string
	Profile string
}

// startupWarnings returns reasons why an entry is worth a closer look.
// They are hints, not a verdict: legitimate programs can match some.
func startupWarnings(f startupFacts) []string {
	var w []string
	exe := strings.ToLower(filepath.Clean(f.Exe))
	base := strings.ToLower(filepath.Base(exe))
	args := strings.ToLower(f.Args)
	win := strings.ToLower(filepath.Clean(f.WinDir))
	under := func(dir string) bool {
		if dir == "" {
			return false
		}
		d := strings.ToLower(filepath.Clean(dir)) + string(filepath.Separator)
		return strings.HasPrefix(exe, d)
	}
	if systemNames[base] && win != "" && !under(win) && f.Exe != "" && strings.ContainsAny(f.Exe, `\/`) {
		w = append(w, "sysname")
	}
	if f.Exists && (f.Signed == "HashMismatch" || f.Signed == "NotTrusted") {
		w = append(w, "badsig")
	}
	unsigned := f.Signed == "NotSigned"
	if unsigned && (under(f.Temp) || under(filepath.Join(f.Profile, "Downloads")) || under(`C:\Users\Public`) || under(filepath.Join(f.Profile, "AppData", "Local", "Temp"))) {
		w = append(w, "tempdir")
	}
	if launchers[base] {
		switch {
		case strings.Contains(args, "-enc") || strings.Contains(args, "-encodedcommand") || strings.Contains(args, "frombase64string"):
			w = append(w, "encoded")
		case strings.Contains(args, "http://") || strings.Contains(args, "https://") || strings.Contains(args, "javascript:") || strings.Contains(args, "vbscript:"):
			w = append(w, "web")
		case strings.Contains(args, "-w hidden") || strings.Contains(args, "-windowstyle hidden") || strings.Contains(args, "-window hidden"):
			w = append(w, "hiddenwin")
		}
	}
	if reDoubleExt.MatchString(base) {
		w = append(w, "doubleext")
	}
	if f.Hidden && f.Exists {
		w = append(w, "hiddenfile")
	}
	return w
}

var reDoubleExt = regexp.MustCompile(`\.(pdf|doc|docx|xls|xlsx|jpg|jpeg|png|txt|mp3|mp4|zip)\.(exe|scr|com|bat|cmd|vbs|js)$`)

func fileExistsAny(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
