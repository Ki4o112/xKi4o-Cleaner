//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// Non-Windows builds exist only so the file engine can be unit-tested.

func isLink(fi fs.FileInfo) bool { return fi.Mode()&fs.ModeSymlink != 0 }

func removeOne(path string) error { return os.Remove(path) }

func deleteFile(path string) error { return os.Remove(path) }

func errKind(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "gone"
	case errors.Is(err, syscall.EBUSY), errors.Is(err, syscall.ETXTBSY):
		return "locked"
	case errors.Is(err, fs.ErrPermission):
		return "denied"
	}
	return "failed"
}

// probeDelete has no portable way to see another program's open handles;
// on these test builds a file is free unless it is gone.
func probeDelete(path string) string {
	if _, err := os.Lstat(path); err != nil {
		return errKind(err)
	}
	return ""
}
