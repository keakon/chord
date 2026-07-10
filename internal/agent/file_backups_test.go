package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileBackupManagerPrunesPerPathInCreationOrder(t *testing.T) {
	dir := t.TempDir()
	mgr := newFileBackupManager(dir)
	path := filepath.Join(dir, "target.txt")
	for i := 0; i < maxToolBackupsPerPath+2; i++ {
		if _, err := mgr.Backup(path, "Edit", []byte(fmt.Sprintf("backup-%02d", i))); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
	}
	key := normalizeAgentFilePath(path)
	got := mgr.byPath[key]
	if len(got) != maxToolBackupsPerPath {
		t.Fatalf("backup count = %d, want %d", len(got), maxToolBackupsPerPath)
	}
	if strings.Contains(filepath.Base(got[0]), "000000000001") || strings.Contains(filepath.Base(got[0]), "000000000002") {
		t.Fatalf("oldest backups were not pruned in creation order: %#v", got)
	}
	for _, removedSeq := range []string{"000000000001", "000000000002"} {
		matches, err := filepath.Glob(filepath.Join(dir, "backups", "*", removedSeq+"-*"))
		if err != nil {
			t.Fatalf("Glob: %v", err)
		}
		if len(matches) != 0 {
			t.Fatalf("removed backup sequence %s still exists: %#v", removedSeq, matches)
		}
	}
}

func TestFileBackupManagerRestrictsExistingSessionHierarchy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "session")
	backupsDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := newFileBackupManager(dir)
	record, err := mgr.Backup("secret.txt", "Edit", []byte("secret"))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	for _, path := range []string{dir, backupsDir, filepath.Dir(record.Path)} {
		assertAgentMode(t, path, 0o700)
	}
	assertAgentMode(t, record.Path, 0o600)
}

func assertAgentMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
	}
}

func TestFileBackupManagerRejectsSingleLargeBackup(t *testing.T) {
	mgr := newFileBackupManager(t.TempDir())
	_, err := mgr.Backup("large.txt", "Write", make([]byte, maxSingleToolBackupBytes+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds the backup size limit") || strings.Contains(err.Error(), "No files were modified") {
		t.Fatalf("Backup error = %v, want size-limit error", err)
	}
}

func TestFileBackupManagerRejectsSessionFileLimit(t *testing.T) {
	dir := t.TempDir()
	mgr := newFileBackupManager(dir)
	for i := 0; i < maxToolBackupsPerSession; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i))
		if _, err := mgr.Backup(path, "Delete", []byte("x")); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
	}
	_, err := mgr.Backup(filepath.Join(dir, "overflow.txt"), "Delete", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "session backup file limit") || strings.Contains(err.Error(), "No files were modified") {
		t.Fatalf("Backup overflow error = %v, want session file-limit error", err)
	}
}

func TestFileBackupManagerRejectsSessionByteLimit(t *testing.T) {
	dir := t.TempDir()
	mgr := newFileBackupManager(dir)
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := mgr.Backup(path, "Edit", make([]byte, maxToolBackupBytesPerSession+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds the backup size limit") {
		t.Fatalf("Backup huge error = %v, want single-file size-limit error", err)
	}
}
