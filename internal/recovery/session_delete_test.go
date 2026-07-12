package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteSessionByIDRemovesUnlockedNonCurrentSession(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	currentDir := filepath.Join(sessionsDir, "2000")
	targetDir := filepath.Join(sessionsDir, "1000")
	if err := os.MkdirAll(filepath.Join(currentDir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir current session: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetDir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir target session: %v", err)
	}
	mainPath := filepath.Join(targetDir, "main.jsonl")
	if err := os.WriteFile(mainPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write target main.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "images", "a.png"), []byte("img"), 0o600); err != nil {
		t.Fatalf("write target asset: %v", err)
	}
	info, err := os.Stat(mainPath)
	if err != nil {
		t.Fatalf("stat target main.jsonl: %v", err)
	}
	messageCountCache.Store(mainPath, messageCountEntry{size: info.Size(), modTime: info.ModTime(), count: 1})
	t.Cleanup(func() { messageCountCache.Delete(mainPath) })

	if err := DeleteSessionByID(sessionsDir, currentDir, "1000"); err != nil {
		t.Fatalf("DeleteSessionByID: %v", err)
	}
	if _, err := os.Stat(targetDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target session still exists, stat err = %v", err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("current session unexpectedly missing: %v", err)
	}
	if _, ok := messageCountCache.Load(mainPath); ok {
		t.Fatal("deleted session message count remains cached")
	}
}

func TestDeleteSessionByIDRejectsCurrentSession(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	currentDir := filepath.Join(sessionsDir, "2000")
	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		t.Fatalf("mkdir current session: %v", err)
	}

	err := DeleteSessionByID(sessionsDir, currentDir, "2000")
	if !errors.Is(err, ErrDeleteCurrentSession) {
		t.Fatalf("DeleteSessionByID(current) err = %v, want ErrDeleteCurrentSession", err)
	}
}

func TestDeleteSessionByIDRejectsLockedSession(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	targetDir := filepath.Join(sessionsDir, "1000")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target session: %v", err)
	}
	lock, err := AcquireSessionLock(targetDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	err = DeleteSessionByID(sessionsDir, filepath.Join(sessionsDir, "2000"), "1000")
	if _, ok := errors.AsType[*SessionLockedError](err); !ok {
		t.Fatalf("DeleteSessionByID(locked) err = %T %v, want SessionLockedError", err, err)
	}
}
