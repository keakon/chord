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

// A config file the user deliberately installed as a symlink — a dotfiles
// repository or a shared checkout beside the project — has to stay a symlink.
// Renaming a temp file over it would replace the link with a regular file and
// leave the real source stale, silently undoing that choice.
func TestCreateOrUpdateConfigFileLockedWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("providers: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CreateOrUpdateConfigFileLocked(link, func(current []byte) ([]byte, error) {
		return append(current, []byte("# added\n")...), nil
	}); err != nil {
		t.Fatalf("CreateOrUpdateConfigFileLocked: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink was replaced by a regular file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if string(got) != "providers: {}\n# added\n" {
		t.Fatalf("symlink target = %q, want the appended content", got)
	}
}

// A dangling link still names the file the user meant to keep, so the write
// recreates that file rather than replacing the link with a local copy.
func TestCreateOrUpdateConfigFileLockedWritesThroughDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared", "config.yaml")
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := CreateOrUpdateConfigFileLocked(link, func([]byte) ([]byte, error) {
		return []byte("providers: {}\n"), nil
	}); err != nil {
		t.Fatalf("CreateOrUpdateConfigFileLocked: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dangling config symlink was replaced by a regular file")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected the link target to be created: %v", err)
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

func TestLockConfigMutationClosePreservesLockFileAndAllowsReacquire(t *testing.T) {
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
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected stable lock file after close, got %v", err)
	}

	lock, err = LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation reacquire: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close reacquired lock: %v", err)
	}
}
