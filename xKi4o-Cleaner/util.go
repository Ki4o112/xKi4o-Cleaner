package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

var reVdfPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// mergeLibraries returns main plus every "path" in Steam's
// libraryfolders.vdf, without duplicates.
func mergeLibraries(main, vdf string) []string {
	libs := []string{main}
	seen := map[string]bool{normPath(main): true}
	for _, m := range reVdfPath.FindAllStringSubmatch(vdf, -1) {
		p := filepath.Clean(strings.ReplaceAll(m[1], `\\`, `\`))
		if !seen[normPath(p)] {
			seen[normPath(p)] = true
			libs = append(libs, p)
		}
	}
	return libs
}
