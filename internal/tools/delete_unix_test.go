//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveFileOrSymlinkDoesNotRemoveDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "victim")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := removeFileOrSymlink(dir); err == nil {
		t.Fatal("expected directory removal to fail")
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		t.Fatalf("directory should remain after failed removal, info=%v err=%v", info, err)
	}
}
