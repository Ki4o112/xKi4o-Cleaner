package main

// When Microsoft Defender is gone or turned off, the virus scanner page uses
// ClamAV (open source, from Cisco; installed through winget on first use) with
// its own signature database. Next to it: a check of the places malware hides
// in (startup entries, the hosts file, the proxy). Free of Windows calls.

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const clamWingetID = "Cisco.ClamAV"

// freshclamConf: the smallest config freshclam accepts (the database folder is
// given on the command line).
const freshclamConf = "DatabaseMirror database.clamav.net\r\nConnectTimeout 30\r\nReceiveTimeout 120\r\n"

// parseClamLine reads one line of clamscan: "C:\x\file.exe: Win.Trojan.X FOUND"
// or "C:\x\file.txt: OK". Anything else (warnings, the summary) is neither.
func parseClamLine(line string) (path, sig string, found, ok bool) {
	line = strings.TrimSpace(line)
	i := strings.LastIndex(line, ": ")
	if i <= 0 {
		return "", "", false, false
	}
	path, res := line[:i], line[i+2:]
	switch {
	case strings.HasSuffix(res, " FOUND"):
		return path, strings.TrimSpace(strings.TrimSuffix(res, " FOUND")), true, false
	case res == "OK":
		return path, "", false, true
	}
	return "", "", false, false
}

// clamQuickDirs: where a quick scan looks — the folders malware lands in and
// starts from.
func clamQuickDirs(profile, temp, programData, winDir string) []string {
	var d []string
	add := func(p ...string) { d = append(d, filepath.Join(p...)) }
	if profile != "" {
		add(profile, "Downloads")
		add(profile, "Desktop")
		add(profile, "AppData", "Roaming")
		add(profile, "AppData", "Local", "Temp")
	}
	if temp != "" {
		add(temp)
	}
	if programData != "" {
		add(programData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	}
	if winDir != "" {
		add(winDir, "Temp")
	}
	add(`C:\Users\Public`)
	seen := map[string]bool{}
	var res []string
	for _, p := range d {
		k := strings.ToLower(strings.ReplaceAll(filepath.Clean(p), `\`, "/"))
		if !seen[k] {
			seen[k] = true
			res = append(res, p)
		}
	}
	return res
}

// hostsRedirects: the lines of the hosts file that send a name to a real
// address. Blocking lists (0.0.0.0, 127.x, ::1) are harmless and left out.
func hostsRedirects(text string) []string {
	var res []string
	for _, l := range strings.Split(text, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(f[0])
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		res = append(res, f[0]+" "+strings.Join(f[1:], " "))
	}
	return res
}

// quarName: the name a file gets in the quarantine folder; the ".quar" ending
// keeps Windows from running it.
func quarName(orig string, stamp int64) string {
	b := filepath.Base(strings.ReplaceAll(orig, `\`, "/"))
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, b) + "." + strconv.FormatInt(stamp, 10) + ".quar"
}
