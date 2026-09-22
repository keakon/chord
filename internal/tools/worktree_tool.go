package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// WorktreeEnterRequest asks the agent runtime to make a worktree the agent's
// active working directory, creating it first when it does not exist yet.
type WorktreeEnterRequest struct {
	Name        string
	Path        string
	Base        string
	Branch      string
	ResetBranch bool
}

// WorktreeEnterResult is the outcome of a successful switch. Warnings carry
// post-switch failures (LSP reconfiguration, reminder refresh) that did not
// roll the working directory back.
type WorktreeEnterResult struct {
	Name         string
	Branch       string
	Path         string
	MainRoot     string
	BaseSHA      string
	Existed      bool
	MainDirty    bool
	Generation   uint64
	PreviousPath string
	Warnings     []string
}

// WorktreeExitRequest unbinds the active worktree, optionally removing the
// checkout as well.
type WorktreeExitRequest struct {
	Name           string
	Remove         bool
	DiscardChanges bool
}

// WorktreeExitResult reports what the exit did and which directory the agent
// now works in.
type WorktreeExitResult struct {
	Name     string
	Path     string
	Branch   string
	Removed  bool
	WorkDir  string
	Warnings []string
}

// WorktreeListEntry describes one chord-managed worktree of the repository.
type WorktreeListEntry struct {
	Name           string
	Branch         string
	Path           string
	OwnerKind      string
	OwnerSessionID string
	OwnerAgentID   string
	OwnerKnown     bool
	Dirty          bool
	DirtyKnown     bool
	Current        bool
}

// WorktreeHost is the per-agent runtime that owns one agent's active working
// directory. MainAgent and SubAgent each implement it over their own binding,
// so a SubAgent switches only its own working directory.
type WorktreeHost interface {
	WorktreeEnter(ctx context.Context, req WorktreeEnterRequest) (WorktreeEnterResult, error)
	WorktreeExit(ctx context.Context, req WorktreeExitRequest) (WorktreeExitResult, error)
	WorktreeList(ctx context.Context) ([]WorktreeListEntry, error)
}

// WorktreeEnterTool creates or reopens a repository worktree and switches the
// calling agent into it.
type WorktreeEnterTool struct {
	Host WorktreeHost
}

func NewWorktreeEnterTool(host WorktreeHost) WorktreeEnterTool {
	return WorktreeEnterTool{Host: host}
}

func (WorktreeEnterTool) Name() string { return NameWorktreeEnter }

func (WorktreeEnterTool) Description() string {
	return "Creates or opens a git worktree of the current repository and switches this agent's working directory into it for the rest of the session.\n" +
		"Use it only when the user explicitly asks to work in a separate worktree, branch checkout, or isolated copy; do not enter one on your own initiative. " +
		"After a successful switch the shell, file tools, grep/glob and LSP all operate inside the worktree until `" + NameWorktreeExit + "`.\n" +
		"Worktrees share the repository's session history and permissions: entering one does not change permission rules, hooks or agent configuration, and it never modifies the main checkout. " +
		"Tracked files come from the branch; ignore-rule content (local config, AGENTS.md, .chord) is provided by the main checkout rather than copied.\n" +
		"Parameters: `name` (optional; a name is generated when omitted and when `branch` is omitted), `path` (optional; defaults to the configured worktree root), " +
		"`base` (optional commit to branch from; defaults to the current working directory's HEAD), `branch` (optional existing chord-managed branch to check out instead of creating one). " +
		"If a branch with the requested name already exists without being checked out anywhere, the call fails unless `reset_branch` is true; resetting discards that branch's current tip."
}

func (WorktreeEnterTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Worktree name; also the directory and branch slug. Optional: a unique name is generated when omitted.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to create the worktree in. Optional; defaults to the configured worktree root. Relative paths resolve against the repository root.",
			},
			"base": map[string]any{
				"type":        "string",
				"description": "Commit-ish the new branch starts at. Optional; defaults to the current working directory's HEAD.",
			},
			"branch": map[string]any{
				"type":        "string",
				"description": "Existing chord-managed branch to check out instead of creating a new branch. Mutually exclusive with `base` and `reset_branch`.",
			},
			"reset_branch": map[string]any{
				"type":        "boolean",
				"description": "Reset an existing leftover branch to the base commit. Only use it when the user explicitly accepts losing that branch's current tip.",
			},
		},
		"additionalProperties": false,
	}
}

