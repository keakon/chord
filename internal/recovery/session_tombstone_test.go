package recovery

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsDeletingSessionDirName(t *testing.T) {
	cases := map[string]bool{
		"20260427153042123":          false,
		"20260427153042123.deleting": true,
		"deleting":                   false,
		"my.deleting":                true,
		"":                           false,
	}
	for name, want := range cases {
		if got := IsDeletingSessionDirName(name); got != want {
			t.Fatalf("IsDeletingSessionDirName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRecentSessionCandidatesAndListSessionsRejectTombstones(t *testing.T) {
	sessionsDir := t.TempDir()
	live := writeSessionWithTranscript(t, sessionsDir, "20260427153042123")
	tombstone := writeSessionWithTranscript(t, sessionsDir, "20260427153042124.deleting")
	// Rename the tombstone back and forth so its main.jsonl is present but the
	// directory name marks it as deleted.
	if err := os.Rename(tombstone, tombstone+".x"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tombstone+".x", tombstone); err != nil {
		t.Fatal(err)
	}

	candidates := RecentSessionCandidates(sessionsDir, "")
	if len(candidates) != 1 || candidates[0] != live {
		t.Fatalf("candidates = %v, want only %s", candidates, live)
	}
	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Path != live {
		t.Fatalf("list = %#v, want only %s", list, live)
	}
}

func TestAcquireExistingSessionLockRefusesMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "20260427153042123")
	_, err := AcquireExistingSessionLock(missing)
	if err == nil {
		t.Fatal("expected error for missing session dir")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want fs.ErrNotExist", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing dir must not be created, stat err=%v", err)
	}
}

func TestAcquireExistingSessionLockWorksOnPresentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "20260427153042123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireExistingSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := AcquireExistingSessionLock(dir); err == nil {
		t.Fatal("expected locked error for second acquisition")
	}
}

func TestDeleteSessionDirRenamesThenRemoves(t *testing.T) {
	dir := writeSessionWithTranscript(t, t.TempDir(), "20260427153042123")
	mainPath := filepath.Join(dir, "main.jsonl")

	if err := DeleteSessionDir(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists err=%v", err)
	}
	if _, err := os.Stat(dir + SessionDeletingSuffix); !os.IsNotExist(err) {
		t.Fatalf("tombstone still exists err=%v", err)
	}
	if _, err := os.Stat(mainPath); !os.IsNotExist(err) {
		t.Fatalf("transcript still exists err=%v", err)
	}
}

func TestDeleteSessionTombstone(t *testing.T) {
	root := t.TempDir()
	dir := writeSessionWithTranscript(t, root, "20260427153042123")
	tombstone := dir + SessionDeletingSuffix
	if err := os.Rename(dir, tombstone); err != nil {
		t.Fatal(err)
	}

	if err := DeleteSessionDir(tombstone, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("tombstone still exists err=%v", err)
	}
}

func TestDeleteSessionByIDRejectsTombstoneName(t *testing.T) {
	sessionsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionsDir, "20260427153042123.deleting"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSessionByID(sessionsDir, "", "20260427153042123.deleting"); err == nil {
		t.Fatal("expected refusal to delete a tombstone by ID")
	}
}

func TestSessionActivityTimeMatchesTranscriptPresence(t *testing.T) {
	dir := writeSessionWithTranscript(t, t.TempDir(), "20260427153042123")
	age, ok, err := SessionActivityTime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("session with transcript must report activity")
	}
	if age.IsZero() {
		t.Fatal("activity time must not be zero")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := SessionActivityTime(empty); ok || err != nil {
		t.Fatalf("empty dir activity=%v err=%v", ok, err)
	}
}

// writeSessionWithTranscript creates one session dir with a non-empty
// main.jsonl and returns its full path.
func writeSessionWithTranscript(t *testing.T, sessionsDir, id string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.jsonl")
	if err := os.WriteFile(mainPath, []byte(`{"v":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Distinct mtimes keep the activity ordering deterministic when a test
	// creates several sessions.
	when := time.Now().Add(-time.Duration(len(id)%7+1) * time.Minute)
	if err := os.Chtimes(mainPath, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}
