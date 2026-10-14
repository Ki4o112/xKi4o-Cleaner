//go:build windows

package main

import (
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// isLink is true for symlinks, junctions, mount points and any other
// reparse point. They are never followed.
func isLink(fi fs.FileInfo) bool {
	if fi.Mode()&fs.ModeSymlink != 0 {
		return true
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

// removeOne deletes a file, an empty folder or a link. os.Remove already
// clears the read-only flag on files; folders get the same treatment.
func removeOne(path string) error {
	err := os.Remove(path)
	if err == nil {
		return nil
	}
	p, perr := windows.UTF16PtrFromString(path)
	if perr != nil {
		return err
	}
	attrs, aerr := windows.GetFileAttributes(p)
	if aerr != nil || attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		return err
	}
	if windows.SetFileAttributes(p, attrs&^windows.FILE_ATTRIBUTE_READONLY) != nil {
		return err
	}
	return os.Remove(path)
}
