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
	// BaseSHA is the commit the binding started from: the checkout's recorded
	// base when the session installed its binding, or its HEAD when a worker
	// inherited a checkout directly. Completion reports diff against it, so it
	// must stay with the binding rather than being re-derived from the branch
	// later.
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

// WorktreeRuntime carries the cmd-layer services the worktree binding needs.
// internal/agent never resolves storage or git topology on its own; cmd/chord
// injects this bundle before the agent starts running turns.
type WorktreeRuntime struct {
	// PathLocator resolves the state directory worktrees are created under.
	PathLocator *config.PathLocator
	// RepoRoot is the repository content root (the main checkout).
	RepoRoot string
	// BranchPrefix scopes chord-managed branches; empty means the default.
	BranchPrefix string
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

// publishWorkDirChange tells surfaces outside this agent that the active
// checkout changed. The event is an invalidation carrying the new generation:
// consumers read the whole state themselves, so a notification that races a
// later switch still converges on the newest release. A store that did not
// change the binding (re-entering the directory already active, a session in
// the main checkout) sends nothing.
func (a *MainAgent) publishWorkDirChange(prev, next WorkDirState) {
	if a == nil || prev == next {
		return
	}
	a.emitToTUI(WorkDirChangedEvent{Generation: next.Generation})
}

// WorkDirSnapshot reports the display-relevant view of this agent's active
// checkout in one read: the effective working directory plus the worktree
// identity and generation describing it. Callers that compare or render the
// checkout must use this instead of combining WorkDir() with a separate
// identity read, because two reads can straddle a switch.
func (a *MainAgent) WorkDirSnapshot() WorkDirSnapshot {
	if a == nil {
		return WorkDirSnapshot{}
	}
	// Single atomic binding read: the path fallback (cachedWorkDir,
	// contentRoot) is derived from the same loaded state while holding
	// promptMetaMu, so a concurrent switch cannot splice the path of one
	// release with the identity of another.
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	state := a.workDirState.load()
	path := strings.TrimSpace(state.Path)
	if path == "" {
		if dir := strings.TrimSpace(a.cachedWorkDir); dir != "" {
			path = dir
		} else {
			path = a.contentRoot
		}
	}
	return WorkDirSnapshot{
		Path:       path,
		WorktreeID: state.WorktreeID,
		Generation: state.Generation,
	}
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

// afterWorkDirSwitch refreshes every workDir-derived surface after a published
// switch. Failures here are warnings, never rollbacks: an agent that switched
// but could not rebind LSP is still better off than one stuck in the previous
// checkout. reason is persisted verbatim in the session timeline, so an
// automatic adoption must not reuse a reason that names a user-requested
// switch.
func (a *MainAgent) afterWorkDirSwitch(prev, next WorkDirState, reason string) []string {
	a.invalidatePathRoots()
	a.ReloadAgentsMD()
	// The injected git status and virtualenv path describe the working
	// directory, so they follow the switch for the same reason AGENTS.md does.
	a.refreshWorkDirDerivedMeta()
	if a.worktreeRT.RefreshSkills != nil {
		a.worktreeRT.RefreshSkills(a.workDir())
	}
	// The reminder is rebuilt for the new checkout anyway, so a pending Memory
	// commit rides along.
	if _, flipped := a.applyLoadedMemory(); flipped {
		a.markRuntimeSurfaceDirty()
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
// worktree commands use, so a writer and the removal it races derive the same
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
// because it was created in a chord-managed worktree or because resume recorded
// one. The directory must still be a chord-managed worktree of this repository;
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
	prev := a.workDirState.load()
	a.workDirState.store(state)
	a.publishWorkDirChange(prev, state)
	// Installing the binding is a publication like any other: without this a
	// session that starts in a worktree would inject the startup checkout's
	// branch for its whole life.
	a.refreshWorkDirDerivedMeta()
	if err := a.recordWorkDirBoundary(state, reason); err != nil {
		log.Warnf("record restored worktree binding failed session_dir=%v error=%v", a.sessionDir, err)
	}
	return ""
}

// recordWorktreeResumeDrop handles a recorded checkout that could not be
// restored: it clears the session's active checkout and appends the boundary
// explaining that the recorded checkout is gone, and returns the user-facing
// notice. When git is not available the checkout could not be checked at all,
// so the record is kept for a later resume and the notice says so instead of
// claiming the checkout is gone.
func (a *MainAgent) recordWorktreeResumeDrop(state WorkDirState) string {
	if !worktree.GitAvailable() {
		return fmt.Sprintf("session was working in worktree %s, but git is not available to verify it; continuing in %s", state.Path, a.workDir())
	}
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
	a.publishWorkDirChange(prev, next)
	if warnings := a.afterWorkDirSwitch(prev, next, recovery.WorktreeSwitchResume); len(warnings) > 0 {
		log.Warnf("resume worktree switch warnings session=%v warnings=%v", filepath.Base(a.sessionDir), warnings)
	}
	return ""
}
