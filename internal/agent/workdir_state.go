package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/pathutil"
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

// dirInWorktree reports whether dir is the worktree root or lies below it.
// It answers "would removing this checkout take dir with it", so the
// comparison is lexical on purpose: holders are matched on the directory an
// agent actually works in, and that directory may be a subdirectory of the
// checkout.
func dirInWorktree(dir, root string) bool {
	dir = cleanWorkdirPath(dir)
	root = cleanWorkdirPath(root)
	if dir == "" || root == "" {
		return false
	}
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, root+string(filepath.Separator))
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

// restoredPlainWorkDir returns the canonical recorded directory when it is an
// existing directory inside this repository's main checkout. A worker's
// recorded directory is usually its active checkout; a plain directory (the
// main checkout or a subdirectory of it) has no worktree identity but still
// belongs to this repository, so a rehydrated worker must keep it instead of
// inheriting the parent's current checkout — which could be a different tree.
// A removed directory, a nested checkout of its own, or a path outside the
// repository is rejected so replay never resolves against a stale or foreign
// path.
func (a *MainAgent) restoredPlainWorkDir(dir string) (string, bool) {
	dir = strings.TrimSpace(dir)
	contentRoot := strings.TrimSpace(a.ContentRoot())
	if dir == "" || contentRoot == "" {
		return "", false
	}
	canonDir, err := config.CanonicalProjectRoot(dir)
	if err != nil {
		return "", false
	}
	canonRoot, err := config.CanonicalProjectRoot(contentRoot)
	if err != nil {
		return "", false
	}
	if !dirInWorktree(canonDir, canonRoot) {
		return "", false
	}
	if st, err := os.Stat(canonDir); err != nil || !st.IsDir() {
		return "", false
	}
	// A linked worktree nested inside the main root is its own checkout:
	// adopting its directories as plain paths would splice two trees together.
	if canonDir != canonRoot && pathutil.CheckoutRoot(canonDir, canonRoot) != canonRoot {
		return "", false
	}
	return canonDir, true
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
	// removalHolders reports live work still anchored to a worktree (another
	// agent bound to it, a background command running there). nil means "no way
	// to tell"; guardRemoval treats an unknown answer conservatively by
	// refusing the removal.
	removalHolders func(info *worktree.Info) []string
}

