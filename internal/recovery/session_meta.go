package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

const sessionMetaFile = "session-meta.json"

var sessionMetaWriteMu sync.Mutex

// SessionMeta stores lightweight per-session metadata that is not part of the
// transcript itself.
type SessionMeta struct {
	ForkedFrom string `json:"forked_from,omitempty"`
	Title      string `json:"title,omitempty"`
	// Worktree provenance: identifies the chord-managed git worktree the
	// session ran in. Empty for sessions in a non-worktree (main) project.
	// All fields populated together by the worktree startup path.
	RepoID         string `json:"repo_id,omitempty"`
	RepoRoot       string `json:"repo_root,omitempty"`
	WorktreeName   string `json:"worktree_name,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	IsMainWorktree bool   `json:"is_main_worktree,omitempty"`

	// ImportedFrom captures external import provenance (chord import).
	ImportedFrom *ImportMeta `json:"imported_from,omitempty"`

	// MCPEnabledServers records the manual MCP servers this session intends to
	// have enabled. It is the user's durable intent, not a connection snapshot:
	// a server listed here may still be disconnected (pending/retrying). Only
	// manual servers are persisted; automatic servers stay config-driven.
	MCPEnabledServers []string `json:"mcp_enabled_servers,omitempty"`
}

type ImportMeta struct {
	Source          string    `json:"source"` // claude|codex|opencode
	SourcePath      string    `json:"source_path,omitempty"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	ImportedAt      time.Time `json:"imported_at"`
	ReasoningMode   string    `json:"reasoning_mode,omitempty"`
	ReportPath      string    `json:"report_path,omitempty"`
}

// IsZero reports whether m carries no useful information; used by Load to
// avoid surfacing empty metadata files as non-nil to callers.
func (m SessionMeta) IsZero() bool {
	return m.ForkedFrom == "" &&
		m.Title == "" &&
		m.RepoID == "" &&
		m.RepoRoot == "" &&
		m.WorktreeName == "" &&
		m.WorktreeBranch == "" &&
		m.WorktreePath == "" &&
		m.ImportedFrom == nil &&
		len(m.MCPEnabledServers) == 0 &&
		!m.IsMainWorktree
}

// NormalizeMCPEnabledServers returns a canonical, sorted, de-duplicated copy of
// names. Empty entries are dropped. A nil input yields nil, matching the
// omitempty persistence shape.
func NormalizeMCPEnabledServers(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SetMCPEnabledServers replaces the session's persisted manual MCP enabled
// intent with the given server names while preserving every other metadata
// field. names are normalized (trimmed, de-duplicated, sorted); an empty or nil
// slice clears the intent.
func SetMCPEnabledServers(sessionDir string, names []string) error {
	return UpdateSessionMeta(sessionDir, func(meta *SessionMeta) {
		meta.MCPEnabledServers = NormalizeMCPEnabledServers(names)
	})
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
	sessionMetaWriteMu.Lock()
	defer sessionMetaWriteMu.Unlock()
	return saveSessionMeta(sessionDir, meta)
}

func saveSessionMeta(sessionDir string, meta SessionMeta) error {
	meta.MCPEnabledServers = NormalizeMCPEnabledServers(meta.MCPEnabledServers)
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(sessionDir, sessionMetaFile)
	tmp := path + ".tmp"
	if err := privatefs.WriteFile(sessionDir, tmp, data); err != nil {
		return fmt.Errorf("write session meta tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install session meta: %w", err)
	}
	return nil
}

// UpdateSessionMeta loads the current metadata (or starts from a zero value
// when none exists), applies mutate, and atomically saves the result. mutate
// receives a non-nil *SessionMeta and may replace individual fields; it must
// not hold the pointer beyond the call. This is the single merge/update entry
// point so partial writers cannot silently clobber unrelated metadata such as
// the MCP enabled intent.
func UpdateSessionMeta(sessionDir string, mutate func(*SessionMeta)) error {
	sessionMetaWriteMu.Lock()
	defer sessionMetaWriteMu.Unlock()
	meta, err := LoadSessionMeta(sessionDir)
	if err != nil {
		return err
	}
	if meta == nil {
		meta = &SessionMeta{}
	}
	if mutate != nil {
		mutate(meta)
	}
	return saveSessionMeta(sessionDir, *meta)
}
