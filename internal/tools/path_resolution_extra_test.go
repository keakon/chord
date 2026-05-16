package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveExistingToolPathRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	resolved, info, err := resolveExistingToolPath(path, PathTargetRegularFile, "read")
	if err != nil {
		t.Fatalf("resolveExistingToolPath: %v", err)
	}
	if resolved == "" || info == nil || !info.Mode().IsRegular() {
		t.Fatalf("unexpected result: resolved=%q info=%v", resolved, info)
	}
}

func TestResolveExistingToolPathDirectory(t *testing.T) {
	dir := t.TempDir()
	resolved, info, err := resolveExistingToolPath(dir, PathTargetDirectory, "search")
	if err != nil {
		t.Fatalf("resolveExistingToolPath: %v", err)
	}
	if resolved == "" || info == nil || !info.IsDir() {
		t.Fatalf("unexpected result: resolved=%q info=%v", resolved, info)
	}
}

func TestResolveExistingToolPathRejectsWrongKind(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := resolveExistingToolPath(dir, PathTargetRegularFile, "read"); err == nil {
		t.Fatal("expected directory to be rejected as regular file")
	}
}

func TestResolveExistingToolPathRejectsBlockedDevice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("blocked device paths are unix-specific")
	}
	if _, _, err := resolveExistingToolPath("/dev/stdin", PathTargetAny, "search"); err == nil {
		t.Fatal("expected blocked device path to be rejected")
	}
}
