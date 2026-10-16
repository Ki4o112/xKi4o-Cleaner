//go:build windows

package main

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow = 0x08000000
	cpOEMCP        = 1
)

func hiddenCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	// Without this, Wait can block for ever after the timeout kills the
	// tool when a process it started still holds the output pipe open.
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// oemDecode converts console output in the OEM code page to UTF-8.
func oemDecode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) && !hasHighBytes(b) {
		return string(b)
	}
	n, err := windows.MultiByteToWideChar(cpOEMCP, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return string(b)
	}
	u := make([]uint16, n)
	if _, err = windows.MultiByteToWideChar(cpOEMCP, 0, &b[0], int32(len(b)), &u[0], n); err != nil {
		return string(b)
	}
	return windows.UTF16ToString(u)
}

func hasHighBytes(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

func decodeOutput(b []byte) string {
	if looksUTF16(b) {
		var d utf16Decoder
		return d.decode(b)
	}
	return oemDecode(b)
}

// runQuiet runs a program hidden and returns its decoded output and exit code.
func runQuiet(timeout time.Duration, name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hiddenCmd(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return decodeOutput(out.Bytes()), code, err
}

// runPS runs a Windows PowerShell script hidden. Output is UTF-8.
func runPS(timeout time.Duration, script string) (string, int, error) {
	ps := sys32(`WindowsPowerShell\v1.0\powershell.exe`)
	full := "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ProgressPreference='SilentlyContinue'; " + script
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hiddenCmd(ctx, ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", full)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return strings.TrimSpace(strings.TrimPrefix(out.String(), "\ufeff")), code, err
}

// runStream runs a console tool hidden and reports its output line by line.
func runStream(name string, args []string, onLine func(text string, replace bool)) (int, error) {
	return runStreamCtx(context.Background(), name, args, onLine)
}

// runStreamCtx is runStream that ends the tool when ctx is cancelled.
func runStreamCtx(ctx context.Context, name string, args []string, onLine func(text string, replace bool)) (int, error) {
	cmd := hiddenCmd(ctx, name, args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverLog("tool output")
		sp := &lineSplitter{onLine: onLine}
		var u16 *utf16Decoder
		decided := false
		var head []byte
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if !decided {
					head = append(head, chunk...)
					if len(head) < 8 && err == nil {
						continue
					}
					decided = true
					if looksUTF16(head) {
						u16 = &utf16Decoder{}
					}
					chunk = head
				}
				if u16 != nil {
					sp.feed(u16.decode(chunk))
				} else {
					sp.feed(oemDecode(chunk))
				}
			}
			if err != nil {
				if !decided && len(head) > 0 {
					sp.feed(decodeOutput(head))
				}
				sp.flush()
				return
			}
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return code, err
}
