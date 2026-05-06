package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const sessionMetaFile = "session-meta.json"

// SessionMeta stores lightweight per-session metadata that is not part of the
// transcript itself.
type SessionMeta struct {
	ForkedFrom string `json:"forked_from,omitempty"`
	// Worktree provenance: identifies the chord-managed git worktree the
	// session ran in. Empty for sessions in a non-worktree (main) project.
	// All fields populated together by the worktree startup path.
	RepoID         string `json:"repo_id,omitempty"`
	RepoRoot       string `json:"repo_root,omitempty"`
	WorktreeName   string `json:"worktree_name,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	IsMainWorktree bool   `json:"is_main_worktree,omitempty"`
}

// IsZero reports whether m carries no useful information; used by Load to
// avoid surfacing empty metadata files as non-nil to callers.
func (m SessionMeta) IsZero() bool {
	return m.ForkedFrom == "" &&
		m.RepoID == "" &&
		m.RepoRoot == "" &&
		m.WorktreeName == "" &&
		m.WorktreeBranch == "" &&
		m.WorktreePath == "" &&
		!m.IsMainWorktree
}

// LoadSessionMeta reads session metadata for sessionDir. It returns (nil, nil)
// when no metadata file exists or when the file decodes to a zero-valued
// SessionMeta (i.e. carries no useful information).
func LoadSessionMeta(sessionDir string) (*SessionMeta, error) {
	path := filepath.Join(sessionDir, sessionMetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session meta: %w", err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse session meta: %w", err)
	}
	if meta.IsZero() {
		return nil, nil
	}
	return &meta, nil
}

// SaveSessionMeta atomically writes session metadata for sessionDir.
func SaveSessionMeta(sessionDir string, meta SessionMeta) error {
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("mkdir session dir for meta: %w", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(sessionDir, sessionMetaFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write session meta tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install session meta: %w", err)
	}
	return nil
}
