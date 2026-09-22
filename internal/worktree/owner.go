package worktree

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OwnerKind describes which kind of chord actor created a worktree.
type OwnerKind string

const (
	// OwnerKindMain is a MainAgent session.
	OwnerKindMain OwnerKind = "main"
	// OwnerKindSub is a SubAgent.
	OwnerKindSub OwnerKind = "sub"
	// OwnerKindCLI is the `chord worktree` command line.
	OwnerKindCLI OwnerKind = "cli"
)

// OwnerFilename is the per-worktree ownership record. It lives in the
// worktree's git administration directory (`git rev-parse --git-dir`), so it
// is removed together with the worktree and is not affected by repo index
// corruption.
const OwnerFilename = "chord-owner.json"

// Owner records who created a worktree. Ownership is used to decide whether an
// agent tool may delete it: the repo index only caches these fields for
// display, this record is authoritative.
type Owner struct {
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
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

// ReadOwner loads the ownership record for the worktree at dir. A missing or
// malformed record is an error: callers must treat "no owner" as unknown and
// refuse destructive actions instead of assuming ownership.
//
// A record without a session id is accepted only for the CLI kind, which has
// no session to name. An agent-owned record must name its session: session
// equality is exactly what CanRemoveBySession decides on, so a sessionless
// main/sub record would be an unverifiable claim to ownership.
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
	if strings.TrimSpace(o.SessionID) == "" && o.Kind != OwnerKindCLI {
		return nil, fmt.Errorf("worktree owner %s has no session id", path)
	}
	return &o, nil
}

// CanRemoveBySession reports whether an agent of requesterKind acting for
// sessionID may delete the worktree described by owner. Ownership is scoped to
// the session, so a resumed session (which is a new agent instance) can still
// clean up after itself, and to the requester's own kind: a SubAgent may only
// reclaim worktrees it created itself, while the session's MainAgent may also
// reclaim a finished worker's leftover checkout. CLI-created worktrees are
// never removable through a tool, an unknown requester kind is refused, and an
// unknown or unreadable owner is always refused.
func CanRemoveBySession(owner *Owner, sessionID string, requesterKind OwnerKind) bool {
	if owner == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || owner.SessionID != sessionID {
		return false
	}
	switch requesterKind {
	case OwnerKindMain:
		return owner.Kind == OwnerKindMain || owner.Kind == OwnerKindSub
	case OwnerKindSub:
		return owner.Kind == OwnerKindSub
	default:
		return false
	}
}