func (WorktreeEnterTool) IsReadOnly() bool { return false }

func (t WorktreeEnterTool) IsAvailable() bool { return t.Host != nil }

func (t WorktreeEnterTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.Host == nil {
		return "", fmt.Errorf("worktree tools are unavailable for this agent")
	}
	var args struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Base        string `json:"base"`
		Branch      string `json:"branch"`
		ResetBranch bool   `json:"reset_branch"`
	}
	if err := decodeWorktreeArgs(raw, &args); err != nil {
		return "", err
	}
	req := WorktreeEnterRequest{
		Name:        strings.TrimSpace(args.Name),
		Path:        strings.TrimSpace(args.Path),
		Base:        strings.TrimSpace(args.Base),
		Branch:      strings.TrimSpace(args.Branch),
		ResetBranch: args.ResetBranch,
	}
	result, err := t.Host.WorktreeEnter(ctx, req)
	if err != nil {
		return "", err
	}
	return formatWorktreeEnterResult(result), nil
}

// WorktreeExitTool leaves the active worktree, optionally removing its
// checkout.
type WorktreeExitTool struct {
	Host WorktreeHost
}

func NewWorktreeExitTool(host WorktreeHost) WorktreeExitTool {
	return WorktreeExitTool{Host: host}
}

func (WorktreeExitTool) Name() string { return NameWorktreeExit }

func (WorktreeExitTool) Description() string {
	return "Leaves the active worktree and returns this agent to the checkout the session started in, or removes a worktree checkout.\n" +
		"`action: \"keep\"` (default) only unbinds the working directory and always keeps the branch and its commits. " +
		"`action: \"remove\"` deletes the checkout after the work is done; the branch is always kept. " +
		"Removal is refused when this session does not own the worktree, when the worktree is the currently active working directory (leave first), or when it has uncommitted changes or commits that exist only on its branch. " +
		"Pass `discard_changes: true` only when the user explicitly accepts losing those changes."
}

func (WorktreeExitTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Worktree to leave. Optional; defaults to the active worktree.",
			},
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"keep", "remove"},
				"description": "keep (default) unbinds the directory; remove also deletes the checkout but keeps the branch.",
			},
			"discard_changes": map[string]any{
				"type":        "boolean",
				"description": "Remove even when the worktree has uncommitted changes or unmerged commits. Only set it when the user explicitly accepts losing that work.",
			},
		},
		"additionalProperties": false,
	}
}

func (WorktreeExitTool) IsReadOnly() bool { return false }

func (t WorktreeExitTool) IsAvailable() bool { return t.Host != nil }

func (t WorktreeExitTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.Host == nil {
		return "", fmt.Errorf("worktree tools are unavailable for this agent")
	}
	var args struct {
		Name           string `json:"name"`
		Action         string `json:"action"`
		DiscardChanges bool   `json:"discard_changes"`
	}
	if err := decodeWorktreeArgs(raw, &args); err != nil {
		return "", err
	}
	req := WorktreeExitRequest{
		Name:           strings.TrimSpace(args.Name),
		DiscardChanges: args.DiscardChanges,
	}
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "", "keep":
	case "remove":
		req.Remove = true
	default:
		return "", fmt.Errorf("invalid action %q: expected keep or remove", args.Action)
	}
	result, err := t.Host.WorktreeExit(ctx, req)
	if err != nil {
		return "", err
	}
	return formatWorktreeExitResult(result), nil
}

// WorktreeListTool reports the repository's chord-managed worktrees, their
// owner and whether they have local changes.
type WorktreeListTool struct {
	Host WorktreeHost
}

func NewWorktreeListTool(host WorktreeHost) WorktreeListTool {
	return WorktreeListTool{Host: host}
}

func (WorktreeListTool) Name() string { return NameWorktreeList }

