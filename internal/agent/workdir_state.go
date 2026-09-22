package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
)

// WorkDirState is the active checkout of one agent instance. The zero value
// means "the directory the session started in". Path is canonical; WorktreeID
// and Branch are empty unless the active directory is a chord-managed
// worktree.
type WorkDirState struct {
	Path       string
	WorktreeID string
	Branch     string
	// BaseSHA is the commit the binding started from: the worktree's create
	// base on Enter, or its HEAD when a worker was placed in it directly.
	// Completion reports diff against it, so it must stay with the binding
	// rather than being re-derived from the branch later.
	BaseSHA    string
	Generation uint64
}

// workDirBinding holds the active checkout for one agent. Switches publish a
// whole new state with a higher generation through a single atomic store, so a
// running tool goroutine never observes a half-updated binding.
type workDirBinding struct {
	state atomic.Pointer[WorkDirState]
}

func (b *workDirBinding) load() WorkDirState {
	if b == nil {
		return WorkDirState{}
	}
	if s := b.state.Load(); s != nil {
		return *s
	}
	return WorkDirState{}
}

func (b *workDirBinding) store(s WorkDirState) { b.state.Store(&s) }

func cleanWorkdirPath(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	return p
}

// WorktreeRuntime carries the cmd-layer services the worktree tools need.
// internal/agent never resolves storage or git topology on its own; cmd/chord
// injects this bundle before the agent starts running turns.
type WorktreeRuntime struct {
	// PathLocator resolves the state directory worktrees are created under.
	PathLocator *config.PathLocator
	// RepoRoot is the repository content root (the main checkout).
	RepoRoot string
	// BranchPrefix scopes chord-managed branches; empty means the default.
	BranchPrefix string
	// Root is the raw worktree.root value (worktree.CreateOptions.Root):
	// empty keeps the state-dir layout, a relative path resolves against
	// RepoRoot.
	Root string
	// SessionID identifies the owning session for worktree ownership records.
	SessionID string
	// RebindLSP reconfigures the language servers for a new working directory.
	// A failure is surfaced as a warning after the switch: the working
	// directory is never rolled back because diagnostics could not follow.
	RebindLSP func(workDir string) error
	// RefreshSkills reloads project skills for a new working directory.
	// Skills are project behavior (checkout wins, content root falls back),
	// not pinned control plane, so they follow the switch like AGENTS.md.
	RefreshSkills func(workDir string)
}

// SetWorktreeRuntime installs the worktree services. Call it before the agent
// runs turns; the value is read-only afterwards.
func (a *MainAgent) SetWorktreeRuntime(rt WorktreeRuntime) {
	if a == nil {
		return
	}
	a.worktreeRT = rt
}

// resolveRestoredWorktree validates a checkout recorded for a rehydrated agent.
// It returns the worktree only when the directory is still a chord-managed
// worktree of this repository, so replay never resolves a restored
// transcript's relative paths against a stale or foreign path. The full Info
// travels with it: the resumed binding needs the name, branch, and base commit
// to describe the checkout, not just its directory.
func (a *MainAgent) resolveRestoredWorktree(ctx context.Context, dir string) *worktree.Info {
	dir = strings.TrimSpace(dir)
	if dir == "" || a == nil {
		return nil
	}
	rt := a.worktreeRT
	if strings.TrimSpace(rt.RepoRoot) == "" {
		return nil
	}
	info, err := worktree.ResolveByPath(ctx, rt.RepoRoot, dir, rt.BranchPrefix)
	if err != nil {
		return nil
	}
	return info
}

// workDirActor adapts one agent instance to the worktree tools: where its
// active binding lives, which directory it started in, the injected services,
// and the post-switch refresh that never rolls a published switch back.
type workDirActor struct {
	binding     *workDirBinding
	startupDir  string
	deps        WorktreeRuntime
	agentID     string
	kind        worktree.OwnerKind
	afterSwitch func(prev, next WorkDirState) []string
}

