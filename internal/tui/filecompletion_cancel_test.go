package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAtMentionFallbackWalkHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if files := loadAtMentionWalkFilesInDir(ctx, dir, atMentionMaxFiles); len(files) != 0 {
		t.Fatalf("cancelled scan returned files: %v", files)
	}
}
