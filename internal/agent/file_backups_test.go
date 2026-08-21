package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestFileBackupManagerPrunesPerPathInCreationOrder(t *testing.T) {
	dir := t.TempDir()
	mgr := newFileBackupManager(dir)
	path := filepath.Join(dir, "target.txt")
	for i := range maxToolBackupsPerPath + 2 {
		if _, err := mgr.Backup(path, "Edit", fmt.Appendf(nil, "backup-%02d", i)); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
	}
	key := shortPathHash(normalizeAgentFilePath(path))
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

func TestFileBackupManagerRestrictsNewDirectoriesOnly(t *testing.T) {
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

	// Pre-existing session dirs keep their permissions; only the newly created
	// sequence directory and backup file get the private modes.
	assertAgentMode(t, dir, 0o755)
	assertAgentMode(t, backupsDir, 0o755)
	assertAgentMode(t, filepath.Dir(record.Path), 0o700)
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
	for i := range maxToolBackupsPerSession {
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

// A backup is best effort, so the model and the user must be told the same
// thing: both get the location when one exists, and neither is told a net was
// in place when it was not.
func TestBackupNotesAreIdenticalForModelAndUser(t *testing.T) {
	backupPath := filepath.Join(t.TempDir(), "backups", "before.txt")
	created := appendBackupNotes("updated", tools.NameEdit, true, 1, fileBackupOutcome{
		Records: []fileBackupRecord{{Path: backupPath}},
	})
	if !strings.Contains(created, "Backup saved to: "+backupPath) {
		t.Fatalf("result missing backup location: %q", created)
	}
	if got := backupPathsFromResult(created); len(got) != 1 || got[0] != backupPath {
		t.Fatalf("backupPathsFromResult = %#v, want [%q]", got, backupPath)
	}

	failed := appendBackupNotes("updated", tools.NameEdit, true, 1, fileBackupOutcome{})
	if strings.Contains(failed, "Backup") {
		t.Fatalf("a failed backup must not claim anything about a backup: %q", failed)
	}
	if !strings.Contains(failed, "changed on disk") {
		t.Fatalf("result missing the drift warning: %q", failed)
	}
}

// write replaces the whole file with no anchors to re-check, so its warning
// must not borrow edit/apply_patch's "validated current contents" wording.
func TestWriteBackupNoteDoesNotClaimValidation(t *testing.T) {
	note := appendBackupNotes("wrote 1 line", tools.NameWrite, true, 1, fileBackupOutcome{})
	if strings.Contains(note, "validated") {
		t.Fatalf("write drift warning claims validation: %q", note)
	}
	if !strings.Contains(note, "replaced by this write") {
		t.Fatalf("write drift warning does not say the contents were replaced: %q", note)
	}
	edit := appendBackupNotes("edited", tools.NameEdit, true, 1, fileBackupOutcome{})
	if !strings.Contains(edit, "validated current contents") {
		t.Fatalf("edit drift warning lost its validation wording: %q", edit)
	}
}

// backupPathsFromResult extracts the backup locations a tool result reports.
func backupPathsFromResult(result string) []string {
	var paths []string
	for _, line := range strings.Split(result, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Backup saved to: "); ok {
			paths = append(paths, rest)
		}
	}
	return paths
}

// A stale write whose path is a symlink must not copy the link target into the
// session backup directory: write refuses to follow symlinks, so the tool call
// mutates nothing, and the target may live outside the project entirely.
func TestReadPreWriteBytesSkipsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is platform-specific")
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	data, existed, err := readPreWriteBytes(link)
	if err != nil {
		t.Fatalf("readPreWriteBytes(symlink): %v", err)
	}
	if existed || len(data) != 0 {
		t.Fatalf("symlink reported as backup source: existed=%v data=%q", existed, data)
	}

	data, existed, err = readPreWriteBytes(outside)
	if err != nil || !existed || string(data) != "private" {
		t.Fatalf("readPreWriteBytes(regular) = %q, %v, %v", data, existed, err)
	}
}

// A new manager for the same session directory must observe the backups the
// previous owner wrote: the sequence counter continues (never overwriting an
// existing file), and the per-path quota counts the historical files.
func TestFileBackupManagerReindexesExistingBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")

	first := newFileBackupManager(dir)
	for i := range 3 {
		if _, err := first.Backup(path, "Edit", fmt.Appendf(nil, "backup-%02d", i)); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
	}
	firstSeq := first.seq

	second := newFileBackupManager(dir)
	if second.seq != firstSeq {
		t.Fatalf("reindexed seq = %d, want %d from disk", second.seq, firstSeq)
	}
	record, err := second.Backup(path, "Edit", []byte("next"))
	if err != nil {
		t.Fatalf("Backup after reindex: %v", err)
	}
	seq, ok := backupSequenceFromName(filepath.Base(record.Path))
	if !ok || seq <= firstSeq {
		t.Fatalf("new backup seq = %d, want > %d so existing files are never overwritten", seq, firstSeq)
	}

	// The reindexed manager enforces the per-path cap across the boundary:
	// 3 existing + 9 new = 12, so the 2 oldest are pruned and 10 remain.
	for i := range 8 {
		if _, err := second.Backup(path, "Edit", fmt.Appendf(nil, "more-%02d", i)); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
	}
	key := shortPathHash(normalizeAgentFilePath(path))
	if got := len(second.byPath[key]); got != maxToolBackupsPerPath {
		t.Fatalf("backups for path = %d, want %d", got, maxToolBackupsPerPath)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "backups", key, "*-before-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != maxToolBackupsPerPath {
		t.Fatalf("backup files on disk = %d, want %d", len(matches), maxToolBackupsPerPath)
	}
}

