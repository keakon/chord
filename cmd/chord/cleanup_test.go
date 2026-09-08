package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/bytefmt"
	"github.com/keakon/chord/internal/maintenance"
)

func setCleanupPathEnv(t *testing.T) (stateDir, cacheDir, sessionsDir, logsDir string) {
	t.Helper()
	root := t.TempDir()
	stateDir = filepath.Join(root, "state")
	cacheDir = filepath.Join(root, "cache")
	sessionsDir = filepath.Join(root, "sessions")
	logsDir = filepath.Join(root, "logs")
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_CACHE_DIR", cacheDir)
	t.Setenv("CHORD_SESSIONS_DIR", sessionsDir)
	t.Setenv("CHORD_LOGS_DIR", logsDir)
	return stateDir, cacheDir, sessionsDir, logsDir
}

func TestCleanupStatusCommandUsesCommandOutput(t *testing.T) {
	stateDir, cacheDir, sessionsDir, logsDir := setCleanupPathEnv(t)
	for _, dir := range []string{stateDir, cacheDir, sessionsDir, logsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.bin"), []byte("abc"), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	var buf bytes.Buffer
	cmd := newCleanupCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cleanup status Execute: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"state_dir: " + stateDir,
		"cache_dir: " + cacheDir,
		"logs_dir: " + logsDir,
		"sessions: 0 across 0 projects",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cleanup status output = %q, want %q", out, want)
		}
	}
}

func TestCleanupLogsCommandDryRunUsesCommandOutputAndKeepsFiles(t *testing.T) {
	_, _, _, logsDir := setCleanupPathEnv(t)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll logs: %v", err)
	}
	oldLog := filepath.Join(logsDir, "old.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldLog, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old log: %v", err)
	}

	var buf bytes.Buffer
	cmd := newCleanupCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"logs", "--older-than", "1h"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cleanup logs Execute: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "would remove "+oldLog) || !strings.Contains(out, "dry-run: pass --yes to delete") {
		t.Fatalf("cleanup logs dry-run output = %q", out)
	}
	if _, err := os.Stat(oldLog); err != nil {
		t.Fatalf("dry-run should keep old log: %v", err)
	}
}

func TestCleanupLogsCommandYesDeletesCandidates(t *testing.T) {
	_, _, _, logsDir := setCleanupPathEnv(t)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll logs: %v", err)
	}
	oldLog := filepath.Join(logsDir, "old.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldLog, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old log: %v", err)
	}

	var buf bytes.Buffer
	cmd := newCleanupCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"logs", "--older-than", "1h", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cleanup logs --yes Execute: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "removed "+oldLog) || strings.Contains(out, "dry-run") {
		t.Fatalf("cleanup logs --yes output = %q", out)
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatalf("cleanup logs --yes should remove old log, stat err=%v", err)
	}
}

func TestCleanupByteFormatter(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0 B"},
		{name: "bytes", bytes: 847, want: "847 B"},
		{name: "kb", bytes: 1536, want: "1.5 KB"},
		{name: "mb", bytes: 276285348, want: "263.5 MB"},
		{name: "gb", bytes: 31775732436, want: "29.6 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bytefmt.Short(tt.bytes); got != tt.want {
				t.Fatalf("bytefmt.Short(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestWriteCleanupStatusUsesHumanReadableSizes(t *testing.T) {
	st := &maintenance.Status{
		StateDir:     "/state",
		CacheDir:     "/cache",
		LogsDir:      "/state/logs",
		StateBytes:   31775732436,
		CacheBytes:   847,
		LogsBytes:    276285348,
		SessionCount: 8260,
		ProjectCount: 8373,
		Warnings:     []string{"scan /locked: permission denied"},
	}

	var buf bytes.Buffer
	writeCleanupStatus(&buf, st)

	want := "state_dir: /state (29.6 GB)\n" +
		"cache_dir: /cache (847 B)\n" +
		"logs_dir: /state/logs (263.5 MB)\n" +
		"sessions: 8260 across 8373 projects\n" +
		"warning: scan /locked: permission denied\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup status output = %q, want %q", got, want)
	}
}

func TestWriteCleanupCandidateUsesHumanReadableSizes(t *testing.T) {
	candidate := maintenance.CleanupCandidate{
		Path:  "/state/sessions/proj/session-a",
		Bytes: 276285348,
	}

	var buf bytes.Buffer
	writeCleanupCandidate(&buf, "would remove", candidate)

	want := "would remove /state/sessions/proj/session-a (263.5 MB)\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup candidate output = %q, want %q", got, want)
	}
}

