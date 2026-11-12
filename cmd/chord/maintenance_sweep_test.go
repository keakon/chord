package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func writeStartupSessionDir(t *testing.T, sessionsDir, id string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPlanSessionStartupResumeRejectsTombstone(t *testing.T) {
	sessionsDir := t.TempDir()
	writeStartupSessionDir(t, sessionsDir, "20260427153042123.deleting")

	for _, id := range []string{"20260427153042123.deleting", "20260427153042123.deleting/", "../20260427153042123.deleting", "."} {
		plan, err := planSessionStartup(sessionsDir, t.TempDir(), sessionStartupOptions{ResumeID: id})
		if plan.SessionLock != nil {
			_ = plan.SessionLock.Release()
		}
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("resume %q = %v, want not-found error", id, err)
		}
	}
}

func TestPlanSessionStartupResumeRechecksTranscriptAfterLock(t *testing.T) {
	sessionsDir := t.TempDir()
	stateDir := t.TempDir()
	// A session that looks alive on disk but holds no transcript: the pre-lock
	// check runs against main.jsonl, so simulate the deletion-protocol window
	// by pointing the resume at a dir whose transcript is removed after the
	// stat would have passed — ownedSessionDirHoldsTranscript must still refuse.
	sessionDir := writeStartupSessionDir(t, sessionsDir, "20260427153042123")
	if !ownedSessionDirHoldsTranscript(sessionDir) {
		t.Fatal("session with transcript must verify")
	}
	if err := os.Remove(filepath.Join(sessionDir, "main.jsonl")); err != nil {
		t.Fatal(err)
	}
	if ownedSessionDirHoldsTranscript(sessionDir) {
		t.Fatal("transcript-less dir must not verify")
	}
	// The resume path itself refuses before locking when the transcript is
	// already gone.
	_, err := planSessionStartup(sessionsDir, stateDir, sessionStartupOptions{ResumeID: "20260427153042123"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resume without transcript = %v, want not-found error", err)
	}
}

func TestPlanContinueFromCandidatesSkipsVanishedAndClaimsNext(t *testing.T) {
	sessionsDir := t.TempDir()
	vanished := filepath.Join(sessionsDir, "20260427153042123")
	if err := os.MkdirAll(vanished, 0o755); err != nil {
		t.Fatal(err)
	}
	live := writeStartupSessionDir(t, sessionsDir, "20260427153042124")

	plan, taken, err := planContinueFromCandidates([]string{vanished, live})
	if err != nil {
		t.Fatal(err)
	}
	if !taken || plan.SessionDir != live || plan.SessionLock == nil {
		t.Fatalf("plan = %#v taken=%v, want live session claimed", plan, taken)
	}
	defer plan.SessionLock.Release()
}

func TestPlanContinueFromCandidatesReportsSkippedLocked(t *testing.T) {
	sessionsDir := t.TempDir()
	locked := writeStartupSessionDir(t, sessionsDir, "20260427153042123")
	lock, err := recovery.AcquireSessionLock(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	plan, taken, err := planContinueFromCandidates([]string{locked})
	if err != nil || taken {
		t.Fatalf("taken=%v err=%v, want no claim", taken, err)
	}
	if len(plan.SkippedLockedIDs) != 1 || plan.SkippedLockedIDs[0] != "20260427153042123" {
		t.Fatalf("skipped = %v", plan.SkippedLockedIDs)
	}
}

func TestCleanupDumpsCommandDryRunAndYes(t *testing.T) {
	_, _, sessionsDir, _ := setCleanupPathEnv(t)
	sessionDir := writeStartupSessionDir(t, filepath.Join(sessionsDir, "HOME-Workspace-p1"), "20260427153042123")
	dumpsDir := filepath.Join(sessionDir, "dumps", "llm")
	if err := os.MkdirAll(dumpsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldDump := filepath.Join(dumpsDir, "0001_20260426_120000_openai_gpt.json")
	if err := os.WriteFile(oldDump, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldDump, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	freshDump := filepath.Join(dumpsDir, "0002_20260427_120000_openai_gpt.json")
	if err := os.WriteFile(freshDump, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		var buf bytes.Buffer
		cmd := newCleanupCmd()
		cmd.SetOut(&buf)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cleanup %v Execute: %v", args, err)
		}
		return buf.String()
	}

	dry := run("dumps", "--older-than", "24h")
	if !strings.Contains(dry, "would remove") || !strings.Contains(dry, "0001_20260426_120000_openai_gpt.json") {
		t.Fatalf("dry-run output = %q", dry)
	}
	if !strings.Contains(dry, "dry-run: pass --yes to delete") {
		t.Fatalf("dry-run output missing hint = %q", dry)
	}
	if _, err := os.Stat(oldDump); err != nil {
		t.Fatalf("dry-run removed dump: %v", err)
	}

	yes := run("dumps", "--older-than", "24h", "--yes")
	if !strings.Contains(yes, "removed 1 items, total ") {
		t.Fatalf("--yes output = %q", yes)
	}
	if _, err := os.Stat(oldDump); !os.IsNotExist(err) {
		t.Fatalf("over-age dump still exists err=%v", err)
	}
	if _, err := os.Stat(freshDump); err != nil {
		t.Fatalf("fresh dump must be kept: %v", err)
	}
	if _, err := os.Stat(dumpsDir); err != nil {
		t.Fatalf("dump dir must be kept: %v", err)
	}
}