func (WorktreeListTool) Description() string {
	return "Lists the current repository's chord-managed worktrees with their branch, owner, path and whether they have uncommitted changes. " +
		"Read-only. Use it to find a worktree to enter (by name) or to check what is still around before removing one."
}

func (WorktreeListTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (WorktreeListTool) IsReadOnly() bool { return true }

// ConcurrencyPolicy lets two listings batch: the call only reads git worktree
// metadata, owner files and the repository index cache, and never mutates the
// active checkout or the worktree set.
func (WorktreeListTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "tool:worktree_list", Mode: ConcurrencyModeRead}
}

// ConcurrencySafeReadOnly allows the listing to run next to other read-only
// calls; it only inspects repository state.
func (WorktreeListTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (t WorktreeListTool) IsAvailable() bool { return t.Host != nil }

func (t WorktreeListTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.Host == nil {
		return "", fmt.Errorf("worktree tools are unavailable for this agent")
	}
	entries, err := t.Host.WorktreeList(ctx)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "No chord-managed worktrees in this repository.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d chord-managed worktree(s):\n", len(entries))
	for _, e := range entries {
		marker := " "
		if e.Current {
			marker = "*"
		}
		status := "unknown"
		if e.DirtyKnown {
			if e.Dirty {
				status = "dirty"
			} else {
				status = "clean"
			}
		}
		fmt.Fprintf(&b, "%s %s\n    branch: %s\n    path:   %s\n    owner:  %s\n    status: %s\n",
			marker, e.Name, e.Branch, e.Path, formatWorktreeOwner(e), status)
	}
	b.WriteString("* = the agent's active working directory")
	return b.String(), nil
}

func decodeWorktreeArgs(raw json.RawMessage, dst any) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func formatWorktreeOwner(e WorktreeListEntry) string {
	if !e.OwnerKnown {
		return "unknown (created outside chord); remove it with `chord worktree remove`"
	}
	session := e.OwnerSessionID
	if len(session) > 8 {
		session = session[:8]
	}
	label := strings.TrimSpace(e.OwnerKind)
	if session != "" {
		label += ":" + session
	}
	if e.OwnerAgentID != "" {
		label += "/" + e.OwnerAgentID
	}
	return label
}

func formatWorktreeEnterResult(r WorktreeEnterResult) string {
	var b strings.Builder
	verb := "Entered existing worktree"
	if !r.Existed {
		verb = "Created and entered worktree"
	}
	fmt.Fprintf(&b, "%s %s\n", verb, r.Name)
	fmt.Fprintf(&b, "  path:   %s\n", r.Path)
	fmt.Fprintf(&b, "  branch: %s\n", r.Branch)
	if r.BaseSHA != "" {
		fmt.Fprintf(&b, "  base:   %s\n", shortWorktreeSHA(r.BaseSHA))
	}
	fmt.Fprintf(&b, "Main checkout: %s\n", r.MainRoot)
	if r.PreviousPath != "" && r.PreviousPath != r.Path {
		fmt.Fprintf(&b, "Working directory changed from %s to %s; shell, file tools, grep/glob and LSP now run in the worktree.\n", r.PreviousPath, r.Path)
	}
	if r.MainDirty {
		b.WriteString("Note: the main checkout has uncommitted changes that are not visible in the worktree.\n")
	}
	b.WriteString("Sessions and permission rules are shared by every checkout of the repository.\n")
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", w)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatWorktreeExitResult(r WorktreeExitResult) string {
	var b strings.Builder
	if r.Removed {
		if r.Name != "" {
			fmt.Fprintf(&b, "Removed worktree %s (branch %s kept)", r.Name, r.Branch)
		} else {
			b.WriteString("Removed worktree (branch kept)")
		}
	} else if r.Name != "" {
		fmt.Fprintf(&b, "Left worktree %s (branch %s kept)", r.Name, r.Branch)
	} else {
		b.WriteString("Left the active worktree")
	}
	if r.WorkDir != "" {
		fmt.Fprintf(&b, "; now working in %s", r.WorkDir)
	}
	b.WriteString(".")
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nWarning: %s", w)
	}
	return b.String()
}

func shortWorktreeSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