func TestWriteCleanupCandidateSkip(t *testing.T) {
	candidate := maintenance.CleanupCandidate{
		Path: "/state/logs/chord.log.2",
		Skip: "permission denied",
	}

	var buf bytes.Buffer
	writeCleanupCandidate(&buf, "would remove", candidate)

	want := "skip /state/logs/chord.log.2: permission denied\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup candidate skip output = %q, want %q", got, want)
	}
}

func TestWriteCleanupSummarySessions(t *testing.T) {
	removed := []maintenance.CleanupCandidate{
		{Path: "/sessions/p1/s1", Kind: "session", Bytes: 1000},
		{Path: "/sessions/p1/s2", Kind: "session", Bytes: 1000},
		{Path: "/sessions/p2", Kind: "empty project sessions", Bytes: 490},
	}

	var buf bytes.Buffer
	writeCleanupSummary(&buf, "removed", "sessions", removed)

	want := "removed 2 sessions, 1 empty project dirs, total 2.4 KB\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup sessions summary output = %q, want %q", got, want)
	}
}

func TestWriteCleanupSummarySessionsSingleKind(t *testing.T) {
	removed := []maintenance.CleanupCandidate{
		{Path: "/sessions/p1/s1", Kind: "session", Bytes: 1000},
	}

	var buf bytes.Buffer
	writeCleanupSummary(&buf, "would remove", "sessions", removed)

	want := "would remove 1 sessions, total 1000 B\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup sessions summary output = %q, want %q", got, want)
	}
}

func TestWriteCleanupSummaryOtherKinds(t *testing.T) {
	removed := []maintenance.CleanupCandidate{
		{Path: "/logs/chord.log.1", Kind: "logs", Bytes: 1000},
		{Path: "/logs/chord.log.2", Kind: "logs", Bytes: 2000},
		{Path: "/logs/chord.log.3", Kind: "logs", Bytes: 3000},
	}

	var buf bytes.Buffer
	writeCleanupSummary(&buf, "removed", "logs", removed)

	want := "removed 3 items, total 5.9 KB\n"
	if got := buf.String(); got != want {
		t.Fatalf("cleanup logs summary output = %q, want %q", got, want)
	}
}

func TestWriteCleanupSummaryEmpty(t *testing.T) {
	var buf bytes.Buffer
	writeCleanupSummary(&buf, "removed", "sessions", nil)
	if got := buf.String(); got != "" {
		t.Fatalf("cleanup summary for nothing removed = %q, want empty", got)
	}
}

func TestCleanupSessionsCommandYesPrintsSummary(t *testing.T) {
	_, _, sessionsDir, _ := setCleanupPathEnv(t)
	old := time.Now().Add(-2 * time.Hour)
	for _, project := range []string{"HOME-Workspace-p1", "HOME-Workspace-p2"} {
		projectDir := filepath.Join(sessionsDir, project)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", projectDir, err)
		}
		if err := os.WriteFile(filepath.Join(projectDir, "project.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write project.json: %v", err)
		}
	}
	for _, sid := range []string{"20260101000000000", "20260102000000000"} {
		sessionDir := filepath.Join(sessionsDir, "HOME-Workspace-p1", sid)
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", sessionDir, err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "main.jsonl"), []byte("line\n"), 0o600); err != nil {
			t.Fatalf("write main.jsonl: %v", err)
		}
	}
	// Age the whole tree last so creating session dirs does not refresh the
	// project dir mtimes past the cutoff.
	for _, project := range []string{"HOME-Workspace-p1", "HOME-Workspace-p2"} {
		projectDir := filepath.Join(sessionsDir, project)
		for _, sid := range []string{"20260101000000000", "20260102000000000"} {
			sessionDir := filepath.Join(projectDir, sid)
			if info, err := os.Stat(sessionDir); err == nil && info.IsDir() {
				if err := os.Chtimes(sessionDir, old, old); err != nil {
					t.Fatalf("Chtimes %s: %v", sessionDir, err)
				}
			}
		}
		if err := os.Chtimes(projectDir, old, old); err != nil {
			t.Fatalf("Chtimes %s: %v", projectDir, err)
		}
	}

	var buf bytes.Buffer
	cmd := newCleanupCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"sessions", "--older-than", "1h", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cleanup sessions --yes Execute: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "removed 2 sessions, 2 empty project dirs, total ") {
		t.Fatalf("cleanup sessions --yes output = %q, want summary line", out)
	}
	if strings.Contains(out, "dry-run") {
		t.Fatalf("cleanup sessions --yes output = %q, must not mention dry-run", out)
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, "HOME-Workspace-p1", "20260101000000000")); !os.IsNotExist(err) {
		t.Fatalf("cleanup sessions --yes should remove old session, stat err=%v", err)
	}
}
