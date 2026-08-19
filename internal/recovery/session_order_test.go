package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
)

// writeSessionDir creates a session directory with a non-empty main.jsonl.
func writeSessionDir(t *testing.T, sessionsDir, sid, firstUser string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte(`{"role":"user","content":"`+firstUser+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func setSessionMainModTime(t *testing.T, sessionDir string, modTime time.Time) {
	t.Helper()
	mainPath := filepath.Join(sessionDir, "main.jsonl")
	if err := os.Chtimes(mainPath, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// writeCachedSessionActivity records cached activity at lastUpdatedAt. Ordering
// reads the file's modification time rather than parsing it, so the fixture sets
// both and stays honest about what the code actually observes.
func writeCachedSessionActivity(t *testing.T, sessionDir string, lastUpdatedAt time.Time) {
	t.Helper()
	data, err := json.Marshal(analytics.SessionUsageSummary{LastUpdatedAt: lastUpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(sessionDir, analytics.SessionUsageSummaryFileName)
	if err := os.WriteFile(summaryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(summaryPath, lastUpdatedAt, lastUpdatedAt); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOrderUsesLastActivityTime(t *testing.T) {
	sessionsDir := t.TempDir()
	olderID := "20261031120000123"
	newerID := "20261031130000456"
	olderSession := writeSessionDir(t, sessionsDir, olderID, "older id, active recently")
	newerSession := writeSessionDir(t, sessionsDir, newerID, "newer id, inactive")
	now := time.Now()
	setSessionMainModTime(t, olderSession, now.Add(-time.Minute))
	setSessionMainModTime(t, newerSession, now.Add(-time.Hour))

	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].ID != olderID || list[1].ID != newerID {
		t.Fatalf("order = %q, %q; want recently active %q first", list[0].ID, list[1].ID, olderID)
	}
	if got := FindMostRecentSession(sessionsDir, ""); got != olderSession {
		t.Fatalf("FindMostRecentSession = %q, want %q", got, olderSession)
	}
}

func TestSessionOrderUsesCachedActivityNewerThanTranscript(t *testing.T) {
	sessionsDir := t.TempDir()
	activeID := "20261031120000123"
	recentTranscriptID := "20261031130000456"
	activeSession := writeSessionDir(t, sessionsDir, activeID, "active through usage")
	recentTranscript := writeSessionDir(t, sessionsDir, recentTranscriptID, "recent transcript")
	now := time.Now()
	setSessionMainModTime(t, activeSession, now.Add(-2*time.Hour))
	setSessionMainModTime(t, recentTranscript, now.Add(-time.Hour))
	writeCachedSessionActivity(t, activeSession, now.Add(-time.Minute))

	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].ID != activeID {
		t.Fatalf("first = %q, want recently active session %q", list[0].ID, activeID)
	}
	if got := FindMostRecentSession(sessionsDir, ""); got != activeSession {
		t.Fatalf("FindMostRecentSession = %q, want %q", got, activeSession)
	}
}

// Equal activity timestamps are routine: `cp -p`/`rsync -a` preserve mtimes, and
// filesystems with one-second mtime granularity (FAT/exFAT, some SMB and NFS
// mounts) collapse nearby writes onto the same value. The tie must then be
// broken deterministically and identically by both the list and --continue.
func TestSessionOrderTieBreaksOnDescendingIDDeterministically(t *testing.T) {
	sessionsDir := t.TempDir()
	lowerID := "20261031120000123"
	higherID := "20261031130000456"
	lowerSession := writeSessionDir(t, sessionsDir, lowerID, "lower id")
	higherSession := writeSessionDir(t, sessionsDir, higherID, "higher id")
	same := time.Now().Add(-time.Hour).Truncate(time.Second)
	setSessionMainModTime(t, lowerSession, same)
	setSessionMainModTime(t, higherSession, same)

	for range 5 {
		list, err := ListSessions(sessionsDir, "")
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("len(list) = %d, want 2", len(list))
		}
		if list[0].ID != higherID || list[1].ID != lowerID {
			t.Fatalf("order = %q, %q; want descending ID %q first on equal activity", list[0].ID, list[1].ID, higherID)
		}
		if got := FindMostRecentSession(sessionsDir, ""); got != higherSession {
			t.Fatalf("FindMostRecentSession = %q, want %q (must match the list's first row)", got, higherSession)
		}
	}
}

// A session another live process owns cannot be opened, so it is not a
// candidate for --continue or in-app /resume even when it is the most recent.
// It still appears in the list, marked Locked, for the picker to render.
func TestFindMostRecentSessionSkipsSessionOwnedByAnotherProcess(t *testing.T) {
	sessionsDir := t.TempDir()
	idleID := "20261031120000123"
	busyID := "20261031130000456"
	idleSession := writeSessionDir(t, sessionsDir, idleID, "idle")
	busySession := writeSessionDir(t, sessionsDir, busyID, "busy")
	now := time.Now()
	setSessionMainModTime(t, idleSession, now.Add(-time.Hour))
	setSessionMainModTime(t, busySession, now.Add(-time.Minute))

	lock, err := AcquireSessionLock(busySession)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer lock.Release()

	if got := RecentSessionCandidates(sessionsDir, ""); len(got) != 2 || got[0] != busySession {
		t.Fatalf("RecentSessionCandidates = %v, want the busy session ordered first", got)
	}
	if got := FindMostRecentSession(sessionsDir, ""); got != idleSession {
		t.Fatalf("FindMostRecentSession = %q, want %q (the busy session is owned elsewhere)", got, idleSession)
	}
	list, err := ListSessions(sessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 || list[0].ID != busyID || !list[0].Locked {
		t.Fatalf("list = %+v, want the busy session listed first and marked Locked", list)
	}
}

func TestCachedSessionPreviewIgnoresLargeAggregatePayloads(t *testing.T) {
	sessionDir := t.TempDir()
	want := time.Now().Round(0)
	data := []byte(`{"last_updated_at":"` + want.Format(time.RFC3339Nano) + `","first_user_message":"hello","by_model_ref":{"provider/model":{"llm_calls":1}},"unknown_future_field":{"nested":true}}`)
	if err := os.WriteFile(filepath.Join(sessionDir, "usage-summary.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	first, compacted, original, got := cachedSessionPreview(sessionDir)
	if first != "hello" || compacted || original != "" || !got.Equal(want) {
		t.Fatalf("cachedSessionPreview = (%q, %t, %q, %v), want metadata only", first, compacted, original, got)
	}
}
