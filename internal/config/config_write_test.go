package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteConfigFileAtomicallyCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("providers:\n  openai:\n    type: responses\n")
	if err := WriteConfigFileAtomically(path, data, 0o644); err != nil {
		t.Fatalf("WriteConfigFileAtomically: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content = %q, want %q", got, data)
	}
}

func TestLockConfigMutationContextStopsWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	first, err := LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation: %v", err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = LockConfigMutationContext(ctx, path)
	if err == nil {
		t.Fatal("LockConfigMutationContext acquired a held lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockConfigMutationContext error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context cancellation took too long: %v", elapsed)
	}
}

func TestLockConfigMutationContextRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := LockConfigMutationContext(ctx, filepath.Join(t.TempDir(), "config.yaml"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LockConfigMutationContext error = %v, want context canceled", err)
	}
}

func TestWriteConfigFileAtomicallyRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WriteConfigFileAtomically(path, []byte("new"), 0o644); err == nil {
		t.Fatal("expected existing file write to fail")
	} else if !os.IsExist(err) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestLockConfigMutationCloseRemovesLockFileAndAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	lockPath := path + ".lock"

	lock, err := LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock file to exist while held: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock file to be removed after close, got %v", err)
	}

	lock, err = LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation reacquire: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close reacquired lock: %v", err)
	}
}
