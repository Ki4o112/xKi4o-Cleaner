//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity on other systems exists for the unit tests (device + inode).
func fileIdentity(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), true
}
