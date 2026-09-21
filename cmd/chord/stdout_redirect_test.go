package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedirectProcessStdoutMovesFdOneIntoLog(t *testing.T) {
	original, err := os.Stdout.Stat()
	if err != nil {
		t.Fatalf("stat stdout: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "chord.log")
	protocol, redirect, err := redirectProcessStdout(logPath)
	if err != nil {
		t.Fatalf("redirectProcessStdout returned error: %v", err)
	}
	if _, err := protocol.WriteString("to-protocol\n"); err != nil {
		t.Fatalf("write to saved stdout: %v", err)
	}
	if _, err := os.Stdout.WriteString("to-log\n"); err != nil {
		t.Fatalf("write to redirected stdout: %v", err)
	}

	protocolInfo, err := protocol.Stat()
	if err != nil {
		t.Fatalf("stat saved stdout: %v", err)
	}
	if !os.SameFile(original, protocolInfo) {
		t.Error("the saved protocol stream must be the original stdout")
	}
	logInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		t.Fatalf("stat redirected stdout: %v", err)
	}
	if !os.SameFile(stdoutInfo, logInfo) {
		t.Error("fd 1 must point at the log file while the guard is active")
	}
	if os.SameFile(protocolInfo, logInfo) {
		t.Error("the protocol stream must not be the log file")
	}

	if err := redirect.Close(); err != nil {
		t.Fatalf("close redirect: %v", err)
	}
	restored, err := os.Stdout.Stat()
	if err != nil {
		t.Fatalf("stat restored stdout: %v", err)
	}
	if os.SameFile(restored, logInfo) {
		t.Error("closing the guard must restore the original stdout")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "to-log") {
		t.Errorf("log file = %q, want the redirected stdout write", data)
	}
	if strings.Contains(string(data), "to-protocol") {
		t.Errorf("log file = %q, protocol bytes must not land there", data)
	}
}

func TestStdoutRedirectRebindFollowsRotation(t *testing.T) {
	dir := t.TempDir()
	protocol, redirect, err := redirectProcessStdout(filepath.Join(dir, "first.log"))
	if err != nil {
		t.Fatalf("redirectProcessStdout returned error: %v", err)
	}

	rotatedPath := filepath.Join(dir, "second.log")
	rotated, err := os.OpenFile(rotatedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open rotated log: %v", err)
	}
	rebindErr := redirect.Rebind(rotated)
	var writeErr error
	if rebindErr == nil {
		_, writeErr = os.Stdout.WriteString("after-rotation\n")
	}
	// Restore fd 1 before asserting, so failures land in the test output rather
	// than the log file under test. The saved stdout duplicate must stay open
	// until the guard releases it.
	closeErr := redirect.Close()
	_ = protocol.Close()

	if rebindErr != nil {
		t.Fatalf("rebind: %v", rebindErr)
	}
	if writeErr != nil {
		t.Fatalf("write after rebind: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close redirect: %v", closeErr)
	}
	data, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("read rotated log: %v", err)
	}
	if !strings.Contains(string(data), "after-rotation") {
		t.Fatalf("rotated log = %q, want the write after rebinding", data)
	}
}

// A file adopted from the runtime log writer belongs to that writer: closing
// the guard must restore fd 1 without closing a file somebody else still owns.
func TestStdoutRedirectCloseLeavesAdoptedFileOpen(t *testing.T) {
	dir := t.TempDir()
	protocol, redirect, err := redirectProcessStdout(filepath.Join(dir, "early.log"))
	if err != nil {
		t.Fatalf("redirectProcessStdout returned error: %v", err)
	}
	t.Cleanup(func() { _ = protocol.Close() })

	adopted, err := os.OpenFile(filepath.Join(dir, "runtime.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open runtime log: %v", err)
	}
	t.Cleanup(func() { _ = adopted.Close() })
	if err := redirect.Rebind(adopted); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if err := redirect.Close(); err != nil {
		t.Fatalf("close redirect: %v", err)
	}
	if _, err := adopted.WriteString("still-open\n"); err != nil {
		t.Fatalf("the adopted log file was closed by the guard: %v", err)
	}
}

// Log rotation replaces the file the writer holds; fd 1 has to follow it, or a
// stray stdout write lands in the rotated file instead of the live one.
func TestStdoutRedirectFollowsLogRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "chord.log")
	protocol, redirect, err := redirectProcessStdout(logPath)
	if err != nil {
		t.Fatalf("redirectProcessStdout returned error: %v", err)
	}
	t.Cleanup(func() { _ = protocol.Close() })

	writer, err := newRotatingLogFileWithOptions(logPath, rotatingLogOptions{
		MaxSize:             64,
		MaxFiles:            2,
		CheckEveryBytes:     1 << 20,
		MaintenanceInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("newRotatingLogFile: %v", err)
	}
	if err := redirect.Rebind(writer.CurrentFile()); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	writer.SetStdoutRedirect(redirect)

	if _, err := writer.Write([]byte(strings.Repeat("x", 128))); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := writer.maybeMaintain(); err != nil {
		t.Fatalf("maybeMaintain: %v", err)
	}

	var stdoutInfo, currentInfo os.FileInfo
	stdoutInfo, err = os.Stdout.Stat()
	if err != nil {
		t.Fatalf("stat stdout: %v", err)
	}
	currentInfo, err = writer.CurrentFile().Stat()
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	followed := os.SameFile(stdoutInfo, currentInfo)

	// Restore fd 1 before reporting, so a failure reaches the test output and
	// later tests still write to the real stdout. The redirect leaves the
	// adopted file to the writer.
	if closeErr := redirect.Close(); closeErr != nil {
		t.Fatalf("close redirect: %v", closeErr)
	}
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("close log writer: %v", closeErr)
	}
	if !followed {
		t.Fatal("fd 1 does not follow log rotation: stdout is not the writer's current file")
	}
}

// The saved stdout stream owns its descriptor, and the guard releases it
// exactly once. Closing it a second time at the fd level would land on whatever
// file the runtime has opened since, which then fails with EBADF.
func TestStdoutRedirectCloseReleasesSavedDescriptorOnce(t *testing.T) {
	dir := t.TempDir()
	protocol, redirect, err := redirectProcessStdout(filepath.Join(dir, "chord.log"))
	if err != nil {
		t.Fatalf("redirectProcessStdout returned error: %v", err)
	}
	if err := redirect.Close(); err != nil {
		t.Fatalf("close redirect: %v", err)
	}
	if err := protocol.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closing the released stream reported %v, want it already closed", err)
	}
}

func TestRedirectProcessStdoutRequiresLogPath(t *testing.T) {
	protocol, redirect, err := redirectProcessStdout("")
	if err == nil {
		_ = protocol
		_ = redirect
		t.Fatal("an empty log path must be rejected")
	}
}
