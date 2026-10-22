package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncSkipsNonDurableSessionDirs(t *testing.T) {
	r, dir := newTestManager(t)
	defer r.Close()
	for _, name := range []string{"dumps", "traces", "job-logs"} {
		sub := filepath.Join(dir, name, "nested")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(sub, "escape")); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Sync(context.Background()); err != nil {
		t.Fatalf("Sync rejected diagnostic session dirs: %v", err)
	}
}

// Only a directory directly under the session root is skipped: one nested
// deeper that happens to share a diagnostic name is still part of the tree the
// barrier covers, so a symlink inside it still fails the barrier.
func TestSyncCoversNestedDirsSharingADiagnosticName(t *testing.T) {
	r, dir := newTestManager(t)
	defer r.Close()
	nested := filepath.Join(dir, "images", "dumps")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(nested, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("Sync skipped a nested directory that only shares a diagnostic name")
	}
}

func TestSyncRejectsSymlinksInRecoveryPaths(t *testing.T) {
	r, dir := newTestManager(t)
	defer r.Close()
	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(images, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("Sync accepted symlink in recovery path")
	}
}

func TestCloseLeavesSessionTreeToSync(t *testing.T) {
	r, dir := newTestManager(t)
	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(images, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close walked the session tree: %v", err)
	}
}

func TestSyncHonorsCanceledContext(t *testing.T) {
	r, _ := newTestManager(t)
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync error = %v", err)
	}
}
