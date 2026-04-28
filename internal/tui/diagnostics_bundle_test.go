package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectDiagnosticLogTailSelectsCurrentInstanceFromSharedLog(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	instanceID := "12345-999"
	logPath := filepath.Join(dir, "chord.log")
	content := strings.Join([]string{
		"time=2026-01-01T00:00:00Z level=INFO msg=other pid=1 instance_id=other-1 project_root=/tmp/other",
		"time=2026-01-01T00:00:00Z level=WARN msg=stderr pid=1 instance_id=other-1 stderr_text=\"other stderr line\"",
		fmt.Sprintf("time=2026-01-01T00:00:01Z level=INFO msg=start pid=2 instance_id=%s project_root=%s", instanceID, baseDir),
		fmt.Sprintf("time=2026-01-01T00:00:01Z level=WARN msg=stderr pid=2 instance_id=%s stderr_text=\"panic: something happened under current instance\"", instanceID),
		fmt.Sprintf("time=2026-01-01T00:00:01Z level=WARN msg=stderr pid=2 instance_id=%s stderr_text=\"stack frame line 1\"", instanceID),
		"time=2026-01-01T00:00:02Z level=INFO msg=next pid=3 instance_id=other-2 project_root=/tmp/other2",
		"time=2026-01-01T00:00:02Z level=WARN msg=stderr pid=3 instance_id=other-2 stderr_text=\"other stderr line 2\"",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(log): %v", err)
	}

	name, tail := collectDiagnosticLogTail(dir, baseDir, instanceID)
	if name != "chord.log" {
		t.Fatalf("log name = %q, want chord.log", name)
	}
	if !strings.Contains(tail, "instance_id="+instanceID) {
		t.Fatalf("tail should include current instance line, got %q", tail)
	}
	if !strings.Contains(tail, "panic: something happened under current instance") {
		t.Fatalf("tail should include current instance stderr, got %q", tail)
	}
	if strings.Contains(tail, "other stderr line") || strings.Contains(tail, "other-2") {
		t.Fatalf("tail should exclude other instances, got %q", tail)
	}
	if strings.Contains(tail, baseDir) {
		t.Fatalf("tail should sanitize base dir, got %q", tail)
	}
}

func TestCollectDiagnosticLogTailFallsBackToRotatedSharedLog(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	instanceID := "rot-1"
	rotatedPath := filepath.Join(dir, "chord.log.1")
	if err := os.WriteFile(rotatedPath, []byte(fmt.Sprintf("time=2026 level=INFO instance_id=%s project_root=%s\n", instanceID, baseDir)), 0o644); err != nil {
		t.Fatalf("WriteFile(rotated): %v", err)
	}
	currentPath := filepath.Join(dir, "chord.log")
	if err := os.WriteFile(currentPath, []byte("time=2026 level=INFO instance_id=other\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(current): %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(currentPath, now, now); err != nil {
		t.Fatalf("Chtimes(current): %v", err)
	}
	if err := os.Chtimes(rotatedPath, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("Chtimes(rotated): %v", err)
	}

	name, tail := collectDiagnosticLogTail(dir, baseDir, instanceID)
	if name != "chord.log.1" {
		t.Fatalf("log name = %q, want chord.log.1", name)
	}
	if !strings.Contains(tail, "instance_id="+instanceID) {
		t.Fatalf("tail = %q, want instance line", tail)
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

func TestBuildDiagnosticsMetadataIncludesInstanceID(t *testing.T) {
	m := NewModel(nil)
	m.SetInstanceID("111-222")
	meta := m.buildDiagnosticsMetadata(time.Unix(0, 0), "/diagnostics", "/tmp/proj", "/tmp/proj/out.zip", "chord.log")
	if !strings.Contains(meta, "instance_id: 111-222") {
		t.Fatalf("metadata = %q, want instance_id", meta)
	}
}
