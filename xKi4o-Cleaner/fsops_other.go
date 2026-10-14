//go:build !windows

package main

import (
	"io/fs"
	"os"
)

// Non-Windows builds exist only so the file engine can be unit-tested.

func isLink(fi fs.FileInfo) bool { return fi.Mode()&fs.ModeSymlink != 0 }

func removeOne(path string) error { return os.Remove(path) }
