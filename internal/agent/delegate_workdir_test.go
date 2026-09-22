package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
)

func TestResolveDelegateWorkDirByNameAndPath(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-resolve")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-delegate"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-delegate"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}

	byName, err := a.resolveDelegateWorkDir(ctx, "feat-delegate")
	if err != nil || byName == nil {
		t.Fatalf("resolve by name = %#v, %v", byName, err)
	}
	if byName.Path != res.Path || byName.Name != "feat-delegate" || byName.Branch != res.Branch {
		t.Fatalf("resolve by name = %#v, want worktree %#v", byName, res)
	}

	byPath, err := a.resolveDelegateWorkDir(ctx, res.Path)
	if err != nil || byPath == nil {
		t.Fatalf("resolve by path = %#v, %v", byPath, err)
	}
	if byPath.Path != res.Path || byPath.Name != "feat-delegate" {
		t.Fatalf("resolve by path = %#v, want worktree %#v", byPath, res)
	}

	// An empty argument means "inherit the delegating agent's directory", so it
	// resolves to no worktree rather than an error.
	if got, err := a.resolveDelegateWorkDir(ctx, "   "); err != nil || got != nil {
		t.Fatalf("empty workdir = %#v, %v; want nil, nil", got, err)
	}
}

func TestResolveDelegateWorkDirFailsFast(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-resolve")

	if _, err := a.resolveDelegateWorkDir(ctx, "feat-missing"); err == nil || !strings.Contains(err.Error(), "not an existing worktree") {
		t.Fatalf("missing worktree err = %v, want a fail-fast rejection", err)
	}
	// A path spelling that is not one of this repository's worktrees is
	// rejected the same way, not silently accepted as a directory.
	if _, err := a.resolveDelegateWorkDir(ctx, filepath.Join(t.TempDir(), "not-a-worktree")); err == nil {
		t.Fatal("expected a non-worktree path to be rejected")
	}

	// Without injected worktree services a workdir argument cannot be resolved
	// at all, because the repository topology is unknown.
	plain := newTestMainAgent(t, t.TempDir())
	if _, err := plain.resolveDelegateWorkDir(ctx, "feat-missing"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable err = %v", err)
	}
	if got, err := plain.resolveDelegateWorkDir(ctx, ""); err != nil || got != nil {
		t.Fatalf("empty workdir without runtime = %#v, %v; want nil, nil", got, err)
	}
}

func TestDelegationCallerInheritsSubCurrentWorktree(t *testing.T) {
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

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-inherit"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	if got := sub.effectiveToolBaseDir(); got != res.Path {
		t.Fatalf("sub base dir = %q, want %q", got, res.Path)
	}

	// A worker that entered a worktree must delegate into its current
	// checkout, not the directory it was created in.
	caller, err := a.delegationCallerFromContext(tools.WithAgentID(ctx, sub.instanceID))
	if err != nil {
		t.Fatalf("delegationCallerFromContext(sub): %v", err)
	}
	if caller.IsMain {
		t.Fatalf("caller = %#v, want the sub caller", caller)
	}
	if caller.WorkDir != res.Path {
		t.Fatalf("caller workdir = %q, want the sub's current checkout %q", caller.WorkDir, res.Path)
	}
}

func TestCreateSubAgentBindsRequestedWorktree(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-delegate")
	configureNestedDelegationTestRuntime(a, 1)
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-worker"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-worker"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}

	handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child work", AgentType: "worker", WorkDir: "feat-worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected the child SubAgent to exist")
	}
	if got := child.effectiveToolBaseDir(); got != res.Path {
		t.Fatalf("child base dir = %q, want %q", got, res.Path)
	}
	state := child.workDirState.load()
	if state.Path != res.Path || state.WorktreeID != "feat-worker" {
		t.Fatalf("child binding = %#v, want feat-worker at %s", state, res.Path)
	}
	if state.Branch != worktree.DefaultBranchPrefix+"feat-worker" {
		t.Fatalf("child branch = %q, want %q", state.Branch, worktree.DefaultBranchPrefix+"feat-worker")
	}
	if strings.TrimSpace(state.BaseSHA) == "" {
		t.Fatalf("child binding has no base commit: %#v", state)
	}
	assertEnvBlockStatesWorktree(t, subAgentReminderContent(t, child), res.Path, "feat-worker", state.Branch)
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
		t.Fatalf("child binding = %#v, want none when no worktree was requested", state)
	}
}

func TestCreateSubAgentRejectsUnknownWorkDirBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-delegate-missing")
	configureNestedDelegationTestRuntime(a, 1)

	_, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "child work", AgentType: "worker", WorkDir: "feat-nope"})
	if err == nil || !strings.Contains(err.Error(), "not an existing worktree") {
		t.Fatalf("CreateSubAgent err = %v, want a fail-fast unknown-worktree error", err)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("a rejected workdir consumed %d runtime slots", got)
	}
}

