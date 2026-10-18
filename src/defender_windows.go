//go:build windows

package main

// Help for the Windows Defender false positive. The app never turns Defender
// off and never adds an exclusion itself — a program that edits the antivirus
// is exactly what antiviruses treat as malware. Instead it OPENS Windows
// Security (so the user adds an exclusion there, in Microsoft's own screen)
// and the Microsoft report page, and shows the exe's SHA-256 so the user can
// check the file is the real one.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

type defenderInfo struct {
	Exe    string `json:"exe"`
	SHA256 string `json:"sha256"`
}

var (
	exeHashOnce sync.Once
	exeHashVal  string
	exePathVal  string
)

// exeSHA256 hashes the running exe once (so the user can compare it with the
// hash under the GitHub release).
func exeSHA256() (string, string) {
	exeHashOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		exePathVal = exe
		f, err := os.Open(exe)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			exeHashVal = hex.EncodeToString(h.Sum(nil))
		}
	})
	return exePathVal, exeHashVal
}

func defenderHelp() defenderInfo {
	exe, sum := exeSHA256()
	return defenderInfo{Exe: exe, SHA256: sum}
}

// openWindowsSecurity opens Virus & threat protection settings, where the user
// can add an exclusion themselves. It changes nothing on its own.
func openWindowsSecurity() bool {
	logEvent("defender", "info", "", T("log.defOpen"), nil)
	shellOpen("windowsdefender://threatsettings")
	return true
}

// reportFalsePositive opens Microsoft's false-positive submission page.
func reportFalsePositive() bool {
	shellOpen("https://www.microsoft.com/en-us/wdsi/filesubmission")
	return true
}
