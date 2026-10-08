package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestDelegationCallerInheritsSubCurrentCheckout(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-caller")
	sub := newControllableTestSubAgent(t, a, "task-inherit")

	mainCaller, err := a.delegationCallerFromContext(tools.WithAgentID(ctx, ""))
	if err != nil {
		t.Fatalf("delegationCallerFromContext(main): %v", err)
	}
	if !mainCaller.IsMain || mainCaller.WorkDir != repo {
		t.Fatalf("main caller = %#v, want IsMain with workdir %q", mainCaller, repo)
	}

	info := createTestWorktree(t, a, "feat-inherit")
	sub.workDirState.store(WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: 1,
	})
	if got := sub.effectiveToolBaseDir(); got != info.Path {
		t.Fatalf("sub base dir = %q, want %q", got, info.Path)
	}

	// A worker that moved into a checkout must delegate into its current
	// checkout, not the directory it was created in.
	caller, err := a.delegationCallerFromContext(tools.WithAgentID(ctx, sub.instanceID))
	if err != nil {
		t.Fatalf("delegationCallerFromContext(sub): %v", err)
	}
	if caller.IsMain {
		t.Fatalf("caller = %#v, want the sub caller", caller)
	}
	if caller.WorkDir != info.Path {
		t.Fatalf("caller workdir = %q, want the sub's current checkout %q", caller.WorkDir, info.Path)
	}
	if caller.WorkDirState.WorktreeID != info.Name {
		t.Fatalf("caller binding = %#v, want the sub's checkout identity", caller.WorkDirState)
	}
}

func TestCreateSubAgentInheritsCallerWorkDirByDefault(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-inherit-delegate")
	configureNestedDelegationTestRuntime(a, 1)

	handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected the child SubAgent to exist")
	}
	if got := child.effectiveToolBaseDir(); got != repo {
		t.Fatalf("child base dir = %q, want the inherited %q", got, repo)
	}
	if state := child.workDirState.load(); state.Path != "" || state.WorktreeID != "" {
		t.Fatalf("child binding = %#v, want none in the main checkout", state)
	}
}

// TestCreateSubAgentInheritsCallerCheckout pins the delegation rule that
// replaced the workdir argument: a worker runs in the checkout its caller is
// working in, identity included, so its relative paths and environment block
// describe the same tree.
func TestCreateSubAgentInheritsCallerCheckout(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-inherit-checkout")
	configureNestedDelegationTestRuntime(a, 1)

	installed := installTestCheckout(t, a, "feat-inherited")
	handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected the child SubAgent to exist")
	}
	if got := child.effectiveToolBaseDir(); got != installed.Path {
		t.Fatalf("child base dir = %q, want the inherited checkout %q", got, installed.Path)
	}
	state := child.workDirState.load()
	if state.Path != installed.Path || state.WorktreeID != installed.WorktreeID || state.Branch != installed.Branch {
		t.Fatalf("child binding = %#v, want the inherited checkout %s", state, installed.Path)
	}
	if state.BaseSHA != installed.BaseSHA {
		t.Fatalf("child base commit = %q, want the caller's %q", state.BaseSHA, installed.BaseSHA)
	}
	assertEnvBlockStatesWorktree(t, subAgentReminderContent(t, child), installed.Path, installed.WorktreeID, installed.Branch)
}

func TestSessionEnvSnapshotReportsActiveWorktree(t *testing.T) {
	a, repo := newWorktreeTestAgent(t, "session-env")

	before := a.sessionEnvSnapshot()
	if before.WorktreeName != "" || before.WorktreeBranch != "" {
		t.Fatalf("env before a switch = %#v, want no worktree", before)
	}
	if before.WorkDir != repo {
		t.Fatalf("env workdir = %q, want %q", before.WorkDir, repo)
	}

	installed := installTestCheckout(t, a, "feat-env")
	after := a.sessionEnvSnapshot()
	if after.WorkDir != installed.Path || after.WorktreeName != "feat-env" || after.WorktreeBranch != installed.Branch {
		t.Fatalf("env after the switch = %#v, want worktree feat-env at %s", after, installed.Path)
	}
}

// TestRecordWorkDirBoundaryPersistsSwitchTimeline pins the persisted boundary
// of an installed checkout: the session metadata names the checkout and the
// timeline records the HEAD and generation a later resume reasons about.
func TestRecordWorkDirBoundaryPersistsSwitchTimeline(t *testing.T) {
	a, _ := newWorktreeTestAgent(t, "session-timeline")

	installed := installTestCheckout(t, a, "feat-line")

	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", meta, err)
	}
	if meta.WorktreePath != installed.Path || meta.WorktreeName != "feat-line" || meta.WorktreeBranch != installed.Branch {
		t.Fatalf("recorded checkout = %q %q %q, want %s", meta.WorktreePath, meta.WorktreeName, meta.WorktreeBranch, installed.Path)
	}
	last := meta.WorktreeTimeline[len(meta.WorktreeTimeline)-1]
	if last.Reason != recovery.WorktreeSwitchResume || last.Path != installed.Path || last.Name != "feat-line" {
		t.Fatalf("boundary = %+v, want a resume boundary for the installed checkout", last)
	}
	if last.Head == "" || last.Generation != installed.Generation {
		t.Fatalf("boundary should record the checkout HEAD and generation: %+v", last)
	}
}

