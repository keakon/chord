package worktree

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// CurrentRepoIndexSchema is the schema version for RepoIndex on disk;
// bump and migrate when the structure changes incompatibly.
const CurrentRepoIndexSchema = 1

// RepoIndex aggregates the main repo plus all chord-managed worktrees of
// a single git repository. Path is <stateDir>/repos/<repoID>.json.
type RepoIndex struct {
	SchemaVersion int                 `json:"schema_version"`
	RepoID        string              `json:"repo_id"`
	MainRepoRoot  string              `json:"main_repo_root"`
	DisplayName   string              `json:"display_name,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	MainProject   RepoIndexProject    `json:"main_project,omitzero"`
	Worktrees     []RepoIndexWorktree `json:"worktrees,omitempty"`
}

// RepoIndexProject pairs a project root with the chord ProjectKey that
// scopes its sessions/cache/exports under the global state directory.
type RepoIndexProject struct {
	ProjectKey  string `json:"project_key,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
}

// RepoIndexWorktree records one chord-managed worktree under this repo.
// Path is the canonical worktree root; ProjectKey is used to locate and
// purge per-project state on remove.
type RepoIndexWorktree struct {
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	Branch     string    `json:"branch"`
	Path       string    `json:"path"`
	ProjectKey string    `json:"project_key,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// RepoIDFor returns a short stable identifier for a canonical main repo
// root. Uses sha1 truncated to 16 hex chars: short enough to keep paths
// well under MAX_PATH on Windows, with negligible collision risk.
func RepoIDFor(canonicalMainRepoRoot string) string {
	sum := sha1.Sum([]byte(canonicalMainRepoRoot))
	return hex.EncodeToString(sum[:])[:16]
}

// repoIndexPath returns <stateDir>/repos/<repoID>.json. Callers should
// MkdirAll the parent before writing.
func repoIndexPath(stateDir, repoID string) string {
	return filepath.Join(stateDir, "repos", repoID+".json")
}

// LoadRepoIndex reads <stateDir>/repos/<repoID>.json. Returns (nil, nil)
// when the file does not exist; corrupt JSON is also reported as
// (nil, nil) so callers can recreate the index from git as the source of
// truth — repo index is a cache, never authoritative.
func LoadRepoIndex(stateDir, repoID string) (*RepoIndex, error) {
	path := repoIndexPath(stateDir, repoID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read repo index: %w", err)
	}
	var idx RepoIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, nil
	}
	if idx.RepoID == "" {
		return nil, nil
	}
	return &idx, nil
}

// SaveRepoIndex atomically writes the index to disk. It does NOT take a
// cross-process lock; pair with WithRepoIndexLock for write paths that
// race with other chord processes.
func SaveRepoIndex(stateDir string, idx *RepoIndex) error {
	if idx == nil {
		return fmt.Errorf("save repo index: nil index")
	}
	if idx.RepoID == "" {
		return fmt.Errorf("save repo index: empty repo id")
	}
	idx.SchemaVersion = CurrentRepoIndexSchema
	idx.UpdatedAt = time.Now().UTC()
	if idx.CreatedAt.IsZero() {
		idx.CreatedAt = idx.UpdatedAt
	}
	path := repoIndexPath(stateDir, idx.RepoID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir repo index dir: %w", err)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal repo index: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write repo index tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install repo index: %w", err)
	}
	return nil
}

// UpsertWorktree adds or replaces the worktree entry keyed by name.
// CreatedAt is preserved on update; LastUsedAt is bumped to now.
func (idx *RepoIndex) UpsertWorktree(entry RepoIndexWorktree) {
	now := time.Now().UTC()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.LastUsedAt.IsZero() {
		entry.LastUsedAt = now
	}
	for i := range idx.Worktrees {
		if idx.Worktrees[i].Name == entry.Name {
			entry.CreatedAt = idx.Worktrees[i].CreatedAt
			idx.Worktrees[i] = entry
			return
		}
	}
	idx.Worktrees = append(idx.Worktrees, entry)
}

// RemoveWorktree drops the worktree entry by name. Returns true when an
// entry was actually removed.
func (idx *RepoIndex) RemoveWorktree(name string) bool {
	for i := range idx.Worktrees {
		if idx.Worktrees[i].Name == name {
			idx.Worktrees = append(idx.Worktrees[:i], idx.Worktrees[i+1:]...)
			return true
		}
	}
	return false
}

// FindWorktree returns the entry matching name, or nil when absent.
func (idx *RepoIndex) FindWorktree(name string) *RepoIndexWorktree {
	for i := range idx.Worktrees {
		if idx.Worktrees[i].Name == name {
			return &idx.Worktrees[i]
		}
	}
	return nil
}

// SortWorktreesByLastUsed orders worktrees by LastUsedAt descending so
// `worktree list` shows recently active entries first.
func (idx *RepoIndex) SortWorktreesByLastUsed() {
	sort.SliceStable(idx.Worktrees, func(i, j int) bool {
		return idx.Worktrees[i].LastUsedAt.After(idx.Worktrees[j].LastUsedAt)
	})
}

// TouchLastUsed bumps the named worktree's LastUsedAt to now. No-op when
// the entry is absent so callers don't need to pre-check.
func (idx *RepoIndex) TouchLastUsed(name string) {
	if e := idx.FindWorktree(name); e != nil {
		e.LastUsedAt = time.Now().UTC()
	}
}
