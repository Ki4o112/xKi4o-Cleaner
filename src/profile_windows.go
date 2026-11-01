//go:build windows

package main

import (
	"os"
	"runtime/pprof"
	"strconv"
	"time"
)

// profileCPU writes a CPU profile of the next XKI4O_CPUPROFILE_SECS seconds
// (60 by default) to path; for the tests, which ask for it.
func profileCPU(path string) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	secs, _ := strconv.Atoi(os.Getenv("XKI4O_CPUPROFILE_SECS"))
	if secs <= 0 {
		secs = 60
	}
	if pprof.StartCPUProfile(f) != nil {
		_ = f.Close()
		return
	}
	go func() {
		time.Sleep(time.Duration(secs) * time.Second)
		pprof.StopCPUProfile()
		_ = f.Close()
	}()
}
