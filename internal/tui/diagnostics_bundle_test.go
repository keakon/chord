package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectDiagnosticLogTailSelectsCurrentProcessFromSharedLog(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	logPath := filepath.Join(dir, "chord.log")
	content := strings.Join([]string{
		"[I 2026-05-02 01:52:57 common:1 pwd=/tmp/other pid=1 sid=other] other process",
		"[W 2026-05-02 01:52:57 stderr_redirect:1 pwd=/tmp/other pid=1 sid=other] stderr_text=\"other stderr line\"",
		fmt.Sprintf("[I 2026-05-02 01:52:58 common:1 pwd=%s pid=2 sid=session1] start", baseDir),
		"panic: something happened in current process",
		"stack frame line 1",
		"[I 2026-05-02 01:52:59 common:1 pwd=/tmp/other2 pid=3 sid=other-2] next process",
		"[W 2026-05-02 01:52:59 stderr_redirect:1 pwd=/tmp/other2 pid=3 sid=other-2] stderr_text=\"other stderr line 2\"",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(log): %v", err)
	}

	name, tail := collectDiagnosticLogTail(dir, baseDir, 2)
	if name != "chord.log" {
		t.Fatalf("log name = %q, want chord.log", name)
	}
	if !strings.Contains(tail, "pid=2") {
		t.Fatalf("tail should include current process line, got %q", tail)
	}
	if !strings.Contains(tail, "panic: something happened in current process") {
		t.Fatalf("tail should include current process stderr continuation, got %q", tail)
	}
	if strings.Contains(tail, "other stderr line") || strings.Contains(tail, "pid=1") || strings.Contains(tail, "pid=3") {
		t.Fatalf("tail should exclude other processes, got %q", tail)
	}
	if strings.Contains(tail, baseDir) {
		t.Fatalf("tail should sanitize base dir, got %q", tail)
	}
}

func TestCollectDiagnosticLogTailIncludesStartupLinesBeforeSession(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "project")
	logPath := filepath.Join(dir, "chord.log")
	content := strings.Join([]string{
		"[I 2026-05-02 01:52:57 common:1 pwd=/tmp/other pid=1] other startup",
		fmt.Sprintf("[I 2026-05-02 01:52:57 common:1 pwd=%s pid=2] current startup", baseDir),
		fmt.Sprintf("[I 2026-05-02 01:52:58 common:1 pwd=%s pid=2 sid=session1] session start", baseDir),
		"[I 2026-05-02 01:52:59 common:1 pwd=/tmp/other pid=1 sid=other] other session",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(log): %v", err)
	}

	_, tail := collectDiagnosticLogTail(dir, baseDir, 2)
	if !strings.Contains(tail, "current startup") || !strings.Contains(tail, "session start") {
		t.Fatalf("tail should include same pid startup and sid lines, got %q", tail)
	}
	if strings.Contains(tail, "other startup") || strings.Contains(tail, "other session") {
		t.Fatalf("tail should exclude other pid lines, got %q", tail)
	}
}

func TestCollectDiagnosticLogTailFallsBackToRotatedSharedLog(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rotatedPath := filepath.Join(dir, "chord.log.1")
	if err := os.WriteFile(rotatedPath, []byte(fmt.Sprintf("[I 2026-05-02 01:52:58 common:1 pwd=%s pid=2 sid=rot-session] rotated\n", baseDir)), 0o644); err != nil {
		t.Fatalf("WriteFile(rotated): %v", err)
	}
	currentPath := filepath.Join(dir, "chord.log")
	if err := os.WriteFile(currentPath, []byte("[I 2026-05-02 01:52:59 common:1 pwd=/tmp/other pid=3 sid=other] current\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(current): %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(currentPath, now, now); err != nil {
		t.Fatalf("Chtimes(current): %v", err)
	}
	if err := os.Chtimes(rotatedPath, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("Chtimes(rotated): %v", err)
	}

	name, tail := collectDiagnosticLogTail(dir, baseDir, 2)
	if name != "chord.log.1" {
		t.Fatalf("log name = %q, want chord.log.1", name)
	}
	if !strings.Contains(tail, "pid=2") {
		t.Fatalf("tail = %q, want process line", tail)
	}
}

func TestCollectDiagnosticLogTailDoesNotPrefixMatchPid(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "project")
	logPath := filepath.Join(dir, "chord.log")
	content := strings.Join([]string{
		"[I 2026-05-02 01:52:57 common:1 pwd=/tmp/other pid=20] other pid prefix",
		"[I 2026-05-02 01:52:58 common:1 pwd=/tmp/other pid=20 sid=abc2] other sid prefix",
		fmt.Sprintf("[I 2026-05-02 01:52:59 common:1 pwd=%s pid=2 sid=abc] current session", baseDir),
		"current stderr continuation",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(log): %v", err)
	}

	_, tail := collectDiagnosticLogTail(dir, baseDir, 2)
	if !strings.Contains(tail, "current session") || !strings.Contains(tail, "current stderr continuation") {
		t.Fatalf("tail should include exact current pid lines, got %q", tail)
	}
	if strings.Contains(tail, "other pid prefix") || strings.Contains(tail, "pid=20") {
		t.Fatalf("tail should not include prefix-matched pid lines, got %q", tail)
	}
}

func TestIsRuntimeLogFile(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "", ok: false},
		{name: "chord.log", ok: true},
		{name: "chord.log.1", ok: true},
		{name: "chord.log.9", ok: true},
		{name: "chord.log.x", ok: false},
		{name: "chord-42.log", ok: false},
	}
	for _, tc := range cases {
		if got := isRuntimeLogFile(tc.name); got != tc.ok {
			t.Fatalf("isRuntimeLogFile(%q) = %t, want %t", tc.name, got, tc.ok)
		}
	}
}

func TestBuildDiagnosticsMetadataIncludesProcessInstanceID(t *testing.T) {
	m := NewModel(nil)
	m.SetInstanceID("111-222")
	meta := m.buildDiagnosticsMetadata(time.Unix(0, 0), "/diagnostics", "/tmp/proj", "/tmp/proj/out.zip", "chord.log")
	if !strings.Contains(meta, "process_instance_id: 111-222") {
		t.Fatalf("metadata = %q, want process_instance_id", meta)
	}
}