// removalHoldersFor returns the live holders of info, or an unknown-state
// report when this actor has no holder resolver at all.
func (act workDirActor) removalHoldersFor(info *worktree.Info) []string {
	if act.removalHolders == nil {
		return []string{"this session cannot verify whether the checkout is still in use"}
	}
	return act.removalHolders(info)
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
		binding:        &a.workDirState,
		startupDir:     a.cachedWorkDirSnapshot(),
		deps:           a.worktreeRT,
		agentID:        a.instanceID,
		kind:           worktree.OwnerKindMain,
		afterSwitch:    a.afterWorkDirSwitch,
		removalHolders: a.worktreeRemovalHolders,
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
		act.removalHolders = s.parent.worktreeRemovalHolders
		act.afterSwitch = func(WorkDirState, WorkDirState) []string {
			s.parent.refreshPathRoots()
			// AGENTS.md is project behavior tied to the checkout, so the
			// worker reloads it from the checkout just entered (content root
			// falls back for gitignored instructions). Pinned control plane --
			// ruleset, hooks, agent config -- is deliberately left alone.
			if content := loadAgentsMDWithWorkDir(s.parent.ContentRoot(), s.effectiveToolBaseDir()); content != s.agentsMDSnapshot() {
				s.setAgentsMD(content)
				// The workspace-instruction framing is part of the system
				// prompt, so it follows the reload. The reminder rebuilt below
				// carries the instructions themselves.
				s.installSystemPrompt(s.buildSystemPrompt())
			}
			// The worker's own reminder states the working directory, so it
			// must be rebuilt on the switch: otherwise every later request
			// keeps describing the checkout the worker left.
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
	reason := recovery.WorktreeSwitchEnter
	if strings.TrimSpace(next.Path) == "" {
		reason = recovery.WorktreeSwitchExit
	}
	return a.afterWorkDirSwitchWithReason(prev, next, reason)
}

// afterWorkDirSwitchWithReason is afterWorkDirSwitch for callers that know why
// the checkout changed (a worktree tool, a restore): the reason is persisted
// verbatim in the session timeline, so an automatic adoption must not reuse a
// reason that names a user-requested switch.
func (a *MainAgent) afterWorkDirSwitchWithReason(prev, next WorkDirState, reason string) []string {
	a.refreshPathRoots()
	a.ReloadAgentsMD()
	// The injected git status and virtualenv path describe the working
	// directory, so they follow the switch for the same reason AGENTS.md does.
	a.refreshWorkDirDerivedMeta()
	if a.worktreeRT.RefreshSkills != nil {
		a.worktreeRT.RefreshSkills(a.workDir())
	}
	a.refreshSessionContextReminder()
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

// checkoutMutationStateDir resolves the state directory holding the
// cross-process checkout mutation locks. It comes from the locator the
// worktree tools use, so a writer and the removal it races derive the same
// lock; empty means this agent has no worktree runtime and no claim to protect.
func (a *MainAgent) checkoutMutationStateDir() string {
	if a == nil || a.worktreeRT.PathLocator == nil {
		return ""
	}
	return strings.TrimSpace(a.worktreeRT.PathLocator.StateDir)
}

// recordWorkDirBoundary persists the active checkout together with the
// boundary record of the switch that produced it. The switch is already
// published, so a write failure is reported as a warning by the caller and
// never rolls the checkout back.
//
// Recording a checkout takes the mutation lock removal takes, and only while
// the directory is still there. A checkout that a removal already deleted is
// not re-recorded as a claim — the write fails with the removal's error and the
// session keeps the binding it had, so no resume replays a transcript against a
// path nothing is in.
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
		if stateDir := a.checkoutMutationStateDir(); stateDir != "" {
			return worktree.ClaimCheckout(stateDir, path, func() error {
				return recovery.RecordWorktreeBoundary(a.sessionDir, binding, entry)
			})
		}
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
		return a.recordWorktreeResumeDrop(state)
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

// recordWorktreeResumeDrop clears the session's active checkout and appends the
// boundary explaining that the recorded checkout is gone. It returns the
// user-facing notice; the caller keeps its current directory.
func (a *MainAgent) recordWorktreeResumeDrop(state WorkDirState) string {
	entry := recovery.WorktreeTimelineEntry{
		Reason:   recovery.WorktreeSwitchResumeFallback,
		Name:     state.WorktreeID,
		Branch:   state.Branch,
		Path:     state.Path,
		Fallback: true,
		Detail:   fmt.Sprintf("recorded worktree %s is no longer a worktree of this repository", state.Path),
		At:       time.Now().UTC(),
	}
	if err := recovery.RecordWorktreeBoundary(a.sessionDir, recovery.WorktreeBinding{}, entry); err != nil {
		log.Warnf("record worktree resume fallback failed session_dir=%v error=%v", a.sessionDir, err)
	}
	return fmt.Sprintf("session was working in worktree %s, which no longer exists; continuing in %s", state.Path, a.workDir())
}

// recordSessionCheckout writes the checkout the process is working in into the
// session directory that just became current. /new and fork continue in the
// same checkout as the session they replace, but their metadata starts empty:
// without this the switch is recorded nowhere, and a later resume of the new
// session lands in the main checkout and replays the transcript's relative
// paths against a different tree.
func (a *MainAgent) recordSessionCheckout() error {
	if a == nil {
		return nil
	}
	state := a.workDirState.load()
	if strings.TrimSpace(state.Path) == "" {
		// The session runs in the main checkout, which needs no provenance:
		// resume already defaults there.
		return nil
	}
	return a.recordWorkDirBoundary(state, recovery.WorktreeSwitchStartup)
}

// recordSessionCheckoutOrWarn is recordSessionCheckout plus a user-visible
// warning: a failed write is not fatal, but without it a later resume of the
// new session lands in a different checkout than the work it continues.
func (a *MainAgent) recordSessionCheckoutOrWarn() {
	if err := a.recordSessionCheckout(); err != nil {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("working directory is %s but the new session could not record it (%v); resuming that session may land in a different checkout", a.workDir(), err), Level: "warning"})
	}
}

