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

	owner := &Owner{SessionID: "sess-1", AgentID: "main-1", Kind: OwnerKindMain, CreatedAt: time.Now().UTC()}
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
	if got.SessionID != "sess-1" || got.AgentID != "main-1" || got.Kind != OwnerKindMain {
		t.Fatalf("owner = %+v, want sess-1/main-1/main", got)
	}
}

func TestCanRemoveBySession(t *testing.T) {
	cases := []struct {
		name      string
		owner     *Owner
		session   string
		requester OwnerKind
		want      bool
	}{
		{"main removes main", &Owner{SessionID: "s1", Kind: OwnerKindMain}, "s1", OwnerKindMain, true},
		{"main removes sub", &Owner{SessionID: "s1", Kind: OwnerKindSub}, "s1", OwnerKindMain, true},
		{"sub removes sub", &Owner{SessionID: "s1", Kind: OwnerKindSub}, "s1", OwnerKindSub, true},
		// A worker must not reclaim a checkout its own session's main agent
		// created: the session is the same, the creator kind is not.
		{"sub removes main", &Owner{SessionID: "s1", Kind: OwnerKindMain}, "s1", OwnerKindSub, false},
		{"cli owner", &Owner{SessionID: "s1", Kind: OwnerKindCLI}, "s1", OwnerKindMain, false},
		{"cli requester", &Owner{SessionID: "s1", Kind: OwnerKindMain}, "s1", OwnerKindCLI, false},
		{"unknown requester", &Owner{SessionID: "s1", Kind: OwnerKindMain}, "s1", "", false},
		{"other session", &Owner{SessionID: "s2", Kind: OwnerKindMain}, "s1", OwnerKindMain, false},
		{"missing owner", nil, "s1", OwnerKindMain, false},
		{"empty session", &Owner{SessionID: "s1", Kind: OwnerKindMain}, "", OwnerKindMain, false},
	}
	for _, tc := range cases {
		if got := CanRemoveBySession(tc.owner, tc.session, tc.requester); got != tc.want {
			t.Errorf("%s: CanRemoveBySession = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestReadOwnerAcceptsSessionlessCLIRecord pins the one exception to the
// "an owner record must name its session" rule: the CLI has no session, and
// `chord worktree list` must be able to report the creator it recorded.
func TestReadOwnerAcceptsSessionlessCLIRecord(t *testing.T) {
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
	if owner.Kind != OwnerKindCLI || owner.SessionID != "" {
		t.Fatalf("owner = %+v, want a sessionless cli record", owner)
	}
	// The CLI record must stay unremovable through the tools even though it is
	// now readable.
	if CanRemoveBySession(owner, "s1", OwnerKindMain) {
		t.Error("a cli-owned worktree must not be removable through a tool")
	}
}

func TestReadOwnerRefusesSessionlessAgentRecord(t *testing.T) {
	repo := setupTestRepo(t)
	pl := setupTestLocator(t)
	ctx := context.Background()
	info, err := Create(ctx, CreateOptions{Name: "hand-written", RepoRoot: repo, PathLocator: pl})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path, err := OwnerPath(ctx, info.Path)
	if err != nil {
		t.Fatalf("OwnerPath: %v", err)
	}
	// A main/sub record without a session is an unverifiable ownership claim.
	if err := os.WriteFile(path, []byte(`{"session_id":"","agent_id":"main-1","kind":"main"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	if _, err := ReadOwner(ctx, info.Path); err == nil {
		t.Fatal("ReadOwner should refuse a sessionless agent record")
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
		Owner:       &Owner{SessionID: "sess-9", AgentID: "sub-2", Kind: OwnerKindSub},
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
	if entry.OwnerSessionID != "sess-9" || entry.OwnerAgentID != "sub-2" || entry.OwnerKind != string(OwnerKindSub) {
		t.Fatalf("index owner = %+v, want sess-9/sub-2/sub", entry)
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
