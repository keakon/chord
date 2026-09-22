package recovery

import (
	"testing"
	"time"
)

func TestRecordWorktreeBoundarySetsBindingAndAppendsTimeline(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{Title: "keep me", MCPEnabledServers: []string{"manual-search"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	binding := WorktreeBinding{RepoID: "abc123", RepoRoot: "/repo/main", Name: "feat-a", Branch: "chord/feat-a", Path: "/state/wt/feat-a"}
	entry := WorktreeTimelineEntry{
		Reason:     WorktreeSwitchEnter,
		Name:       "feat-a",
		Branch:     "chord/feat-a",
		Path:       "/state/wt/feat-a",
		Head:       "deadbeef",
		Generation: 1,
		At:         time.Now().UTC(),
	}
	if err := RecordWorktreeBoundary(dir, binding, entry); err != nil {
		t.Fatalf("RecordWorktreeBoundary: %v", err)
	}

	got, err := LoadSessionMeta(dir)
	if err != nil || got == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", got, err)
	}
	if got.WorktreeName != "feat-a" || got.WorktreeBranch != "chord/feat-a" || got.WorktreePath != "/state/wt/feat-a" {
		t.Fatalf("binding not recorded: %+v", got)
	}
	if got.RepoID != "abc123" || got.RepoRoot != "/repo/main" {
		t.Fatalf("repository identity not recorded: %+v", got)
	}
	if got.Title != "keep me" || len(got.MCPEnabledServers) != 1 {
		t.Fatalf("boundary write clobbered unrelated metadata: %+v", got)
	}
	if len(got.WorktreeTimeline) != 1 {
		t.Fatalf("timeline = %+v, want one entry", got.WorktreeTimeline)
	}
	if entry := got.WorktreeTimeline[0]; entry.Reason != WorktreeSwitchEnter || entry.Head != "deadbeef" || entry.Generation != 1 {
		t.Fatalf("timeline entry = %+v", entry)
	}
}

func TestRecordWorktreeBoundaryClearsBindingOnExit(t *testing.T) {
	dir := t.TempDir()
	if err := RecordWorktreeBoundary(dir, WorktreeBinding{RepoID: "abc", RepoRoot: "/repo/main", Name: "feat-a", Branch: "chord/feat-a", Path: "/state/wt/feat-a"}, WorktreeTimelineEntry{Reason: WorktreeSwitchEnter, At: time.Now().UTC()}); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := RecordWorktreeBoundary(dir, WorktreeBinding{}, WorktreeTimelineEntry{Reason: WorktreeSwitchExit, Generation: 2, At: time.Now().UTC()}); err != nil {
		t.Fatalf("exit: %v", err)
	}

	got, err := LoadSessionMeta(dir)
	if err != nil || got == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", got, err)
	}
	if got.WorktreeName != "" || got.WorktreeBranch != "" || got.WorktreePath != "" {
		t.Fatalf("exit must clear the recorded worktree: %+v", got)
	}
	if got.RepoID != "abc" || got.RepoRoot != "/repo/main" {
		t.Fatalf("repository identity should survive a worktree exit: %+v", got)
	}
	if len(got.WorktreeTimeline) != 2 || got.WorktreeTimeline[1].Reason != WorktreeSwitchExit || got.WorktreeTimeline[1].Generation != 2 {
		t.Fatalf("timeline = %+v", got.WorktreeTimeline)
	}
}

func TestRecordWorktreeBoundaryTrimsTimeline(t *testing.T) {
	dir := t.TempDir()
	for i := range maxWorktreeTimelineEntries + 5 {
		entry := WorktreeTimelineEntry{Reason: WorktreeSwitchEnter, Generation: uint64(i + 1), At: time.Now().UTC()}
		if err := RecordWorktreeBoundary(dir, WorktreeBinding{RepoID: "abc", RepoRoot: "/repo", Name: "feat-a", Branch: "chord/feat-a", Path: "/state/wt/feat-a"}, entry); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	got, err := LoadSessionMeta(dir)
	if err != nil || got == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", got, err)
	}
	if len(got.WorktreeTimeline) != maxWorktreeTimelineEntries {
		t.Fatalf("timeline length = %d, want %d", len(got.WorktreeTimeline), maxWorktreeTimelineEntries)
	}
	// The newest records survive; the dropped ones are the oldest.
	if last := got.WorktreeTimeline[len(got.WorktreeTimeline)-1]; last.Generation != uint64(maxWorktreeTimelineEntries+5) {
		t.Fatalf("newest timeline entry = %+v", last)
	}
	if first := got.WorktreeTimeline[0]; first.Generation != 6 {
		t.Fatalf("oldest retained timeline entry = %+v, want generation 6", first)
	}
}

func TestSessionMetaIsZeroTracksWorktreeTimeline(t *testing.T) {
	if !(SessionMeta{WorktreeTimeline: []WorktreeTimelineEntry{}}).IsZero() {
		t.Fatal("an empty timeline must not make the metadata meaningful")
	}
	if (SessionMeta{WorktreeTimeline: []WorktreeTimelineEntry{{Reason: WorktreeSwitchEnter}}}).IsZero() {
		t.Fatal("a recorded switch boundary must make the metadata meaningful")
	}
}

func TestRecordWorktreeBoundaryEmptySessionDirIsNoop(t *testing.T) {
	if err := RecordWorktreeBoundary("  ", WorktreeBinding{Path: "/state/wt/feat-a"}, WorktreeTimelineEntry{Reason: WorktreeSwitchEnter}); err != nil {
		t.Fatalf("empty session dir must be a no-op: %v", err)
	}
}

func TestRecordWorktreeBoundaryClearingWithoutReasonLeavesMetaEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := RecordWorktreeBoundary(dir, WorktreeBinding{}, WorktreeTimelineEntry{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got != nil {
		t.Fatalf("meta = %+v, want nil when a boundary writes nothing actionable", got)
	}
}