func (act workDirActor) currentDir() string {
	if p := strings.TrimSpace(act.binding.load().Path); p != "" {
		return p
	}
	return strings.TrimSpace(act.startupDir)
}

func (act workDirActor) available() bool {
	return strings.TrimSpace(act.deps.RepoRoot) != "" && act.deps.PathLocator != nil
}

func (a *MainAgent) worktreeActor() workDirActor {
	return workDirActor{
		binding:     &a.workDirState,
		startupDir:  a.cachedWorkDir,
		deps:        a.worktreeRT,
		agentID:     a.instanceID,
		kind:        worktree.OwnerKindMain,
		afterSwitch: a.afterWorkDirSwitch,
	}
}

func (s *SubAgent) worktreeActor() workDirActor {
	act := workDirActor{
		binding:    &s.workDirState,
		startupDir: s.workDir,
		agentID:    s.instanceID,
		kind:       worktree.OwnerKindSub,
	}
	if s.parent != nil {
		act.deps = s.parent.worktreeRT
		act.afterSwitch = func(WorkDirState, WorkDirState) []string {
			s.parent.refreshPathRoots()
			// The worker's own reminder states the working directory, so it
			// must be rebuilt on the switch: otherwise every later request
			// keeps describing the checkout the worker left. The system prompt
			// (and the AGENTS.md captured with it) stays frozen for the life
			// of the task.
			s.refreshSessionContextReminder()
			// The per-instance metadata is the authoritative source a
			// rehydrated worker resumes from, so a switch must not leave it
			// pointing at the previous checkout.
			if err := s.parent.persistSubAgentMeta(s); err != nil {
				log.Warnf("persist subagent workdir failed agent=%v error=%v", s.instanceID, err)
			}
			return nil
		}
	}
	return act
}

// afterWorkDirSwitch refreshes every workDir-derived surface after a published
// switch. Failures here are warnings, never rollbacks: an agent that switched
// but could not rebind LSP is still better off than one stuck in the previous
// checkout.
func (a *MainAgent) afterWorkDirSwitch(prev, next WorkDirState) []string {
	a.refreshPathRoots()
	a.ReloadAgentsMD()
	// The injected git status and virtualenv path describe the working
	// directory, so they follow the switch for the same reason AGENTS.md does.
	a.refreshWorkDirDerivedMeta()
	if a.worktreeRT.RefreshSkills != nil {
		a.worktreeRT.RefreshSkills(a.workDir())
	}
	a.refreshSessionContextReminder()
	reason := recovery.WorktreeSwitchEnter
	if strings.TrimSpace(next.Path) == "" {
		reason = recovery.WorktreeSwitchExit
	}
	var warnings []string
	if err := a.recordWorkDirBoundary(next, reason); err != nil {
		warnings = append(warnings, fmt.Sprintf("working directory is now %s but the session metadata could not be updated (%v); resume may restore the previous checkout", a.workDir(), err))
	}
	if a.worktreeRT.RebindLSP != nil {
		if err := a.worktreeRT.RebindLSP(a.workDir()); err != nil {
			warnings = append(warnings, fmt.Sprintf("working directory is now %s but the language servers could not be reconfigured (%v); diagnostics may still refer to %s", a.workDir(), err, prev.Path))
		}
	}
	return warnings
}

// recordWorkDirBoundary persists the active checkout together with the
// boundary record of the switch that produced it. The switch is already
// published, so a write failure is reported as a warning by the caller and
// never rolls the checkout back.
func (a *MainAgent) recordWorkDirBoundary(next WorkDirState, reason string) error {
	if a == nil || strings.TrimSpace(a.sessionDir) == "" {
		return nil
	}
	rt := a.worktreeRT
	binding := recovery.WorktreeBinding{RepoRoot: strings.TrimSpace(rt.RepoRoot)}
	if binding.RepoRoot != "" {
		binding.RepoID = worktree.RepoIDFor(binding.RepoRoot)
	}
	entry := recovery.WorktreeTimelineEntry{
		Reason:     reason,
		Name:       next.WorktreeID,
		Branch:     next.Branch,
		Path:       next.Path,
		Generation: next.Generation,
		At:         time.Now().UTC(),
	}
	if path := strings.TrimSpace(next.Path); path != "" {
		binding.Name = next.WorktreeID
		binding.Branch = next.Branch
		binding.Path = path
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if head, err := worktree.HeadCommit(ctx, path); err == nil {
			entry.Head = head
		}
		cancel()
	}
	return recovery.RecordWorktreeBoundary(a.sessionDir, binding, entry)
}

