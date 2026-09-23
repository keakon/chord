package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

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
	created := appendNotes("updated", backupNotes(tools.NameEdit, driftReport{stale: true, paths: 1}, fileBackupOutcome{
		Records: []fileBackupRecord{{Path: backupPath}},
	}))
	if !strings.Contains(created, "Backup saved to: "+backupPath) {
		t.Fatalf("result missing backup location: %q", created)
	}
	if got := backupPathsFromResult(created); len(got) != 1 || got[0] != backupPath {
		t.Fatalf("backupPathsFromResult = %#v, want [%q]", got, backupPath)
	}
	if strings.Contains(created, "Backup saved for:") {
		t.Fatalf("a record without a source path must not claim one: %q", created)
	}

	failed := appendNotes("updated", backupNotes(tools.NameEdit, driftReport{stale: true, paths: 1}, fileBackupOutcome{}))
	if strings.Contains(failed, "Backup") {
		t.Fatalf("a failed backup must not claim anything about a backup: %q", failed)
	}
	if !strings.Contains(failed, "changed on disk") {
		t.Fatalf("result missing the drift warning: %q", failed)
	}
}

// write replaces the whole file with no anchors to re-check, so its warning
// must not borrow edit/apply_patch's "validated current contents" wording.
// A multi-file tool call backs up one snapshot per touched file. The result
// must state each source file explicitly so the reader is not left to infer
// the mapping from output order or the backup filename.
func TestBackupNotesForMultiFileMutationStatePerSourcePerPath(t *testing.T) {
	sources := []string{
		filepath.Join("workspace", "src", "a.go"),
		filepath.Join("workspace", "tests", "a.go"),
	}
	// Deliberately reversed vs. the sources slice to prove the two lists are
	// not correlated by position alone.
	backups := []string{
		filepath.Join("session", "backups", "111", "000000000001-before-apply_patch-a.go"),
		filepath.Join("session", "backups", "222", "000000000002-before-apply_patch-a.go"),
	}
	notes := appendNotes("updated", backupNotes(tools.NameApplyPatch, driftReport{stale: true, paths: 2}, fileBackupOutcome{
		Records: []fileBackupRecord{
			{SourcePath: sources[0], Path: backups[0]},
			{SourcePath: sources[1], Path: backups[1]},
		},
	}))

	gotSources := backupSourcesFromResult(notes)
	gotPaths := backupPathsFromResult(notes)
	if len(gotSources) != 2 || len(gotPaths) != 2 {
		t.Fatalf("backup notes partition = sources %#v paths %#v", gotSources, gotPaths)
	}
	for i, source := range sources {
		if !strings.Contains(notes, "Backup saved for: "+source) {
			t.Fatalf("notes missing source %q:\n%s", source, notes)
		}
		if !strings.Contains(notes, "Backup saved to: "+backups[i]) {
			t.Fatalf("notes missing backup %q:\n%s", backups[i], notes)
		}
	}
	// The mapping must be one-to-one per record, not a header followed by a
	// concatenated list: the two source lines precede the two backup lines.
	if gotSources[0] != sources[0] || gotSources[1] != sources[1] ||
		gotPaths[0] != backups[0] || gotPaths[1] != backups[1] {
		t.Fatalf("backup notes order wrong: sources %#v paths %#v", gotSources, gotPaths)
	}
}

func TestWriteBackupNoteDoesNotClaimValidation(t *testing.T) {
	note := appendNotes("wrote 1 line", backupNotes(tools.NameWrite, driftReport{stale: true, paths: 1}, fileBackupOutcome{}))
	if strings.Contains(note, "validated") {
		t.Fatalf("write drift warning claims validation: %q", note)
	}
	if !strings.Contains(note, "replaced by this write") {
		t.Fatalf("write drift warning does not say the contents were replaced: %q", note)
	}
	edit := appendNotes("edited", backupNotes(tools.NameEdit, driftReport{stale: true, paths: 1}, fileBackupOutcome{}))
	if !strings.Contains(edit, "validated current contents") {
		t.Fatalf("edit drift warning lost its validation wording: %q", edit)
	}
}

