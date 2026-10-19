//go:build windows

package main

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// benchFile reads and writes past Windows' cache (FILE_FLAG_NO_BUFFERING,
// write-through) so the drive itself is measured. It is "delete on close":
// the file disappears when it is closed, even if the app is closed first.
type benchFile struct{ h windows.Handle }

func openBenchFileWin(path string, size int64) (benchIO, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return nil, err
	}
	return &benchFile{h: h}, nil
}

func (f *benchFile) at(p []byte, off int64, write bool) (int, error) {
	ov := windows.Overlapped{Offset: uint32(off), OffsetHigh: uint32(off >> 32)}
	var n uint32
	var err error
	if write {
		err = windows.WriteFile(f.h, p, &n, &ov)
	} else {
		err = windows.ReadFile(f.h, p, &n, &ov)
	}
	if err == nil && int(n) != len(p) {
		err = errors.New("short transfer")
	}
	return int(n), err
}

func (f *benchFile) WriteAt(p []byte, off int64) (int, error) { return f.at(p, off, true) }
func (f *benchFile) ReadAt(p []byte, off int64) (int, error)  { return f.at(p, off, false) }
func (f *benchFile) Close() error                             { return windows.CloseHandle(f.h) }

func benchHistPath() string { return benchHistoryPath(os.Getenv("LOCALAPPDATA")) }

// benchRun runs the benchmark the page asked for and saves it in the history.
func benchRun(o benchOpts) map[string]any {
	drive := strings.ToUpper(strings.TrimSpace(o.Drive))
	if len(drive) < 1 || drive[0] < 'A' || drive[0] > 'Z' {
		drive = systemDrive()
	}
	drive = drive[:1] + ":"
	o.Drive = drive
	root := drive + `\`
	if o.Disk {
		rp, _ := windows.UTF16PtrFromString(root)
		if t := windows.GetDriveType(rp); t != windows.DRIVE_FIXED && t != windows.DRIVE_REMOVABLE {
			return map[string]any{"error": T("bench.err.drive", drive)}
		}
	}
	_, free := diskSpace(root)
	dir := root
	if strings.EqualFold(drive, systemDrive()) {
		dir = os.TempDir() // on the system drive: in Temp, not in the drive's root
		if !strings.EqualFold(dir[:2], drive) {
			dir = root
		}
	}
	benchProgress = func(step string, pct int) { emit("bench-progress", map[string]any{"step": step, "pct": pct}) }
	op := beginOp("bench", drive, T("log.bench"), map[string]any{"cpu": o.CPU, "ram": o.RAM, "disk": o.Disk})
	r := runBench(o, openBenchFileWin, dir, int64(free))
	if r.Error == errBenchBusy.Error() {
		op.end("", nil)
		return map[string]any{"error": T("bench.err.busy")}
	}
	if r.Disk != nil && r.Disk.Error != "" {
		if r.Disk.Error == "nospace" {
			r.Disk.Error = T("bench.err.space", drive)
		}
		op.fail(drive, r.Disk.Error, nil)
	}
	op.end(T("log.benchDone", r.Score), map[string]any{"score": r.Score, "cpu1": r.SCPU1, "cpuN": r.SCPUN, "ram": r.SRAM, "disk": r.SDisk})
	var h []benchResult
	if !r.Stopped && r.Score > 0 {
		h = addBenchHistory(benchHistPath(), r)
	} else {
		h = readBenchHistory(benchHistPath())
	}
	return map[string]any{"result": r, "history": h}
}

func benchStop() bool { bench.stop.Store(true); return true }

func benchHistory() []benchResult {
	h := readBenchHistory(benchHistPath())
	if h == nil {
		h = []benchResult{}
	}
	return h
}

func benchClearHistory() bool {
	err := os.Remove(benchHistPath())
	return err == nil || errors.Is(err, os.ErrNotExist)
}
