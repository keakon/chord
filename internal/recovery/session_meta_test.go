package recovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionMeta_IsZero(t *testing.T) {
	if !(SessionMeta{}).IsZero() {
		t.Errorf("zero value reports non-zero")
	}
	if (SessionMeta{ForkedFrom: "x"}).IsZero() {
		t.Errorf("ForkedFrom only reports zero")
	}
	if (SessionMeta{WorktreeName: "feat"}).IsZero() {
		t.Errorf("WorktreeName only reports zero")
	}
	if (SessionMeta{IsMainWorktree: true}).IsZero() {
		t.Errorf("IsMainWorktree=true reports zero")
	}
}

func TestSaveLoadSessionMeta_RoundTrip_Worktree(t *testing.T) {
	dir := t.TempDir()
	in := SessionMeta{
		RepoID:         "deadbeef00000000",
		RepoRoot:       "/repo/main",
		WorktreeName:   "feat-a",
		WorktreeBranch: "chord/feat-a",
		WorktreePath:   "/state/wt/feat-a",
	}
	if err := SaveSessionMeta(dir, in); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	out, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if out == nil {
		t.Fatalf("LoadSessionMeta returned nil for worktree-only meta — Load should treat any populated worktree field as meaningful")
	}
	if out.WorktreeName != in.WorktreeName || out.RepoID != in.RepoID {
		t.Errorf("round-trip lost fields: got %+v", out)
	}
	if out.ForkedFrom != "" {
		t.Errorf("ForkedFrom unexpectedly set: %q", out.ForkedFrom)
	}
}

func TestLoadSessionMeta_LegacyForkedFromOnly(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSessionMeta(dir, SessionMeta{ForkedFrom: "abc"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got == nil || got.ForkedFrom != "abc" {
		t.Fatalf("ForkedFrom-only round-trip failed: %+v", got)
	}
}

func TestLoadSessionMeta_Missing(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta on missing dir: %v", err)
	}
	if got != nil {
		t.Errorf("missing meta returned %+v, want nil", got)
	}
}

func TestLoadSessionMeta_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sessionMetaFile)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed empty meta: %v", err)
	}
	got, err := LoadSessionMeta(dir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got != nil {
		t.Errorf("empty meta {} returned %+v, want nil", got)
	}
}
