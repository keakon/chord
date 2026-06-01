package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBackupManagerPrunesPerPathInCreationOrder(t *testing.T) {
	dir := t.TempDir()
	mgr := newFileBackupManager(dir)
	path := filepath.Join(dir, "target.txt")
	for i := 0; i < maxToolBackupsPerPath+2; i++ {
		if _, err := mgr.Backup(path, "ApplyPatch", []byte(fmt.Sprintf("backup-%02d", i))); err != nil {
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
	_, err := mgr.Backup(path, "ApplyPatch", make([]byte, maxToolBackupBytesPerSession+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds the backup size limit") {
		t.Fatalf("Backup huge error = %v, want single-file size-limit error", err)
	}
}