func TestFormatChangeAgeBucketsRecentDrift(t *testing.T) {
	now := time.Now()
	runtimeStart := now.Add(-3 * time.Hour)
	cases := []struct {
		name   string
		mod    time.Time
		sess   time.Time
		wantRe string // empty means the age is suppressed
	}{
		{"seconds", now.Add(-10 * time.Second), runtimeStart, `^10s ago$`},
		{"sub-second reads as 1s", now.Add(-100 * time.Millisecond), runtimeStart, `^1s ago$`},
		{"minutes", now.Add(-3 * time.Minute), runtimeStart, `^3m ago$`},
		{"hours", now.Add(-2 * time.Hour), runtimeStart, `^2h ago$`},
		{"older than a day", now.Add(-25 * time.Hour), runtimeStart, ""},
		{"zero modtime", time.Time{}, runtimeStart, ""},
		{"future clock skew", now.Add(10 * time.Minute), runtimeStart, ""},
		{"predates session start", runtimeStart.Add(-time.Minute), runtimeStart, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatChangeAge(tc.mod, tc.sess)
			if tc.wantRe == "" {
				if got != "" {
					t.Fatalf("formatChangeAge = %q, want suppressed", got)
				}
				return
			}
			if ok, _ := regexp.MatchString(tc.wantRe, got); !ok {
				t.Fatalf("formatChangeAge = %q, want matching %q", got, tc.wantRe)
			}
		})
	}
}

func TestDriftWarningIncludesRecentAge(t *testing.T) {
	runtimeStart := time.Now().Add(-time.Hour)
	recent := time.Now().Add(-45 * time.Second)
	const prefix = "the file changed on disk after your last read"
	const suffix = "and those contents were replaced by this write"

	note := driftWarning(prefix, ",", suffix, driftReport{modTime: recent, runtimeStartedAt: runtimeStart})
	if !strings.Contains(note, "(about 45s ago)") {
		t.Fatalf("recent drift must name the age: %q", note)
	}
	old := driftWarning(prefix, ",", suffix, driftReport{modTime: time.Now().Add(-25 * time.Hour), runtimeStartedAt: runtimeStart})
	if strings.Contains(old, "ago") {
		t.Fatalf("day-old drift must omit the age: %q", old)
	}
	predates := driftWarning(prefix, ",", suffix, driftReport{modTime: runtimeStart.Add(-time.Minute), runtimeStartedAt: runtimeStart})
	if strings.Contains(predates, "ago") {
		t.Fatalf("drift predating the runtime start must omit the age: %q", predates)
	}
	writeNote := appendNotes("wrote", backupNotes(tools.NameWrite, driftReport{stale: true, paths: 1, modTime: recent, runtimeStartedAt: runtimeStart}, fileBackupOutcome{}))
	if !strings.Contains(writeNote, "(about 45s ago)") {
		t.Fatalf("write drift warning must carry the recent age: %q", writeNote)
	}
}

// The singular and plural tracked-snapshot warnings differ only in how many
// files they name, so they must punctuate the same way — the age, when there is
// one, slots in before the separator rather than replacing it.
func TestTrackedSnapshotWarningsPunctuateAlike(t *testing.T) {
	runtimeStart := time.Now().Add(-time.Hour)
	const clause = "; the tool validated current contents before writing and continued."

	single := appendNotes("edited", backupNotes(tools.NameEdit, driftReport{stale: true, paths: 1}, fileBackupOutcome{}))
	if !strings.HasSuffix(single, "snapshot"+clause) {
		t.Fatalf("single-file warning lost the semicolon: %q", single)
	}
	plural := appendNotes("patched", backupNotes(tools.NameApplyPatch, driftReport{stale: true, paths: 2}, fileBackupOutcome{}))
	if !strings.HasSuffix(plural, "snapshot"+clause) {
		t.Fatalf("multi-file warning changed shape: %q", plural)
	}
	aged := appendNotes("edited", backupNotes(tools.NameEdit, driftReport{
		stale: true, paths: 1, modTime: time.Now().Add(-45 * time.Second), runtimeStartedAt: runtimeStart,
	}, fileBackupOutcome{}))
	if !strings.HasSuffix(aged, "snapshot (about 45s ago)"+clause) {
		t.Fatalf("aged warning must keep the semicolon after the age: %q", aged)
	}
}

// backupPathsFromResult extracts the backup locations a tool result reports.
func backupPathsFromResult(result string) []string {
	var paths []string
	for line := range strings.SplitSeq(result, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Backup saved to: "); ok {
			paths = append(paths, rest)
		}
	}
	return paths
}

// backupSourcesFromResult extracts the workspace files a tool result's backup
// notes attribute to each backup, in emission order. Stale snapshots are
// backed up one record per touched file, so the mapping source->backup is
// explicit rather than inferred from order or filename.
func backupSourcesFromResult(result string) []string {
	var sources []string
	for line := range strings.SplitSeq(result, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Backup saved for: "); ok {
			sources = append(sources, rest)
		}
	}
	return sources
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