// The session-level file limit must survive a process restart: a fresh manager
// that reindexes a full session rejects the next backup instead of silently
// growing past the cap.
func TestFileBackupManagerReindexesSessionFileLimit(t *testing.T) {
	dir := t.TempDir()
	first := newFileBackupManager(dir)
	for i := range maxToolBackupsPerSession {
		path := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i))
		if _, err := first.Backup(path, "Delete", []byte("x")); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
	}

	second := newFileBackupManager(dir)
	if _, err := second.Backup(filepath.Join(dir, "overflow.txt"), "Delete", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "session backup file limit") {
		t.Fatalf("reindexed overflow error = %v, want session file-limit error", err)
	}
}

// SetSessionDir must reindex the target directory, so switching sessions
// starts from that session's existing backups instead of the source session's.
func TestFileBackupManagerSetSessionDirReindexes(t *testing.T) {
	root := t.TempDir()
	sessionA := filepath.Join(root, "a")
	sessionB := filepath.Join(root, "b")
	pathA := filepath.Join(root, "target.txt")
	pathB := filepath.Join(root, "target.txt")

	mgr := newFileBackupManager(sessionA)
	if _, err := mgr.Backup(pathA, "Edit", []byte("a")); err != nil {
		t.Fatalf("Backup A: %v", err)
	}

	other := newFileBackupManager(sessionB)
	if _, err := other.Backup(pathB, "Edit", []byte("b")); err != nil {
		t.Fatalf("Backup B: %v", err)
	}

	mgr.SetSessionDir(sessionB)
	if got := len(mgr.byPath); got != 1 {
		t.Fatalf("reindexed byPath = %d groups, want 1 (session B only)", got)
	}
	// A second backup in session B must be rejected only when session B's own
	// cap is exhausted, not session A's: fill B to the cap via the reindexed
	// manager and verify the boundary.
	if _, err := mgr.Backup(filepath.Join(root, "another.txt"), "Edit", []byte("x")); err != nil {
		t.Fatalf("Backup after switch: %v", err)
	}
	if got := len(mgr.byPath); got != 2 {
		t.Fatalf("reindexed byPath = %d groups, want 2 after a new path", got)
	}
}
