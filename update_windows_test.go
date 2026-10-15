//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The update puts the new exe in place of the running one by renaming the
// running file first. Windows must allow that: this test renames its own
// running program and back.
func TestRunningExeCanBeRenamed(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	moved := exe + ".swaptest"
	if err := os.Rename(exe, moved); err != nil {
		t.Fatalf("a running exe could not be renamed: %v", err)
	}
	if err := os.Rename(moved, exe); err != nil {
		t.Fatalf("could not rename it back (it is now %s): %v", moved, err)
	}
	if !afterUpdate([]string{"--after-update=999999999"}) || afterUpdate([]string{"-x"}) {
		t.Error("afterUpdate flag")
	}
}