// adoptResumedSessionCheckout switches the process to the checkout a resumed
// session was working in. An in-process /resume replaces the session without
// touching the working directory, so without this the resumed transcript's
// relative paths would be interpreted against the checkout the previous session
// left. The recorded checkout must still be a chord-managed worktree of this
// repository; when it is gone the drop is recorded as a fallback boundary and
// the returned notice explains it. A session that ran in the main checkout has
// no recorded checkout and keeps the current one, matching startup resume.
func (a *MainAgent) adoptResumedSessionCheckout(ctx context.Context, state WorkDirState) string {
	if a == nil || strings.TrimSpace(state.Path) == "" {
		return ""
	}
	info := a.resolveRestoredWorktree(ctx, state.Path)
	if info == nil {
		return a.recordWorktreeResumeDrop(state)
	}
	prev := a.workDirState.load()
	if cleanWorkdirPath(prev.Path) == info.Path {
		return ""
	}
	next := WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: prev.Generation + 1,
	}
	if strings.TrimSpace(next.BaseSHA) == "" {
		if head, err := worktree.HeadCommit(ctx, info.Path); err == nil {
			next.BaseSHA = head
		}
	}
	a.workDirState.store(next)
	if warnings := a.afterWorkDirSwitchWithReason(prev, next, recovery.WorktreeSwitchResume); len(warnings) > 0 {
		log.Warnf("resume worktree switch warnings session=%v warnings=%v", filepath.Base(a.sessionDir), warnings)
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
	// A name is optional unless the branch already carries one: Create derives
	// the name from the branch, so only the "neither given" case needs a
	// generated slug. Keying this off path as well left a path-only call with
	// an empty name, which Create rejects.
	if name == "" && strings.TrimSpace(req.Branch) == "" {
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
	// Create() reuses an existing checkout and reports it through Existed, so
	// entering a worktree another session (or sub-agent) already works in is
	// allowed on purpose: refusing would break resuming a session whose checkout
	// still exists, and the owner metadata records who created a worktree, not
	// who is alive in it. The cost is that both share one working tree and its
	// uncommitted changes, which the Enter tool description and the public usage
	// docs state; there is no runtime occupancy gate.
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
			// guardRemoval already asked once; Remove asks again as late as it
			// can, so a holder that bound this checkout between the two checks
			// is not deleted out from under.
			Holders: act.removalHoldersFor,
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

// worktreeRemovalHolders reports the live work still anchored to one worktree:
// another agent working inside it, or a background command still running
// there. A removed checkout takes their working directory with it, so removal
// waits for them; the answer is never silently empty because something looked
// unverifiable.
//
// Holders are matched on the directory an agent actually works in, not only on
// the binding it recorded. A binding is not the whole story: leaving a worktree
// with action "keep" clears it while the process keeps running in that
// directory, and a session started from inside a checkout never had one. The
// binding name is kept as an additional signal so a checkout recorded under a
// differently spelled path (for example through a symlink) is still refused
// rather than deleted under a live agent.
func (a *MainAgent) worktreeRemovalHolders(info *worktree.Info) []string {
	if a == nil || info == nil || strings.TrimSpace(info.Path) == "" {
		return []string{"the worktree path is unknown"}
	}
	var holders []string
	// The caller's own binding is checked before this (active / currentDir),
	// but a worker reclaiming its own checkout would otherwise delete the
	// directory the session's main agent is working in.
	if a.workDirState.load().WorktreeID == info.Name || dirInWorktree(a.workDir(), info.Path) {
		holders = append(holders, "the session's main agent is still working there")
	}
	a.subs.mu.RLock()
	for _, sub := range a.subs.subAgents {
		if sub == nil {
			continue
		}
		if !isNonTerminalTaskState(string(sub.State())) {
			continue
		}
		if sub.workDirState.load().WorktreeID != info.Name && !dirInWorktree(sub.effectiveToolBaseDir(), info.Path) {
			continue
		}
		holders = append(holders, fmt.Sprintf("agent %s is still working there (%s)", sub.instanceID, sub.State()))
	}
	a.subs.mu.RUnlock()
	for _, job := range tools.RunningJobsInDir(info.Path) {
		holders = append(holders, fmt.Sprintf("background job %s is still running there", job.ID))
	}
	return holders
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
	// Ownership answers who may delete; this answers whether anything is still
	// using the directory. It is deliberately checked before discard_changes,
	// which consents to losing the tree's contents, not to pulling the
	// directory out from under a running agent or command.
	if holders := act.removalHoldersFor(info); len(holders) > 0 {
		return fmt.Errorf("worktree %q is still in use: %s; finish or stop that work before removing the checkout", info.Name, strings.Join(holders, "; "))
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
	// Dirty state and owner are probed one worktree at a time: this list backs a
	// user-triggered diagnostic, the worktree count of one repository is small,
	// and a failed probe degrades to "unknown" instead of failing the list.
	// Batching (`git worktree list --porcelain` for topology, then dirty state in
	// one call or in the background) would change what the caller sees and is
	// only worth it if repositories with many worktrees show up.
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
