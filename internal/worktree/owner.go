package worktree

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OwnerKind describes which kind of chord actor created a worktree.
type OwnerKind string

const (
	// OwnerKindCLI is the `chord worktree` command line.
	OwnerKindCLI OwnerKind = "cli"
)

// OwnerFilename is the per-worktree ownership record. It lives in the
// worktree's git administration directory (`git rev-parse --git-dir`), so it
// is removed together with the worktree and is not affected by repo index
// corruption.
const OwnerFilename = "chord-owner.json"

// Owner records worktree creation metadata. The repository index caches it
// for display; it does not authorize removal.
type Owner struct {
	Kind      OwnerKind `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// OwnerPath returns the ownership record path for the worktree at dir.
func OwnerPath(ctx context.Context, dir string) (string, error) {
	out, err := runGitText(ctx, dir, "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve worktree git dir: %w", err)
	}
	gitDir, err := absClean(out, dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, OwnerFilename), nil
}

// WriteOwner persists o for the worktree at dir. The record is written
// through a temporary file with 0600 permissions so a crash cannot leave a
// half-written owner behind.
func WriteOwner(ctx context.Context, dir string, o Owner) error {
	path, err := OwnerPath(ctx, dir)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal worktree owner: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write worktree owner: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install worktree owner: %w", err)
	}
	return nil
}

// ReadOwner loads the creation metadata for the worktree at dir. A missing or
// malformed record is an error; callers can display the checkout without it.
func ReadOwner(ctx context.Context, dir string) (*Owner, error) {
	path, err := OwnerPath(ctx, dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read worktree owner: %w", err)
	}
	var o Owner
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse worktree owner %s: %w", path, err)
	}
	return &o, nil
}