func TestRestoreWorkDirBindingInstallsLiveWorktree(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-restore")
	info := createTestWorktree(t, a, "feat-restore")

	notice := a.RestoreWorkDirBinding(ctx, WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		Generation: 1,
	}, recovery.WorktreeSwitchResume)
	if notice != "" {
		t.Fatalf("notice = %q, want none for a live worktree", notice)
	}
	if got := a.effectiveToolBaseDir(); got != info.Path {
		t.Fatalf("base dir = %q, want %q", got, info.Path)
	}
	state := a.workDirState.load()
	if state.Path != info.Path || state.WorktreeID != "feat-restore" || state.Branch != info.Branch {
		t.Fatalf("restored binding = %#v, want feat-restore at %s", state, info.Path)
	}
	if strings.TrimSpace(state.BaseSHA) == "" {
		t.Fatalf("restored binding should resolve a base commit: %#v", state)
	}

	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", meta, err)
	}
	if meta.WorktreePath != info.Path || meta.WorktreeName != "feat-restore" {
		t.Fatalf("restore did not persist the active checkout: %+v", meta)
	}
	last := meta.WorktreeTimeline[len(meta.WorktreeTimeline)-1]
	if last.Reason != recovery.WorktreeSwitchResume {
		t.Fatalf("last timeline entry = %+v, want a resume boundary", last)
	}
}

func TestRestoreWorkDirBindingFallsBackWhenWorktreeIsGone(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-restore-gone")
	missing := filepath.Join(repo, "vanish")

	notice := a.RestoreWorkDirBinding(ctx, WorkDirState{Path: missing, WorktreeID: "vanish", Branch: "chord/vanish"}, recovery.WorktreeSwitchResume)
	if !strings.Contains(notice, "no longer exists") {
		t.Fatalf("notice = %q, want a fallback message", notice)
	}
	if got := a.effectiveToolBaseDir(); got != repo {
		t.Fatalf("base dir = %q, want the startup directory %q after a fallback", got, repo)
	}
	if state := a.workDirState.load(); state.Path != "" {
		t.Fatalf("binding = %#v, want none after a fallback", state)
	}

	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", meta, err)
	}
	last := meta.WorktreeTimeline[len(meta.WorktreeTimeline)-1]
	if last.Reason != recovery.WorktreeSwitchResumeFallback || !last.Fallback || last.Path != missing {
		t.Fatalf("fallback boundary = %+v", last)
	}
}

func TestSubAgentCompletionReportsWorktree(t *testing.T) {
	a, _ := newWorktreeTestAgent(t, "session-complete")

	// Outside a checkout there is no tree to report.
	plain := newControllableTestSubAgent(t, a, "task-plain")
	if wt := plain.completionWorktreeSnapshot(); wt != nil {
		t.Fatalf("snapshot outside a checkout = %#v, want nil", wt)
	}
	if envelope := plain.enrichCompletionResult(&AgentResult{Summary: "done"}).Envelope; envelope == nil || envelope.Worktree != nil {
		t.Fatalf("envelope outside a checkout = %#v, want no worktree block", envelope)
	}

	info := createTestWorktree(t, a, "feat-complete")
	sub := newControllableTestSubAgentWithWorkDir(t, a, "task-complete", WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: 1,
	})
	if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := sub.completionWorktreeSnapshot()
	if wt == nil {
		t.Fatal("snapshot in a checkout = nil, want the active checkout")
	}
	if wt.Name != "feat-complete" || wt.Path != info.Path || wt.Branch != info.Branch {
		t.Fatalf("snapshot = %#v, want feat-complete at %s", wt, info.Path)
	}
	if strings.TrimSpace(wt.Base) == "" {
		t.Fatalf("snapshot has no base commit: %#v", wt)
	}
	if wt.DiffError != "" {
		t.Fatalf("snapshot diff error = %q, want none", wt.DiffError)
	}
	if !strings.Contains(wt.DiffStat, "README.md") {
		t.Fatalf("diff stat = %q, want the edited file", wt.DiffStat)
	}

	enriched := sub.enrichCompletionResult(&AgentResult{Summary: "done"})
	if enriched.Envelope == nil || enriched.Envelope.Worktree == nil {
		t.Fatalf("envelope = %#v, want a worktree block", enriched.Envelope)
	}
	if enriched.Envelope.Worktree.Name != "feat-complete" || enriched.Envelope.Worktree.Path != info.Path {
		t.Fatalf("envelope worktree = %#v", enriched.Envelope.Worktree)
	}
}

