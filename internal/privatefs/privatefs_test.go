package privatefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureDirRestrictsOnlyNewDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	root := filepath.Join(t.TempDir(), "session")
	target := filepath.Join(root, "artifacts", "subagents", "agent-1")
	// Everything except the final component pre-exists with a broader mode.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := EnsureDir(root, target); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	// Pre-existing directories keep their permissions.
	assertMode(t, root, 0o755)
	assertMode(t, filepath.Join(root, "artifacts"), 0o755)
	assertMode(t, filepath.Join(root, "artifacts", "subagents"), 0o755)
	// Only the newly created component is restricted.
	assertMode(t, target, DirMode)

	// A second call must not touch any permissions, including the new one.
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(root, target); err != nil {
		t.Fatalf("EnsureDir second call: %v", err)
	}
	assertMode(t, target, 0o755)
}

func TestEnsureDirRejectsPathOutsideRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "session")
	outside := filepath.Join(base, "other")
	if err := EnsureDir(root, outside); err == nil {
		t.Fatal("EnsureDir accepted path outside root")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root should not be created on rejection: %v", err)
	}
}

func TestEnsureDirRejectsRootSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows systems")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	root := filepath.Join(base, "session")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}

	if err := EnsureDir(root, root); err == nil {
		t.Fatal("EnsureDir accepted a symbolic-link root")
	}
	assertMode(t, target, 0o755)
}

func TestWriteFilePreservesExistingFileAndDirModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	root := filepath.Join(t.TempDir(), "session")
	path := filepath.Join(root, "secret.json")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(root, path, []byte("new")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A pre-existing root and file keep their permissions; WriteFile only
	// restricts the directories and files it actually creates.
	assertMode(t, root, 0o755)
	assertMode(t, path, 0o644)

	// A newly created file under the same root gets the private mode.
	other := filepath.Join(root, "new.json")
	if err := WriteFile(root, other, []byte("x")); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	assertMode(t, other, FileMode)
}

func TestWriteFileSyncedWritesPrivateFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session")
	path := filepath.Join(root, "state.json")
	if err := WriteFileSynced(root, path, []byte("current")); err != nil {
		t.Fatalf("WriteFileSynced: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "current" {
		t.Fatalf("content = %q, want current", data)
	}
	assertMode(t, root, DirMode)
	assertMode(t, path, FileMode)
	if err := SyncDir(root); err != nil {
		t.Fatalf("SyncDir: %v", err)
	}
}

func TestWriteFileRejectsEscapingDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows systems")
	}
	base := t.TempDir()
	root := filepath.Join(base, "session")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(root, filepath.Join(root, "link", "secret"), []byte("secret")); err == nil {
		t.Fatal("WriteFile followed a directory symlink outside root")
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist: %v", err)
	}
	assertMode(t, outside, 0o755)
}

func TestFileOperationsRejectFinalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links requires elevated privileges on some Windows systems")
	}
	base := t.TempDir()
	root := filepath.Join(base, "session")
	outPath := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "secret")
	if err := os.Symlink(outPath, path); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(root, path, []byte("changed")); err == nil {
		t.Fatal("WriteFile followed a final symlink")
	}
	if f, err := OpenFile(root, path, os.O_WRONLY|os.O_TRUNC); err == nil {
		_ = f.Close()
		t.Fatal("OpenFile followed a final symlink")
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("outside file changed to %q", data)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
	}
}