// RestoreWorkDirBinding installs the checkout a session starts in, either
// because it was created with --worktree or because resume recorded one. The
// directory must still be a chord-managed worktree of this repository;
// otherwise the session keeps its startup directory, the drop is recorded as a
// fallback boundary, and the returned notice explains it. Returns "" when the
// binding was installed or when there was nothing to restore.
func (a *MainAgent) RestoreWorkDirBinding(ctx context.Context, state WorkDirState, reason string) string {
	if a == nil || strings.TrimSpace(state.Path) == "" {
		return ""
	}
	if reason == "" {
		reason = recovery.WorktreeSwitchResume
	}
	info := a.resolveRestoredWorktree(ctx, state.Path)
	if info == nil {
		detail := fmt.Sprintf("recorded worktree %s is no longer a worktree of this repository", state.Path)
		entry := recovery.WorktreeTimelineEntry{
			Reason:   recovery.WorktreeSwitchResumeFallback,
			Name:     state.WorktreeID,
			Branch:   state.Branch,
			Path:     state.Path,
			Fallback: true,
			Detail:   detail,
			At:       time.Now().UTC(),
		}
		if err := recovery.RecordWorktreeBoundary(a.sessionDir, recovery.WorktreeBinding{}, entry); err != nil {
			log.Warnf("record worktree resume fallback failed session_dir=%v error=%v", a.sessionDir, err)
		}
		return fmt.Sprintf("session was working in worktree %s, which no longer exists; continuing in %s", state.Path, a.workDir())
	}
	state.Path = info.Path
	if strings.TrimSpace(state.BaseSHA) == "" {
		if head, err := worktree.HeadCommit(ctx, info.Path); err == nil {
			state.BaseSHA = head
		}
	}
	a.workDirState.store(state)
	// Installing the binding is a publication like any other: without this a
	// session that starts in a worktree would inject the startup checkout's
	// branch for its whole life.
	a.refreshWorkDirDerivedMeta()
	if err := a.recordWorkDirBoundary(state, reason); err != nil {
		log.Warnf("record restored worktree binding failed session_dir=%v error=%v", a.sessionDir, err)
	}
	return ""
}

func (act workDirActor) enter(ctx context.Context, req tools.WorktreeEnterRequest) (tools.WorktreeEnterResult, error) {
	var res tools.WorktreeEnterResult
	if !act.available() {
		return res, fmt.Errorf("worktree tools are unavailable in this session")
	}
	prev := act.binding.load()
	currentDir := act.currentDir()
	if currentDir == "" {
		return res, fmt.Errorf("this agent has no working directory to base a worktree on")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" && strings.TrimSpace(req.Path) == "" {
		name = worktree.GenerateAutoSlug(time.Now())
	}
	base := strings.TrimSpace(req.Base)
	if base == "" && strings.TrimSpace(req.Branch) == "" {
		head, err := worktree.HeadCommit(ctx, currentDir)
		if err != nil {
			return res, fmt.Errorf("resolve the base commit of %s: %w", currentDir, err)
		}
		base = head
	}
	owner := &worktree.Owner{
		SessionID: act.deps.SessionID,
		AgentID:   act.agentID,
		Kind:      act.kind,
		CreatedAt: time.Now().UTC(),
	}
	info, err := worktree.Create(ctx, worktree.CreateOptions{
		Name:         name,
		RepoRoot:     act.deps.RepoRoot,
		PathLocator:  act.deps.PathLocator,
		BranchPrefix: act.deps.BranchPrefix,
		ResetBranch:  req.ResetBranch,
		Path:         req.Path,
		Root:         act.deps.Root,
		Base:         base,
		Branch:       req.Branch,
		AllowNested:  true,
		Owner:        owner,
	})
	if err != nil {
		return res, err
	}
	res = tools.WorktreeEnterResult{
		Name:         info.Name,
		Branch:       info.Branch,
		Path:         info.Path,
		MainRoot:     info.RepoRoot,
		BaseSHA:      info.BaseSHA,
		Existed:      info.Existed,
		MainDirty:    info.MainDirty,
		Generation:   prev.Generation,
		PreviousPath: currentDir,
	}
	if info.Path == currentDir {
		// Already working here: entering again must not bump the generation or
		// re-run the post-switch hooks.
		return res, nil
	}
	if err := worktree.RegisterInIndex(act.deps.PathLocator, info); err != nil {
		// The index is a display cache; the worktree itself is usable.
		res.Warnings = append(res.Warnings, fmt.Sprintf("worktree index not updated (%v)", err))
	}
	next := WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: prev.Generation + 1,
	}
	act.binding.store(next)
	res.Generation = next.Generation
	if act.afterSwitch != nil {
		res.Warnings = append(res.Warnings, act.afterSwitch(prev, next)...)
	}
	return res, nil
}