func TestSubAgentCompletionWorktreeDiffErrorIsReported(t *testing.T) {
	a, _ := newWorktreeTestAgent(t, "session-complete-error")
	info := createTestWorktree(t, a, "feat-broken")
	sub := newControllableTestSubAgentWithWorkDir(t, a, "task-complete-error", WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		// A base that no longer resolves: the snapshot must report the failure
		// instead of silently claiming there were no changes.
		BaseSHA:    "0000000000000000000000000000000000000000",
		Generation: 1,
	})

	wt := sub.completionWorktreeSnapshot()
	if wt == nil {
		t.Fatal("snapshot = nil, want the active checkout even when the diff fails")
	}
	if wt.DiffError == "" {
		t.Fatalf("snapshot = %#v, want a diff error for an unknown base", wt)
	}
	if wt.DiffStat != "" {
		t.Fatalf("diff stat = %q, want it empty when the diff failed", wt.DiffStat)
	}
	if wt.Path != info.Path {
		t.Fatalf("snapshot path = %q, want %q", wt.Path, info.Path)
	}
}

func TestMailboxInjectionTextRendersWorktree(t *testing.T) {
	msg := &SubAgentMailboxMessage{
		AgentID: "agent-7",
		TaskID:  "adhoc-7",
		Completion: &CompletionEnvelope{
			Summary: "done",
			Worktree: &CompletionWorktree{
				Name:       "feat-x",
				Branch:     "chord/feat-x",
				Path:       "/state/worktrees/feat-x",
				Base:       "abc123",
				Generation: 2,
				DiffStat:   "a.txt | 1 +\n b.txt | 2 ++",
			},
		},
	}
	text := formatSubAgentMailboxInjectionText(msg)
	for _, want := range []string{
		"- worktree: feat-x (branch chord/feat-x)",
		"- worktree_path: /state/worktrees/feat-x",
		"- worktree_base: abc123",
		"- worktree_generation: 2",
		"- worktree_diff_stat:",
		"a.txt | 1 +",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("mailbox text missing %q:\n%s", want, text)
		}
	}

	// A failed diff is surfaced as unknown, never as an empty change set.
	msg.Completion.Worktree.DiffStat = ""
	msg.Completion.Worktree.DiffError = "unknown revision"
	failed := formatSubAgentMailboxInjectionText(msg)
	if !strings.Contains(failed, "- worktree_diff_stat: unknown (unknown revision)") {
		t.Fatalf("failed diff not surfaced:\n%s", failed)
	}
}

// TestSubAgentInstructionsFollowItsStartingCheckout pins which AGENTS.md a
// spawned worker follows. A worker starting in the parent's own checkout keeps
// the parent's snapshot, so mid-session edits stay invisible exactly as they do
// for the parent. A worker starting in another checkout reads that checkout's
// file, because the parent's snapshot describes a different tree — the path a
// rehydrated worker takes.
func TestSubAgentInstructionsFollowItsStartingCheckout(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-agents")
	configureNestedDelegationTestRuntime(a, 1)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("root instructions\n"), 0o644); err != nil {
		t.Fatalf("write main checkout AGENTS.md: %v", err)
	}
	a.ReloadAgentsMD()

	// A worker that starts in the parent's own checkout keeps the session
	// snapshot instead of re-reading the file.
	inherited, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "inherited work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent (inherited): %v", err)
	}
	if got := a.subAgentByTaskID(inherited.TaskID).agentsMDSnapshot(); !strings.Contains(got, "root instructions") {
		t.Fatalf("inherited worker AGENTS.md = %q, want the session snapshot", got)
	}

	info := createTestWorktree(t, a, "feat-agents")
	if err := os.WriteFile(filepath.Join(info.Path, "AGENTS.md"), []byte("checkout instructions\n"), 0o644); err != nil {
		t.Fatalf("write checkout AGENTS.md: %v", err)
	}
	// The parent is still in the main checkout, so a worker starting in the
	// checkout must read the checkout's file rather than reuse the snapshot.
	if got := a.subAgentAgentsMD(info.Path); !strings.Contains(got, "checkout instructions") || strings.Contains(got, "root instructions") {
		t.Fatalf("worker instructions for another checkout = %q, want the checkout's file", got)
	}

	// Once the parent works in the checkout, a delegated worker inherits it and
	// keeps the parent's (now checkout-specific) snapshot.
	installed := installTestCheckout(t, a, "feat-agents")
	a.ReloadAgentsMD()
	own, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "checkout own", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent (own file): %v", err)
	}
	child := a.subAgentByTaskID(own.TaskID)
	if got := child.workDirState.load().Path; got != installed.Path {
		t.Fatalf("inherited worker checkout = %q, want %q", got, installed.Path)
	}
	if got := child.agentsMDSnapshot(); !strings.Contains(got, "checkout instructions") || strings.Contains(got, "root instructions") {
		t.Fatalf("worker in the parent's checkout = %q, want the checkout's instructions", got)
	}
	// The reminder is what the model actually reads on every request, so the
	// checkout's instructions must reach it.
	if reminder := subAgentReminderContent(t, child); !strings.Contains(reminder, "checkout instructions") {
		t.Fatalf("child reminder did not carry the checkout's AGENTS.md, got:\n%s", reminder)
	}
}
