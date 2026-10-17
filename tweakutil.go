package main

// Small text helpers used by the tweaks. They are kept free of Windows
// calls so the tests can run on any OS.

import (
	"regexp"
	"strings"
)

// dxSettings parses DirectX's "Key=Value;Key=Value;" user setting string.
func dxSettings(s string) (keys []string, vals map[string]string) {
	vals = map[string]string{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, seen := vals[k]; !seen {
			keys = append(keys, k)
		}
		vals[k] = strings.TrimSpace(v)
	}
	return keys, vals
}

func dxJoin(keys []string, vals map[string]string) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + vals[k] + ";")
	}
	return b.String()
}

// dxSet sets one key and keeps every other setting as it was.
func dxSet(s, key, val string) string {
	keys, vals := dxSettings(s)
	if _, ok := vals[key]; !ok {
		keys = append(keys, key)
	}
	vals[key] = val
	return dxJoin(keys, vals)
}

// dxDel removes one key and keeps every other setting as it was.
func dxDel(s, key string) string {
	keys, vals := dxSettings(s)
	out := keys[:0]
	for _, k := range keys {
		if k != key {
			out = append(out, k)
		}
	}
	return dxJoin(out, vals)
}

func dxGet(s, key string) string {
	_, vals := dxSettings(s)
	return vals[key]
}

var reWTDefault = regexp.MustCompile(`("defaultProfile"\s*:\s*")(\{[0-9A-Fa-f-]{36}\})(")`)

// wtDefaultProfile returns the defaultProfile GUID of a Windows Terminal
// settings.json.
func wtDefaultProfile(content string) string {
	if m := reWTDefault.FindStringSubmatch(content); m != nil {
		return strings.ToLower(m[2])
	}
	return ""
}

// wtSetDefaultProfile changes only the defaultProfile value, so comments
// and the layout of the file stay exactly as they were.
func wtSetDefaultProfile(content, guid string) (string, bool) {
	loc := reWTDefault.FindStringSubmatchIndex(content)
	if loc == nil {
		return content, false
	}
	return content[:loc[4]] + guid + content[loc[5]:], true
}

// splitCommand splits an uninstall string such as
// `"C:\x\OneDriveSetup.exe" /uninstall` or `C:\x\setup.exe /uninstall`
// into the program and its arguments.
func splitCommand(s string) (exe string, args []string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if s[0] == '"' {
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return strings.Trim(s, `"`), nil
		}
		exe = s[1 : end+1]
		return exe, strings.Fields(s[end+2:])
	}
	if i := strings.Index(strings.ToLower(s), ".exe"); i >= 0 {
		return s[:i+4], strings.Fields(s[i+4:])
	}
	f := strings.Fields(s)
	return f[0], f[1:]
}

var reGUID = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)

// firstGUID returns the first GUID in a tool's output (lower case).
func firstGUID(s string) string {
	return strings.ToLower(reGUID.FindString(s))
}

// bcdFlag reads a boolean element from `bcdedit /enum {current}` output.
// Element names are never translated; the Yes/No value can be, so anything
// that does not look like a "no" counts as set.
func bcdFlag(output, name string) (present, on bool) {
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.EqualFold(f[0], name) {
			continue
		}
		v := strings.ToLower(f[1])
		no := v == "no" || v == "nein" || v == "non" || v == "nee" || v == "nej" || v == "nie" ||
			strings.HasPrefix(v, "не") || strings.HasPrefix(v, "нет") || v == "0" || v == "false"
		return true, !no
	}
	return false, false
}

// pkgMatch matches an app package name against a pattern that may end in *.
func pkgMatch(pattern, name string) bool {
	p, n := strings.ToLower(pattern), strings.ToLower(name)
	if strings.HasSuffix(p, "*") {
		return strings.HasPrefix(n, strings.TrimSuffix(p, "*"))
	}
	return p == n
}

// is24h reports whether a Windows time format uses the 24-hour clock.
func is24h(format string) bool {
	return strings.Contains(format, "H") && !strings.Contains(format, "h") && !strings.Contains(format, "t")
}

// psQuote quotes a string for a single-quoted PowerShell literal.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// isNewIn: something that came with version since is "new" while the app
// is that version (the same major.minor).
func isNewIn(since, current string) bool {
	s, ok1 := parseVersion(since)
	c, ok2 := parseVersion(current)
	return since != "" && ok1 && ok2 && s[0] == c[0] && s[1] == c[1]
}
