package worktree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateWritesOwnerRecord(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()

	owner := &Owner{Kind: OwnerKindCLI, CreatedAt: time.Now().UTC()}
	info, err := Create(ctx, CreateOptions{Name: "owned", RepoRoot: repo, PathLocator: pl, Owner: owner})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path, err := OwnerPath(ctx, info.Path)
	if err != nil {
		t.Fatalf("OwnerPath: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat owner record: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("owner record mode = %o, want 600", perm)
	}
	got, err := ReadOwner(ctx, info.Path)
	if err != nil {
		t.Fatalf("ReadOwner: %v", err)
	}
	if got.Kind != OwnerKindCLI {
		t.Fatalf("owner = %+v, want cli", got)
	}
}

// The CLI creator remains visible without a session identity.
func TestReadOwnerAcceptsCLIRecord(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{
		Name:        "cli-made",
		RepoRoot:    repo,
		PathLocator: pl,
		Owner:       &Owner{Kind: OwnerKindCLI, CreatedAt: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	owner, err := ReadOwner(ctx, info.Path)
	if err != nil {
		t.Fatalf("ReadOwner: %v", err)
	}
	if owner.Kind != OwnerKindCLI {
		t.Fatalf("owner = %+v, want a cli record", owner)
	}
}

func TestReadOwnerRefusesMissingRecord(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	info, err := Create(context.Background(), CreateOptions{Name: "unowned", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ReadOwner(context.Background(), info.Path); err == nil {
		t.Fatal("ReadOwner should fail when no owner record exists")
	}
}

func TestRegisterInIndexCachesOwner(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{
		Name:        "indexed",
		RepoRoot:    repo,
		PathLocator: pl,
		Owner:       &Owner{Kind: OwnerKindCLI},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := RegisterInIndex(pl, info); err != nil {
		t.Fatalf("RegisterInIndex: %v", err)
	}
	idx, err := LoadRepoIndex(pl.StateDir, info.RepoID)
	if err != nil {
		t.Fatalf("LoadRepoIndex: %v", err)
	}
	if idx == nil {
		t.Fatal("index should exist after RegisterInIndex")
	}
	if idx.SchemaVersion != CurrentRepoIndexSchema {
		t.Fatalf("schema = %d, want %d", idx.SchemaVersion, CurrentRepoIndexSchema)
	}
	entry := idx.FindWorktree("indexed")
	if entry == nil {
		t.Fatal("index entry missing")
	}
	if entry.OwnerKind != string(OwnerKindCLI) {
		t.Fatalf("index owner = %+v, want cli", entry)
	}
}

func TestLoadRepoIndexRejectsForeignSchema(t *testing.T) {
	stateDir := t.TempDir()
	path := repoIndexPath(stateDir, "repo-a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A file written by a different chord version is a cache miss, not data
	// to migrate: git plus chord-owner.json are the source of truth.
	foreign, err := json.Marshal(map[string]any{"schema_version": 1, "repo_id": "repo-a"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatalf("write foreign index: %v", err)
	}
	idx, err := LoadRepoIndex(stateDir, "repo-a")
	if err != nil {
		t.Fatalf("LoadRepoIndex: %v", err)
	}
	if idx != nil {
		t.Fatal("foreign schema should be reported as a cache miss")
	}
	if err := SaveRepoIndex(stateDir, &RepoIndex{RepoID: "repo-a"}); err != nil {
		t.Fatalf("SaveRepoIndex: %v", err)
	}
	reloaded, err := LoadRepoIndex(stateDir, "repo-a")
	if err != nil || reloaded == nil {
		t.Fatalf("reload after save: idx=%v err=%v", reloaded, err)
	}
	if reloaded.SchemaVersion != CurrentRepoIndexSchema {
		t.Fatalf("saved schema = %d, want %d", reloaded.SchemaVersion, CurrentRepoIndexSchema)
	}
}
