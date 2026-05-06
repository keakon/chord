package worktree

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLoadRepoIndex_Missing(t *testing.T) {
	dir := t.TempDir()
	idx, err := LoadRepoIndex(dir, "abc123")
	if err != nil {
		t.Fatalf("LoadRepoIndex on missing path: %v", err)
	}
	if idx != nil {
		t.Errorf("LoadRepoIndex on missing path returned %+v, want nil", idx)
	}
}

func TestRepoIDFor_StableShort(t *testing.T) {
	a := RepoIDFor("/repo/main")
	b := RepoIDFor("/repo/main")
	c := RepoIDFor("/repo/other")
	if a != b {
		t.Errorf("repo id not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("repo id collision for distinct roots: %q", a)
	}
	if len(a) != 16 {
		t.Errorf("repo id length = %d, want 16", len(a))
	}
}

func TestSaveLoadRepoIndex_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	repoID := "abcdef0123456789"
	idx := &RepoIndex{
		RepoID:       repoID,
		MainRepoRoot: "/repo/main",
		MainProject:  RepoIndexProject{ProjectKey: "k", ProjectRoot: "/repo/main"},
	}
	idx.UpsertWorktree(RepoIndexWorktree{
		Name:       "feat-a",
		Slug:       "feat-a",
		Branch:     "chord/feat-a",
		Path:       filepath.Join(dir, "wt", "feat-a"),
		ProjectKey: "wt-key",
	})
	if err := SaveRepoIndex(dir, idx); err != nil {
		t.Fatalf("SaveRepoIndex: %v", err)
	}
	loaded, err := LoadRepoIndex(dir, repoID)
	if err != nil {
		t.Fatalf("LoadRepoIndex: %v", err)
	}
	if loaded == nil {
		t.Fatalf("LoadRepoIndex returned nil after save")
	}
	if loaded.SchemaVersion != CurrentRepoIndexSchema {
		t.Errorf("SchemaVersion = %d, want %d", loaded.SchemaVersion, CurrentRepoIndexSchema)
	}
	if loaded.RepoID != repoID || loaded.MainRepoRoot != "/repo/main" {
		t.Errorf("loaded core fields wrong: %+v", loaded)
	}
	if len(loaded.Worktrees) != 1 || loaded.Worktrees[0].Name != "feat-a" {
		t.Errorf("worktrees lost: %+v", loaded.Worktrees)
	}
}

func TestUpsertWorktree_PreservesCreatedAt(t *testing.T) {
	idx := &RepoIndex{RepoID: "x"}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	idx.UpsertWorktree(RepoIndexWorktree{Name: "a", CreatedAt: created})
	idx.UpsertWorktree(RepoIndexWorktree{Name: "a", LastUsedAt: time.Now()})
	got := idx.FindWorktree("a")
	if got == nil {
		t.Fatalf("FindWorktree(a) returned nil")
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt overwritten: got %v, want %v", got.CreatedAt, created)
	}
}

func TestRemoveWorktree(t *testing.T) {
	idx := &RepoIndex{RepoID: "x"}
	idx.UpsertWorktree(RepoIndexWorktree{Name: "a"})
	idx.UpsertWorktree(RepoIndexWorktree{Name: "b"})
	if !idx.RemoveWorktree("a") {
		t.Errorf("RemoveWorktree(a) returned false, want true")
	}
	if idx.FindWorktree("a") != nil {
		t.Errorf("a still present after remove")
	}
	if idx.RemoveWorktree("a") {
		t.Errorf("RemoveWorktree(a) on missing entry returned true")
	}
	if idx.FindWorktree("b") == nil {
		t.Errorf("b removed by accident")
	}
}

func TestLoadRepoIndex_Corrupt(t *testing.T) {
	dir := t.TempDir()
	repoID := "deadbeef00000000"
	path := repoIndexPath(dir, repoID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("seed corrupt index: %v", err)
	}
	idx, err := LoadRepoIndex(dir, repoID)
	if err != nil {
		t.Fatalf("LoadRepoIndex on corrupt file returned err: %v (want nil so caller can rebuild)", err)
	}
	if idx != nil {
		t.Errorf("corrupt index returned %+v, want nil so caller can rebuild from git", idx)
	}
}

func TestWithRepoIndexLock_Serial(t *testing.T) {
	dir := t.TempDir()
	repoID := "lockcafebabe0000"
	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			err := WithRepoIndexLock(dir, repoID, func(idx *RepoIndex) error {
				if idx.RepoID == "" {
					idx.RepoID = repoID
					idx.MainRepoRoot = "/repo"
				}
				idx.UpsertWorktree(RepoIndexWorktree{
					Name:   "w",
					Slug:   "w",
					Branch: "chord/w",
				})
				_ = i
				return nil
			})
			if err != nil {
				t.Errorf("WithRepoIndexLock: %v", err)
			}
		}(i)
	}
	wg.Wait()
	loaded, err := LoadRepoIndex(dir, repoID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRepoIndex after concurrent upserts: %v %+v", err, loaded)
	}
	if len(loaded.Worktrees) != 1 {
		t.Errorf("expected single worktree after concurrent upserts, got %d", len(loaded.Worktrees))
	}
}

// (helpers inlined; no extra package-level helpers needed)