func (act workDirActor) exit(ctx context.Context, req tools.WorktreeExitRequest) (tools.WorktreeExitResult, error) {
	var res tools.WorktreeExitResult
	if !act.available() {
		return res, fmt.Errorf("worktree tools are unavailable in this session")
	}
	prev := act.binding.load()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = prev.WorktreeID
	}
	if name == "" {
		return res, fmt.Errorf("no worktree is active; pass the worktree name to leave or remove one")
	}
	info, err := worktree.ResolveByName(ctx, act.deps.RepoRoot, name, act.deps.BranchPrefix)
	if err != nil {
		return res, err
	}
	res.Name = info.Name
	res.Path = info.Path
	res.Branch = info.Branch
	active := prev.WorktreeID == info.Name
	if req.Remove {
		if err := act.guardRemoval(ctx, info, active, req.DiscardChanges); err != nil {
			return res, err
		}
		if err := worktree.Remove(ctx, act.deps.RepoRoot, info.Name, worktree.RemoveOptions{
			DiscardChanges: true,
			BranchPrefix:   act.deps.BranchPrefix,
		}, act.deps.PathLocator); err != nil {
			return res, err
		}
		res.Removed = true
	}
	if active {
		next := WorkDirState{Generation: prev.Generation + 1}
		act.binding.store(next)
		res.WorkDir = act.currentDir()
		if act.afterSwitch != nil {
			res.Warnings = append(res.Warnings, act.afterSwitch(prev, next)...)
		}
	}
	return res, nil
}

// guardRemoval enforces the ownership and dirty-state rules that stand between
// an agent and deleting someone else's work.
func (act workDirActor) guardRemoval(ctx context.Context, info *worktree.Info, active, discardChanges bool) error {
	if info != nil && strings.TrimSpace(act.deps.RepoRoot) != "" {
		if cleanWorkdirPath(info.Path) == cleanWorkdirPath(act.deps.RepoRoot) {
			return fmt.Errorf("worktree %q is the main checkout; the main checkout cannot be removed", info.Name)
		}
	}
	if active {
		return fmt.Errorf("worktree %q is the agent's active working directory; leave it first (action: keep), then remove it", info.Name)
	}
	if info.Path == act.currentDir() {
		return fmt.Errorf("worktree %q is the agent's working directory; leave it first (action: keep), then remove it", info.Name)
	}
	owner, err := worktree.ReadOwner(ctx, info.Path)
	if err != nil {
		return fmt.Errorf("cannot verify who created worktree %q (%v); remove it with `chord worktree remove %s`", info.Name, err, info.Name)
	}
	if !worktree.CanRemoveBySession(owner, act.deps.SessionID, act.kind) {
		if owner.Kind == worktree.OwnerKindCLI {
			return fmt.Errorf("worktree %q was created by the chord command line; only `chord worktree remove %s` can delete it", info.Name, info.Name)
		}
		return fmt.Errorf("worktree %q was created by session %s (%s), and this %s agent does not own it; only its owning agent or `chord worktree remove %s` can delete it", info.Name, owner.SessionID, owner.Kind, act.kind, info.Name)
	}
	if discardChanges {
		return nil
	}
	if dirty, ok := worktree.IsDirty(ctx, info.Path); !ok {
		return fmt.Errorf("cannot verify whether worktree %q has uncommitted changes; pass discard_changes to remove it anyway", info.Name)
	} else if dirty {
		return fmt.Errorf("worktree %q has uncommitted changes; pass discard_changes to remove it anyway", info.Name)
	}
	if unmerged, err := worktree.UnmergedCommits(ctx, info.RepoRoot, info.Branch); err != nil {
		return fmt.Errorf("cannot verify whether branch %s has commits that exist only there (%v); pass discard_changes to remove the worktree anyway", info.Branch, err)
	} else if unmerged > 0 {
		return fmt.Errorf("branch %s has %d commit(s) that exist only there; pass discard_changes to remove the worktree anyway (the branch is kept)", info.Branch, unmerged)
	}
	return nil
}

