//go:build windows

package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// changesThisPC guards the tests that write to the registry (own test keys,
// the Steam path, startup entries) or close other programs (Notepad). On a
// real PC they are skipped, so running the tests there only reads the
// system and works in the tests' own temporary folders. They run under
// Wine (a throw-away Windows) or when XKI4O_WRITE_TESTS=1 is set on purpose.
// No test ever changes hibernation, power plans, boot settings, services or
// startup apps of the PC it runs on.
func changesThisPC(t *testing.T) {
	t.Helper()
	if underWine() || os.Getenv("XKI4O_WRITE_TESTS") == "1" {
		return
	}
	t.Skip("would change this PC (registry or other programs): runs only under Wine or with XKI4O_WRITE_TESTS=1")
}

// underWine: Wine's ntdll exports wine_get_version, Windows' does not.
func underWine() bool {
	return windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil
}

// The guard must not switch itself off under Wine (the full suite runs
// there), and must say which environment it found.
func TestSafetyGuard(t *testing.T) {
	t.Logf("under Wine: %v, XKI4O_WRITE_TESTS=%q", underWine(), os.Getenv("XKI4O_WRITE_TESTS"))
}
