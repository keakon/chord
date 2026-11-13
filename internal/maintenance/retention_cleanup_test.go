package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func TestCleanupLLMDumpsPreservesOpenTempFile(t *testing.T) {
	loc := testLocator(t)
	dir := filepath.Join(loc.SessionsRoot, "project", "session", "dumps", "llm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("partial dump")); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	// Even old temporary files are not evidence of an abandoned writer.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(f.Name(), old, old); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	result, err := CleanupLLMDumps(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("deleted an unpublished dump: %+v", result.Deleted)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "0001_sample.json")); err != nil {
		t.Fatalf("publish dump after cleanup: %v", err)
	}
}

func TestCleanupLLMDumpsDoesNotFollowDirectorySymlink(t *testing.T) {
	loc := testLocator(t)
	project, err := loc.EnsureProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(project.ProjectSessionsDir, "20260427153042123")
	dumpsDir := filepath.Join(sessionDir, "dumps")
	if err := os.MkdirAll(dumpsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	externalDump := filepath.Join(outside, "old.json")
	if err := os.WriteFile(externalDump, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(externalDump, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dumpsDir, "llm")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := CleanupLLMDumps(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("cleanup result = %#v, want symlink skipped", result)
	}
	if _, err := os.Stat(externalDump); err != nil {
		t.Fatalf("external dump must be preserved: %v", err)
	}
}

func TestCleanupSessionsSkipsInvalidActivityFile(t *testing.T) {
	loc := testLocator(t)
	dir := filepath.Join(loc.SessionsRoot, "project", "session")
	if err := os.MkdirAll(filepath.Join(dir, "main.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupSessions(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("invalid activity must skip session: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupSessionsRemovesFreshTombstone(t *testing.T) {
	loc := testLocator(t)
	dir := filepath.Join(loc.SessionsRoot, "project", "session"+recovery.SessionDeletingSuffix)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupSessions(loc, CleanupOptions{Yes: true, OlderThan: 30 * 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].Path != dir {
		t.Fatalf("tombstone retained: %+v", result)
	}
}

func TestCleanupSessionsDoesNotRenewEmptySession(t *testing.T) {
	loc := testLocator(t)
	dir := filepath.Join(loc.SessionsRoot, "project", "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupSessions(loc, CleanupOptions{Yes: true, OlderThan: 24 * time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("guard creation renewed session age: %+v", result)
	}
}