func (act workDirActor) list(ctx context.Context) ([]tools.WorktreeListEntry, error) {
	if !act.available() {
		return nil, fmt.Errorf("worktree tools are unavailable in this session")
	}
	infos, err := worktree.List(ctx, act.deps.RepoRoot, act.deps.BranchPrefix)
	if err != nil {
		return nil, err
	}
	active := act.binding.load().WorktreeID
	entries := make([]tools.WorktreeListEntry, 0, len(infos))
	for i := range infos {
		info := infos[i]
		entry := tools.WorktreeListEntry{
			Name:    info.Name,
			Branch:  info.Branch,
			Path:    info.Path,
			Current: active != "" && active == info.Name,
		}
		if dirty, ok := worktree.IsDirty(ctx, info.Path); ok {
			entry.DirtyKnown = true
			entry.Dirty = dirty
		}
		if owner, err := worktree.ReadOwner(ctx, info.Path); err == nil {
			entry.OwnerKnown = true
			entry.OwnerKind = string(owner.Kind)
			entry.OwnerSessionID = owner.SessionID
			entry.OwnerAgentID = owner.AgentID
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// WorktreeEnter implements tools.WorktreeHost for the MainAgent.
func (a *MainAgent) WorktreeEnter(ctx context.Context, req tools.WorktreeEnterRequest) (tools.WorktreeEnterResult, error) {
	return a.worktreeActor().enter(ctx, req)
}

// WorktreeExit implements tools.WorktreeHost for the MainAgent.
func (a *MainAgent) WorktreeExit(ctx context.Context, req tools.WorktreeExitRequest) (tools.WorktreeExitResult, error) {
	return a.worktreeActor().exit(ctx, req)
}

// WorktreeList implements tools.WorktreeHost for the MainAgent.
func (a *MainAgent) WorktreeList(ctx context.Context) ([]tools.WorktreeListEntry, error) {
	return a.worktreeActor().list(ctx)
}

// WorktreeEnter implements tools.WorktreeHost for a SubAgent, which owns its
// own working directory even though it shares the repository's roots.
func (s *SubAgent) WorktreeEnter(ctx context.Context, req tools.WorktreeEnterRequest) (tools.WorktreeEnterResult, error) {
	return s.worktreeActor().enter(ctx, req)
}

// WorktreeExit implements tools.WorktreeHost for a SubAgent.
func (s *SubAgent) WorktreeExit(ctx context.Context, req tools.WorktreeExitRequest) (tools.WorktreeExitResult, error) {
	return s.worktreeActor().exit(ctx, req)
}

// WorktreeList implements tools.WorktreeHost for a SubAgent.
func (s *SubAgent) WorktreeList(ctx context.Context) ([]tools.WorktreeListEntry, error) {
	return s.worktreeActor().list(ctx)
}
