//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"unsafe"

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

// removeOne deletes an empty folder or a link (the link itself, never its
// target). os.Remove already clears the read-only flag on files; folders get
// the same treatment.
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

// delError carries the reason a delete was refused after the file was
// already opened (so the permission was fine).
type delError struct {
	kind string
	err  error
}

func (e *delError) Error() string { return e.err.Error() }
func (e *delError) Unwrap() error { return e.err }

const fileShareNone = 0

// openExclusive opens a file for deletion and refuses to share it with
// anyone: it fails with a sharing violation whenever any other program has
// the file open (even programs that allow deletion), so a file in use is
// never pulled away from under a running program.
func openExclusive(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, access, fileShareNone, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

type fileBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
	_                                                       uint32
}

// deleteFile deletes one regular file, but only if no other program has it
// open. Read-only files are deleted too (the flag is cleared first).
func deleteFile(path string) error {
	h, err := openExclusive(path, windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	del := uint32(1)
	err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&del)), 4)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		var bi fileBasicInfo
		if windows.GetFileInformationByHandleEx(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&bi)), uint32(unsafe.Sizeof(bi))) == nil &&
			bi.FileAttributes&windows.FILE_ATTRIBUTE_READONLY != 0 {
			nb := fileBasicInfo{FileAttributes: bi.FileAttributes &^ windows.FILE_ATTRIBUTE_READONLY}
			if nb.FileAttributes == 0 {
				nb.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
			}
			if windows.SetFileInformationByHandle(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&nb)), uint32(unsafe.Sizeof(nb))) == nil {
				if err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&del)), 4); err == nil {
					return nil
				}
				// put the flag back, the file stays
				_ = windows.SetFileInformationByHandle(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&bi)), uint32(unsafe.Sizeof(bi)))
			}
		}
		// The file could be opened for deletion, so the permission is fine:
		// Windows refuses because the file is running or mapped (a loaded
		// program or library).
		return &delError{kind: "locked", err: err}
	}
	return err
}

// errKind sorts a delete error: locked (in use by another program),
// denied (no permission), gone (already removed) or failed.
func errKind(err error) string {
	var de *delError
	if errors.As(err, &de) {
		return de.kind
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION, windows.ERROR_USER_MAPPED_FILE:
			return "locked"
		case windows.ERROR_ACCESS_DENIED, windows.ERROR_PRIVILEGE_NOT_HELD:
			return "denied"
		case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND:
			return "gone"
		}
	}
	return "failed"
}

// probeDelete checks, without deleting anything, whether a file could be
// deleted right now by deleteFile. Returns "" when the file is free, or
// locked / denied / gone / failed.
func probeDelete(path string) string {
	h, err := openExclusive(path, windows.DELETE|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return errKind(err)
	}
	windows.CloseHandle(h)
	return ""
}