func TestSessionEnvSnapshotReportsActiveWorktree(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-env")

	before := a.sessionEnvSnapshot()
	if before.WorktreeName != "" || before.WorktreeBranch != "" {
		t.Fatalf("env before a switch = %#v, want no worktree", before)
	}
	if before.WorkDir != repo {
		t.Fatalf("env workdir = %q, want %q", before.WorkDir, repo)
	}

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-env"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	after := a.sessionEnvSnapshot()
	if after.WorkDir != res.Path || after.WorktreeName != "feat-env" || after.WorktreeBranch != res.Branch {
		t.Fatalf("env after enter = %#v, want worktree feat-env at %s", after, res.Path)
	}

	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-env"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	cleared := a.sessionEnvSnapshot()
	if cleared.WorktreeName != "" || cleared.WorktreeBranch != "" || cleared.WorkDir != repo {
		t.Fatalf("env after exit = %#v, want the startup directory and no worktree", cleared)
	}
}

func TestRecordWorkDirBoundaryPersistsSwitchTimeline(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-timeline")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-line"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-line"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}

	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", meta, err)
	}
	if meta.WorktreePath != "" || meta.WorktreeName != "" {
		t.Fatalf("exit should leave no active worktree recorded: %+v", meta)
	}
	if len(meta.WorktreeTimeline) < 2 {
		t.Fatalf("timeline = %+v, want enter and exit boundaries", meta.WorktreeTimeline)
	}
	enter, exit := meta.WorktreeTimeline[0], meta.WorktreeTimeline[1]
	if enter.Reason != recovery.WorktreeSwitchEnter || enter.Path != res.Path || enter.Name != "feat-line" {
		t.Fatalf("enter boundary = %+v", enter)
	}
	if enter.Head == "" || enter.Generation == 0 {
		t.Fatalf("enter boundary should record the checkout HEAD and generation: %+v", enter)
	}
	if exit.Reason != recovery.WorktreeSwitchExit {
		t.Fatalf("exit boundary = %+v", exit)
	}
}

func TestRestoreWorkDirBindingInstallsLiveWorktree(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-restore")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-restore"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-restore"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}

	notice := a.RestoreWorkDirBinding(ctx, WorkDirState{Path: res.Path, WorktreeID: "feat-restore", Branch: res.Branch}, recovery.WorktreeSwitchResume)
	if notice != "" {
		t.Fatalf("notice = %q, want none for a live worktree", notice)
	}
	if got := a.effectiveToolBaseDir(); got != res.Path {
		t.Fatalf("base dir = %q, want %q", got, res.Path)
	}
	state := a.workDirState.load()
	if state.Path != res.Path || state.WorktreeID != "feat-restore" {
		t.Fatalf("restored binding = %#v, want feat-restore at %s", state, res.Path)
	}
	if strings.TrimSpace(state.BaseSHA) == "" {
		t.Fatalf("restored binding should resolve a base commit: %#v", state)
	}

	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil || meta == nil {
		t.Fatalf("LoadSessionMeta: %+v, %v", meta, err)
	}
	if meta.WorktreePath != res.Path || meta.WorktreeName != "feat-restore" {
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
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-complete")
	sub := newControllableTestSubAgent(t, a, "task-complete")

	// Outside a worktree there is no checkout to report.
	if wt := sub.completionWorktreeSnapshot(); wt != nil {
		t.Fatalf("snapshot outside a worktree = %#v, want nil", wt)
	}
	plain := sub.enrichCompletionResult(&AgentResult{Summary: "done"})
	if plain.Envelope == nil || plain.Envelope.Worktree != nil {
		t.Fatalf("envelope outside a worktree = %#v, want no worktree block", plain.Envelope)
	}

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-complete"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := sub.completionWorktreeSnapshot()
	if wt == nil {
		t.Fatal("snapshot in a worktree = nil, want the active checkout")
	}
	if wt.Name != "feat-complete" || wt.Path != res.Path || wt.Branch != res.Branch {
		t.Fatalf("snapshot = %#v, want feat-complete at %s", wt, res.Path)
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
	if enriched.Envelope.Worktree.Name != "feat-complete" || enriched.Envelope.Worktree.Path != res.Path {
		t.Fatalf("envelope worktree = %#v", enriched.Envelope.Worktree)
	}
}

func TestSubAgentCompletionWorktreeDiffErrorIsReported(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-complete-error")
	sub := newControllableTestSubAgent(t, a, "task-complete-error")

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-broken"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	// Rewrite the binding with a base that no longer resolves: the snapshot must
	// report the failure instead of silently claiming there were no changes.
	state := sub.workDirState.load()
	state.BaseSHA = "0000000000000000000000000000000000000000"
	sub.workDirState.store(state)

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
	if wt.Path != res.Path {
		t.Fatalf("snapshot path = %q, want %q", wt.Path, res.Path)
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
