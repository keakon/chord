package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestRenderEnvBlockIncludesWorktreeLine(t *testing.T) {
	got := buildSessionContextReminder(SessionEnvSnapshot{
		WorkDir:        "/state/worktrees/feat",
		WorktreeName:   "feat",
		WorktreeBranch: "chord/feat",
		Platform:       "test/os",
		Date:           "Apr 17 2026",
	}, "")
	for _, want := range []string{
		"Working directory: /state/worktrees/feat",
		"Worktree: feat (branch chord/feat)",
		"separate checkout",
		"relative paths resolve inside it",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("env block missing %q: %q", want, got)
		}
	}

	// A main checkout has no worktree identity to report.
	plain := buildSessionContextReminder(SessionEnvSnapshot{WorkDir: "/repo", Platform: "test/os", Date: "Apr 17 2026"}, "")
	if strings.Contains(plain, "Worktree:") {
		t.Fatalf("main-checkout env block should not carry a Worktree line: %q", plain)
	}

	// A named worktree without a branch still renders the name.
	branchless := buildSessionContextReminder(SessionEnvSnapshot{WorkDir: "/state/worktrees/feat", WorktreeName: "feat", Date: "Apr 17 2026"}, "")
	if !strings.Contains(branchless, "Worktree: feat\n") {
		t.Fatalf("branchless worktree line = %q, want the bare name", branchless)
	}
}

func TestHasEnvIncludesWorktreeIdentity(t *testing.T) {
	if (SessionEnvSnapshot{WorktreeName: "feat"}).hasEnv() != true {
		t.Fatal("a worktree identity alone must count as environment content")
	}
	if (SessionEnvSnapshot{}).hasEnv() {
		t.Fatal("an empty snapshot must not count as environment content")
	}
}

func subAgentReminderContent(t *testing.T, s *SubAgent) string {
	t.Helper()
	ptr := s.cachedSessionReminderContent.Load()
	if ptr == nil {
		t.Fatal("SubAgent has no session-context reminder")
	}
	return *ptr
}

func assertEnvBlockStatesWorktree(t *testing.T, content, workDir, name, branch string) {
	t.Helper()
	for _, want := range []string{
		"Working directory: " + workDir,
		"Worktree: " + name + " (branch " + branch + ")",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("subagent reminder missing %q:\n%s", want, content)
		}
	}
}

// TestSubAgentSessionReminderNamesInheritedWorktree covers the common case: the
// owner works inside a worktree and delegates without naming a workdir, so the
// worker runs in that checkout and its reminder must say so.
func TestSubAgentSessionReminderNamesInheritedWorktree(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-sub-env")
	configureNestedDelegationTestRuntime(a, 1)

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-env"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "work", AgentType: "worker"})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	child := a.subAgentByTaskID(handle.TaskID)
	if child == nil {
		t.Fatal("expected the child SubAgent to exist")
	}
	if state := child.workDirState.load(); state.WorktreeID != "feat-env" {
		t.Fatalf("child binding = %#v, want the inherited feat-env", state)
	}
	assertEnvBlockStatesWorktree(t, subAgentReminderContent(t, child), res.Path, "feat-env", res.Branch)
}

// TestSubAgentSessionReminderFollowsWorktreeSwitch pins the live side of the
// reminder: a worker that switches checkout mid-task must not keep describing
// the directory it left in every later request.
func TestSubAgentSessionReminderFollowsWorktreeSwitch(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-sub-switch")
	sub := newControllableTestSubAgent(t, a, "task-switch")

	before := subAgentReminderContent(t, sub)
	if !strings.Contains(before, "Working directory: "+repo) {
		t.Fatalf("initial reminder missing startup dir %q:\n%s", repo, before)
	}
	if strings.Contains(before, "Worktree:") {
		t.Fatalf("initial reminder should not claim a worktree:\n%s", before)
	}

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-switch"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	assertEnvBlockStatesWorktree(t, subAgentReminderContent(t, sub), res.Path, "feat-switch", res.Branch)

	if _, err := sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-switch"}); err != nil {
		t.Fatalf("sub WorktreeExit: %v", err)
	}
	after := subAgentReminderContent(t, sub)
	if !strings.Contains(after, "Working directory: "+repo) {
		t.Fatalf("reminder after exit missing startup dir %q:\n%s", repo, after)
	}
	if strings.Contains(after, "Worktree:") {
		t.Fatalf("reminder after exit still claims a worktree:\n%s", after)
	}
}
