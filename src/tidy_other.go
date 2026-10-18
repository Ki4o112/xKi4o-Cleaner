//go:build !windows

package main

import "os"

func reparseDir(string, os.DirEntry) bool { return false }
