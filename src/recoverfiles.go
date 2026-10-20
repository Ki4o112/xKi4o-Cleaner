package main

// Recover deleted files with Microsoft's Windows File Recovery (winfr, from
// the Microsoft Store). The page picks the drive, what to look for and where
// to save; this builds winfr's command and reads its progress. winfr never
// writes to the drive it searches (the destination must be another drive).
// Free of Windows calls.

import (
	"regexp"
	"strconv"
	"strings"
)

const winfrStoreID = "9N26S50LN705"

var recoverKinds = map[string][]string{
	"photos":   {"jpg", "jpeg", "png", "heic", "gif", "bmp", "webp", "tif", "tiff"},
	"docs":     {"doc", "docx", "xls", "xlsx", "ppt", "pptx", "pdf", "txt", "odt", "rtf", "csv"},
	"videos":   {"mp4", "mov", "mkv", "avi", "wmv", "webm"},
	"music":    {"mp3", "wav", "flac", "m4a", "aac", "ogg"},
	"archives": {"zip", "rar", "7z"},
}

type recoverReq struct {
	Src   string   `json:"src"` // "C"
	Dst   string   `json:"dst"` // "D"
	Kinds []string `json:"kinds"`
	Name  string   `json:"name"` // part of a file name, optional
	Deep  bool     `json:"deep"` // extensive mode: slower, finds older or formatted files
}

func driveLetter(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(s), ":"))
	return s, len(s) == 1 && s[0] >= 'A' && s[0] <= 'Z'
}

// winfrArgs builds winfr's arguments, or "" and the reason's text key.
func winfrArgs(r recoverReq) ([]string, string) {
	src, ok1 := driveLetter(r.Src)
	dst, ok2 := driveLetter(r.Dst)
	if !ok1 || !ok2 {
		return nil, "rf.err.drive"
	}
	if src == dst {
		return nil, "rf.err.same"
	}
	mode := "/regular"
	if r.Deep {
		mode = "/extensive"
	}
	args := []string{src + ":", dst + ":", mode, "/a"}
	name := strings.TrimSpace(r.Name)
	if name != "" {
		if len(name) > 80 || strings.ContainsAny(name, "\"/\\:<>|") {
			return nil, "rf.err.name"
		}
		if !strings.ContainsAny(name, "*?") {
			name = "*" + name + "*"
		}
		args = append(args, "/n", name)
	}
	for _, k := range r.Kinds {
		for _, ext := range recoverKinds[k] {
			args = append(args, "/n", "*."+ext)
		}
	}
	if len(args) == 4 {
		return nil, "rf.err.what" // winfr needs a filter on the system drive, and a search for everything is never useful
	}
	return args, ""
}

var reWinfrPct = regexp.MustCompile(`(\d{1,3})\s*%`)

// winfrPercent: the last "NN%" in a line of winfr's output, or -1.
func winfrPercent(line string) int {
	m := reWinfrPct.FindAllStringSubmatch(line, -1)
	if len(m) == 0 {
		return -1
	}
	n, _ := strconv.Atoi(m[len(m)-1][1])
	return min(n, 100)
}
