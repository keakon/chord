package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
)

// agentTestGitEnv pins identity and silences prompts so `git commit` works on
// CI runners without a global git config.
var agentTestGitEnv = []string{
	"GIT_TERMINAL_PROMPT=0",
	"GIT_ASKPASS=",
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.invalid",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.invalid",
}

func runAgentTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), agentTestGitEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// agentTestGitOutput returns the trimmed stdout of one git command.
func agentTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), agentTestGitEnv...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

// newWorktreeTestRepo creates a single-commit git repository and returns its
// canonical path, matching the spelling worktree.Create reports.
func newWorktreeTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	dir := t.TempDir()
	runAgentTestGit(t, dir, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runAgentTestGit(t, dir, "add", "README.md")
	runAgentTestGit(t, dir, "commit", "-q", "-m", "init")
	canonical, err := config.CanonicalProjectRoot(dir)
	if err != nil {
		t.Fatalf("canonical root: %v", err)
	}
	return canonical
}

func newWorktreeTestLocator(t *testing.T) *config.PathLocator {
	t.Helper()
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	sessionsDir := filepath.Join(stateDir, "sessions")
	for _, p := range []string{stateDir, sessionsDir, filepath.Join(base, "cache"), filepath.Join(base, "config"), filepath.Join(base, "logs")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	return &config.PathLocator{
		ConfigHome:   filepath.Join(base, "config"),
		StateDir:     stateDir,
		CacheDir:     filepath.Join(base, "cache"),
		SessionsRoot: sessionsDir,
		LogsDir:      filepath.Join(base, "logs"),
		ExportsDir:   filepath.Join(stateDir, "exports"),
	}
}

// newWorktreeTestAgent builds a MainAgent whose repository, worktree root and
// session id are all isolated per test, with WriteTool registered the way the
// runtime registers it (anchored to the startup checkout).
func newWorktreeTestAgent(t *testing.T, sessionID string) (*MainAgent, string) {
	t.Helper()
	repo := newWorktreeTestRepo(t)
	return newWorktreeTestAgentOn(t, repo, sessionID), repo
}

// newWorktreeTestAgentOn is newWorktreeTestAgent on an existing test
// repository, so one test can model a second process sharing the same session
// directory and worktrees.
func newWorktreeTestAgentOn(t *testing.T, repo, sessionID string) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, repo)
	a.SetWorktreeRuntime(WorktreeRuntime{
		PathLocator:  newWorktreeTestLocator(t),
		RepoRoot:     repo,
		BranchPrefix: worktree.DefaultBranchPrefix,
		SessionID:    sessionID,
	})
	a.tools.Register(tools.WriteTool{BaseDir: repo})
	return a
}

func writeThroughPipeline(t *testing.T, p toolExecutionPipeline, name string) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"path": name, "content": name})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := p.executeToolForCall(context.Background(), message.ToolCall{Name: tools.NameWrite, Args: args}); err != nil {
		t.Fatalf("execute write %s: %v", name, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s = %q, want %q", path, data, want)
	}
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s exists, want it absent", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

// TestWorktreeSwitchRefreshesInjectedGitStatus pins the per-checkout facts the
// runtime injects into every request — the git status that names the branch and
// the virtualenv path — to the live binding. Leaving either at the startup
// value tells the model it is on the branch of the checkout it left.
func TestWorktreeSwitchRefreshesInjectedGitStatus(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t)
	writeTestVenv(t, repo)
	a := newWorktreeTestAgentOn(t, repo, "session-owner")
	a.waitGitStatus(ctx)

	_, startupStatus, _, startupVenv := a.promptMetaSnapshot()
	if !strings.Contains(startupStatus, "Git branch: main") {
		t.Fatalf("startup git status = %q, want the startup branch", startupStatus)
	}
	if want := filepath.Join(repo, ".venv"); startupVenv != want {
		t.Fatalf("startup venv = %q, want %q", startupVenv, want)
	}

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-status"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	_, inWorktree, _, worktreeVenv := a.promptMetaSnapshot()
	if !strings.Contains(inWorktree, "Git branch: "+res.Branch) {
		t.Errorf("git status inside the worktree = %q, want branch %q", inWorktree, res.Branch)
	}
	if strings.Contains(inWorktree, "Git branch: main") {
		t.Errorf("git status inside the worktree still names the startup branch: %q", inWorktree)
	}
	// The worktree has no environment of its own, so the content root's venv
	// stays in the hint rather than disappearing.
	if want := filepath.Join(repo, ".venv"); worktreeVenv != want {
		t.Errorf("venv inside the worktree = %q, want the content root's %q", worktreeVenv, want)
	}

	// A checkout that does carry its own environment wins over the fallback.
	ownVenv := writeTestVenv(t, res.Path)
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-status"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	_, back, _, backVenv := a.promptMetaSnapshot()
	if !strings.Contains(back, "Git branch: main") {
		t.Errorf("git status after leaving the worktree = %q, want the startup branch", back)
	}
	if backVenv != startupVenv {
		t.Errorf("venv after leaving the worktree = %q, want %q", backVenv, startupVenv)
	}
	if _, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-status"}); err != nil {
		t.Fatalf("WorktreeEnter (again): %v", err)
	}
	_, reentered, _, reenteredVenv := a.promptMetaSnapshot()
	if !strings.Contains(reentered, "Git branch: "+res.Branch) {
		t.Errorf("git status after re-entering = %q, want branch %q", reentered, res.Branch)
	}
	if reenteredVenv != ownVenv {
		t.Errorf("venv after re-entering = %q, want the worktree's own %q", reenteredVenv, ownVenv)
	}
}

// writeTestVenv lays down a minimal virtualenv (a directory holding pyvenv.cfg)
// and returns its path.
func writeTestVenv(t *testing.T, dir string) string {
	t.Helper()
	venv := filepath.Join(dir, ".venv")
	if err := os.MkdirAll(venv, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", venv, err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr\n"), 0o644); err != nil {
		t.Fatalf("write pyvenv.cfg: %v", err)
	}
	return venv
}

func TestWorktreeEnterSwitchesBaseDirAndKeepsInFlightSnapshot(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")

	before := a.toolExecutionPipeline()
	if got := before.effectiveToolBaseDir(); got != repo {
		t.Fatalf("initial base dir = %q, want %q", got, repo)
	}

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-one"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if res.Existed {
		t.Error("expected a freshly created worktree")
	}
	if res.PreviousPath != repo {
		t.Errorf("PreviousPath = %q, want %q", res.PreviousPath, repo)
	}
	state := a.workDirState.load()
	if state.Generation != 1 {
		t.Errorf("binding generation = %d, want 1", state.Generation)
	}
	if state.Path != res.Path || state.WorktreeID != "feat-one" || state.Branch != worktree.DefaultBranchPrefix+"feat-one" {
		t.Errorf("published state = %#v, want path/branch of feat-one", state)
	}
	if got := a.effectiveToolBaseDir(); got != res.Path {
		t.Errorf("effectiveToolBaseDir = %q, want %q", got, res.Path)
	}
	if got := a.effectivePathScope().Cwd; got != res.Path {
		t.Errorf("path scope cwd = %q, want %q", got, res.Path)
	}

	after := a.toolExecutionPipeline()
	if got := after.effectiveToolBaseDir(); got != res.Path {
		t.Errorf("pipeline base dir after switch = %q, want %q", got, res.Path)
	}

	// The snapshot bound before the switch keeps writing the checkout it bound.
	writeThroughPipeline(t, before, "before.txt")
	assertFileContent(t, filepath.Join(repo, "before.txt"), "before.txt")
	assertNoFile(t, filepath.Join(res.Path, "before.txt"))

	// A pipeline built after the switch writes the worktree.
	writeThroughPipeline(t, after, "after.txt")
	assertFileContent(t, filepath.Join(res.Path, "after.txt"), "after.txt")
	assertNoFile(t, filepath.Join(repo, "after.txt"))

	// The permission scope and the hook working directory a call was dispatched
	// with keep the checkout they bound, too: only the request binding moves
	// forward, the in-flight request does not.
	if got := before.effectivePathScope().Cwd; got != repo {
		t.Errorf("in-flight path scope cwd after switch = %q, want %q", got, repo)
	}
	if got := after.effectivePathScope().Cwd; got != res.Path {
		t.Errorf("post-switch path scope cwd = %q, want %q", got, res.Path)
	}

	// Entering the active worktree again is a no-op: it must not bump the
	// generation or re-run the post-switch refresh.
	again, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-one"})
	if err != nil {
		t.Fatalf("WorktreeEnter (again): %v", err)
	}
	if again.Path != res.Path {
		t.Errorf("re-enter result = %#v, want the same path", again)
	}
	if got := a.workDirState.load().Generation; got != 1 {
		t.Errorf("generation after re-enter = %d, want 1", got)
	}
}

// recordingSyncHookEngine records the envelopes of synchronous hooks so a test
// can assert the working directory a tool call's hook ran in.
type recordingSyncHookEngine struct {
	mu        sync.Mutex
	envelopes []hook.Envelope
}

func (e *recordingSyncHookEngine) Fire(_ context.Context, env hook.Envelope) (*hook.Result, error) {
	e.mu.Lock()
	e.envelopes = append(e.envelopes, env)
	e.mu.Unlock()
	return &hook.Result{Action: hook.ActionContinue}, nil
}

func (*recordingSyncHookEngine) FireBackground(context.Context, hook.Envelope) {}

func (*recordingSyncHookEngine) RunAutomation(context.Context, hook.Envelope) ([]hook.AutomationJobResult, error) {
	return nil, nil
}

func (*recordingSyncHookEngine) HasSyncHooks(string) bool { return false }

func (e *recordingSyncHookEngine) projectRoots(point string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var roots []string
	for _, env := range e.envelopes {
		if env.Point == point {
			roots = append(roots, env.ProjectRoot)
		}
	}
	return roots
}

// TestWorktreeSwitchKeepsInFlightRequestBinding drives a tool call through the
// pipeline and checks the hook envelope's working directory: the pipeline built
// before the switch keeps firing in the checkout it bound, while a call
// dispatched after the switch runs in the worktree.
func TestWorktreeSwitchKeepsInFlightRequestBinding(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	a.tools.Register(tools.ReadTool{BaseDir: repo})
	hooks := &recordingSyncHookEngine{}
	a.hookEngine = hooks

	const rel = "binding.txt"
	if err := os.WriteFile(filepath.Join(repo, rel), []byte("main checkout"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	args, err := json.Marshal(map[string]string{"path": rel})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	before := a.toolExecutionPipeline()
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-binding"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, rel), []byte("worktree checkout"), 0o644); err != nil {
		t.Fatalf("write worktree %s: %v", rel, err)
	}

	if _, err := before.execute(ctx, message.ToolCall{ID: "call-inflight", Name: tools.NameRead, Args: args}, true); err != nil {
		t.Fatalf("in-flight read: %v", err)
	}
	if got := hooks.projectRoots(hook.OnToolCall); len(got) != 1 || got[0] != repo {
		t.Fatalf("in-flight hook dirs = %v, want [%s]", got, repo)
	}

	if _, err := a.executeToolCall(ctx, message.ToolCall{ID: "call-current", Name: tools.NameRead, Args: args}); err != nil {
		t.Fatalf("current read: %v", err)
	}
	got := hooks.projectRoots(hook.OnToolCall)
	if len(got) != 2 || got[1] != res.Path {
		t.Fatalf("hook dirs = %v, want [%s %s]", got, repo, res.Path)
	}
}

// TestWorktreeEnterHonorsConfiguredRoot checks that the Enter tool creates the
// checkout where worktree.root points, not always under the state dir.
func TestWorktreeEnterHonorsConfiguredRoot(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t)
	a := newTestMainAgent(t, repo)
	a.SetWorktreeRuntime(WorktreeRuntime{
		PathLocator:  newWorktreeTestLocator(t),
		RepoRoot:     repo,
		BranchPrefix: worktree.DefaultBranchPrefix,
		Root:         ".chord/worktrees",
		SessionID:    "session-owner",
	})

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-inrepo"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	want := filepath.Join(repo, ".chord", "worktrees", "feat-inrepo")
	if res.Path != want {
		t.Fatalf("worktree path = %s, want %s", res.Path, want)
	}
}

func TestWorktreeExitKeepReturnsToStartupDir(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-keep"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}

	exit, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-keep"})
	if err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	if exit.Removed {
		t.Error("keep should not remove the checkout")
	}
	if exit.WorkDir != repo {
		t.Errorf("WorkDir = %q, want %q", exit.WorkDir, repo)
	}
	if got := a.effectiveToolBaseDir(); got != repo {
		t.Errorf("effectiveToolBaseDir = %q, want %q", got, repo)
	}
	if got := a.workDirState.load().Generation; got != 2 {
		t.Errorf("generation = %d, want 2", got)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("kept worktree checkout missing: %v", err)
	}
	exists, err := worktree.BranchRefExists(ctx, repo, worktree.DefaultBranchPrefix+"feat-keep")
	if err != nil || !exists {
		t.Fatalf("branch kept = %v (err %v), want true", exists, err)
	}
}

func TestWorktreeExitWithoutActiveWorktree(t *testing.T) {
	a, _ := newWorktreeTestAgent(t, "session-owner")
	_, err := a.WorktreeExit(context.Background(), tools.WorktreeExitRequest{})
	if err == nil || !strings.Contains(err.Error(), "no worktree is active") {
		t.Fatalf("err = %v, want a no-active-worktree error", err)
	}
}

func TestWorktreeExitRefusesRemovingActiveWorktree(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-owner")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-active"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}

	_, err = a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-active", Remove: true})
	if err == nil || !strings.Contains(err.Error(), "active working directory") {
		t.Fatalf("err = %v, want an active-working-directory refusal", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("worktree should survive a refused removal: %v", err)
	}
}

func TestWorktreeExitRefusesForeignOwner(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-owner")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-owned"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-owned"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	if err := worktree.WriteOwner(ctx, res.Path, worktree.Owner{
		SessionID: "other-session",
		Kind:      worktree.OwnerKindMain,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("rewrite owner: %v", err)
	}

	_, err = a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-owned", Remove: true})
	if err == nil || !strings.Contains(err.Error(), "was created by session other-session") {
		t.Fatalf("err = %v, want a foreign-owner refusal", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("worktree should survive a foreign-owner removal: %v", err)
	}
}

func TestWorktreeExitNeedsDiscardForDirtyCheckout(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-dirty"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-dirty"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "dirty.txt"), []byte("local change\n"), 0o644); err != nil {
		t.Fatalf("make dirty: %v", err)
	}

	_, err = a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-dirty", Remove: true})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v, want an uncommitted-changes refusal", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("worktree should survive a refused removal: %v", err)
	}

	exit, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-dirty", Remove: true, DiscardChanges: true})
	if err != nil {
		t.Fatalf("WorktreeExit discard: %v", err)
	}
	if !exit.Removed {
		t.Error("discard_changes removal should report Removed")
	}
	assertNoFile(t, res.Path)
	exists, err := worktree.BranchRefExists(ctx, repo, worktree.DefaultBranchPrefix+"feat-dirty")
	if err != nil || !exists {
		t.Fatalf("branch kept = %v (err %v), want true", exists, err)
	}
}

// TestWorktreeRemovalWaitsForRunningWork pins the runtime half of the removal
// guard: a checkout another agent is bound to, or one a background command is
// still running in, cannot be deleted — not even with discard_changes, which
// consents to losing the tree's contents rather than to pulling the directory
// out from under a live process.
func TestWorktreeRemovalWaitsForRunningWork(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-owner")
	a.tools.Register(tools.NewShellTool(""))
	defer tools.StopAllJobsForSessionSwitch()

	agentTree, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-agent-holder"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-agent-holder"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	worker := newControllableTestSubAgent(t, a, "task-holder")
	worker.setState(SubAgentStateRunning, "working")
	worker.workDirState.store(WorkDirState{Path: agentTree.Path, WorktreeID: agentTree.Name, Branch: agentTree.Branch})

	_, err = a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-agent-holder", Remove: true, DiscardChanges: true})
	if err == nil || !strings.Contains(err.Error(), "still in use") || !strings.Contains(err.Error(), worker.instanceID) {
		t.Fatalf("err = %v, want a live-holder refusal naming %s", err, worker.instanceID)
	}
	if _, err := os.Stat(agentTree.Path); err != nil {
		t.Fatalf("worktree should survive a refused removal: %v", err)
	}

	// A worker that has not reached a terminal state still resumes in that
	// checkout, so it keeps holding it until it does.
	worker.setState(SubAgentStateCompleted, "done")
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-agent-holder", Remove: true}); err != nil {
		t.Fatalf("WorktreeExit after the worker finished: %v", err)
	}
	assertNoFile(t, agentTree.Path)

	// A background command keeps holding its checkout after the agent leaves it.
	jobTree, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-job-holder"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	out, err := a.executeToolCall(ctx, message.ToolCall{
		ID:   "call-background",
		Name: tools.NameShell,
		Args: json.RawMessage(`{"command":"sleep 30","description":"hold the checkout","run_in_background":true}`),
	})
	if err != nil {
		t.Fatalf("background shell: %v", err)
	}
	if !strings.Contains(out.Result, "job-") {
		t.Fatalf("background shell output = %q, want a job handle", out.Result)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-job-holder"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	_, err = a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-job-holder", Remove: true, DiscardChanges: true})
	if err == nil || !strings.Contains(err.Error(), "background job") {
		t.Fatalf("err = %v, want a refusal naming the running background job", err)
	}
	if _, err := os.Stat(jobTree.Path); err != nil {
		t.Fatalf("worktree should survive a refused removal: %v", err)
	}
}

func TestWorktreeListMarksActiveCheckout(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-list")
	if _, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-list"}); err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}

	entries, err := a.WorktreeList(ctx)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want exactly one worktree", entries)
	}
	entry := entries[0]
	if entry.Name != "feat-list" || !entry.Current {
		t.Errorf("entry = %#v, want feat-list marked current", entry)
	}
	if !entry.OwnerKnown || entry.OwnerKind != string(worktree.OwnerKindMain) || entry.OwnerSessionID != "session-list" {
		t.Errorf("owner = %#v, want the creating session recorded", entry)
	}
	if !entry.DirtyKnown || entry.Dirty {
		t.Errorf("dirty = %v (known %v), want a clean checkout", entry.Dirty, entry.DirtyKnown)
	}

	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-list"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	entries, err = a.WorktreeList(ctx)
	if err != nil {
		t.Fatalf("WorktreeList after exit: %v", err)
	}
	if len(entries) != 1 || entries[0].Current {
		t.Errorf("entries after exit = %#v, want feat-list no longer current", entries)
	}
}

func TestSubAgentWorktreeBindingIsIndependent(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	sub := newControllableTestSubAgent(t, a, "task-1")

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-sub"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	if got := sub.effectiveToolBaseDir(); got != res.Path {
		t.Errorf("sub base dir = %q, want %q", got, res.Path)
	}
	if got := a.effectiveToolBaseDir(); got != repo {
		t.Errorf("main base dir = %q, want it untouched at %q", got, repo)
	}
	if a.workDirState.load().Path != "" {
		t.Errorf("main binding = %#v, want it untouched by the sub's switch", a.workDirState.load())
	}
	if state := sub.workDirState.load(); state.WorktreeID != "feat-sub" {
		t.Errorf("sub binding = %#v, want feat-sub", state)
	}

	exit, err := sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-sub"})
	if err != nil {
		t.Fatalf("sub WorktreeExit: %v", err)
	}
	if exit.WorkDir != repo {
		t.Errorf("sub WorkDir = %q, want %q", exit.WorkDir, repo)
	}
	if got := sub.effectiveToolBaseDir(); got != repo {
		t.Errorf("sub base dir after exit = %q, want %q", got, repo)
	}
}

// TestSubAgentWorktreeSwitchReloadsAgentsMD pins the worker's AGENTS.md to the
// checkout it works in. A gitignored AGENTS.md that exists only in the main
// checkout must still reach a worker inside a worktree, while a checkout that
// carries its own file must override it. Keeping the spawn-time snapshot would
// leave the worker following the instructions of the checkout it left.
func TestSubAgentWorktreeSwitchReloadsAgentsMD(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("root instructions\n"), 0o644); err != nil {
		t.Fatalf("write main checkout AGENTS.md: %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "task-1")

	entered, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-agents"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	// The checkout has no AGENTS.md of its own, so the switch must fall back to
	// the main checkout's copy (where gitignored local instructions live).
	if got := sub.agentsMDSnapshot(); !strings.Contains(got, "root instructions") {
		t.Fatalf("AGENTS.md in checkout = %q, want the main checkout's instructions", got)
	}
	if prompt := sub.buildSystemPrompt(); !strings.Contains(prompt, "## Workspace Instructions") {
		t.Fatalf("system prompt did not pick up the reloaded AGENTS.md, got:\n%s", prompt)
	}

	if err := os.WriteFile(filepath.Join(entered.Path, "AGENTS.md"), []byte("checkout instructions\n"), 0o644); err != nil {
		t.Fatalf("write checkout AGENTS.md: %v", err)
	}
	// Leaving re-reads the main checkout's copy: the worktree's own file stays
	// behind with the checkout.
	if _, err := sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-agents"}); err != nil {
		t.Fatalf("sub WorktreeExit: %v", err)
	}
	if got := sub.agentsMDSnapshot(); !strings.Contains(got, "root instructions") || strings.Contains(got, "checkout instructions") {
		t.Fatalf("AGENTS.md after exit = %q, want the main checkout's instructions", got)
	}

	// Re-entering must pick up the file the checkout now carries, and the
	// reminder must follow: it is what the model actually reads.
	if _, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-agents"}); err != nil {
		t.Fatalf("sub WorktreeEnter again: %v", err)
	}
	if got := sub.agentsMDSnapshot(); !strings.Contains(got, "checkout instructions") || strings.Contains(got, "root instructions") {
		t.Fatalf("AGENTS.md in checkout = %q, want the checkout's own instructions", got)
	}
	reminder := sub.cachedSessionReminderContent.Load()
	if reminder == nil || !strings.Contains(*reminder, "checkout instructions") {
		t.Fatalf("session reminder = %v, want it to carry the checkout's AGENTS.md", reminder)
	}
}

// TestSubAgentCannotRemoveMainAgentWorktree pins the ownership rule to the
// requester's kind, not just the session: within one session a worker may only
// reclaim what a worker created, while the session's MainAgent may reclaim
// anything the session owns.
func TestSubAgentCannotRemoveMainAgentWorktree(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-owner")
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-owned-by-main"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-owned-by-main"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}

	sub := newControllableTestSubAgent(t, a, "task-1")
	_, err = sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-owned-by-main", Remove: true})
	if err == nil {
		t.Fatal("a subagent must not remove a worktree its session's main agent created")
	}
	if !strings.Contains(err.Error(), "does not own it") {
		t.Errorf("err = %v, want an ownership refusal", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("worktree should survive the refused removal: %v", err)
	}

	exit, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-owned-by-main", Remove: true})
	if err != nil {
		t.Fatalf("main WorktreeExit remove: %v", err)
	}
	if !exit.Removed {
		t.Error("the main agent should be able to reclaim a worktree its session owns")
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still present after removal: %v", err)
	}
}

// TestWorktreeSessionAnchorsMachineStateToContentRoot pins the "machine state
// lives in the main worktree" invariant to the tool layer. A worktree checkout
// never contains chord's own state directories, so a relative plan path that
// resolved against the checkout would be written into a directory that
// `worktree remove` deletes.
func TestWorktreeSessionAnchorsMachineStateToContentRoot(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	a.tools.Register(tools.ReadTool{BaseDir: repo})
	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-state"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}

	p := a.toolExecutionPipeline()
	if got := p.effectiveToolBaseDir(); got != res.Path {
		t.Fatalf("pipeline base dir = %q, want the worktree %q", got, res.Path)
	}
	write := func(t *testing.T, name, content string) {
		t.Helper()
		args, err := json.Marshal(map[string]string{"path": name, "content": content})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		if _, err := a.executeToolCall(ctx, message.ToolCall{ID: "call-" + name, Name: tools.NameWrite, Args: args}); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	const plan = ".chord/plans/20260922-machine-state.md"
	write(t, plan, plan)
	assertFileContent(t, filepath.Join(repo, filepath.FromSlash(plan)), plan)
	assertNoFile(t, filepath.Join(res.Path, filepath.FromSlash(plan)))

	// Checkout content still follows the checkout.
	write(t, "src/main.go", "src/main.go")
	assertFileContent(t, filepath.Join(res.Path, "src", "main.go"), "src/main.go")
	assertNoFile(t, filepath.Join(repo, "src", "main.go"))

	// Reads map to the same file, so the session reads back the plan it wrote.
	readArgs, err := json.Marshal(map[string]string{"path": plan})
	if err != nil {
		t.Fatalf("marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(ctx, message.ToolCall{ID: "call-read", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("read the plan from the worktree session: %v", err)
	}

	// The whole call is anchored, not just the tool body: the pipeline's base
	// dir is what the permission scope, the tracked file lock and the pre-write
	// capture read, so they must agree with the path the tool writes.
	planArgs, err := json.Marshal(map[string]string{"path": plan, "content": "plan"})
	if err != nil {
		t.Fatalf("marshal plan args: %v", err)
	}
	if got := p.withMachineStateBaseDir(message.ToolCall{Name: tools.NameWrite, Args: planArgs}).effectiveToolBaseDir(); got != repo {
		t.Errorf("machine-state call base dir = %q, want the content root %q", got, repo)
	}
	srcArgs, err := json.Marshal(map[string]string{"path": "src/main.go", "content": "x"})
	if err != nil {
		t.Fatalf("marshal source args: %v", err)
	}
	if got := p.withMachineStateBaseDir(message.ToolCall{Name: tools.NameWrite, Args: srcArgs}).effectiveToolBaseDir(); got != res.Path {
		t.Errorf("checkout-content call base dir = %q, want the worktree %q", got, res.Path)
	}

	// A mixed call is never redirected: applying half a patch to another
	// checkout would be worse than leaving the call on the session's checkout.
	mixed := json.RawMessage(`{"paths":[".chord/plans/x.md","src/main.go"],"reason":"cleanup"}`)
	if got := p.withMachineStateBaseDir(message.ToolCall{Name: tools.NameDelete, Args: mixed}).effectiveToolBaseDir(); got != res.Path {
		t.Errorf("mixed call base dir = %q, want the worktree %q", got, res.Path)
	}

	// Leaving the worktree restores the ordinary base dir.
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-state"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	if got := a.toolExecutionPipeline().withMachineStateBaseDir(message.ToolCall{Name: tools.NameWrite, Args: planArgs}).effectiveToolBaseDir(); got != repo {
		t.Errorf("base dir after leaving = %q, want %q", got, repo)
	}
}

// TestWorktreeReadsFollowTheActiveCheckout pins acceptance ③ ("跨副本 read
// 对齐"), which the plan states without defining. The observable form is: one
// relative path resolves to the checkout the session is bound to, so a read
// returns that copy's content and never the other copy's. Both copies hold the
// file with different content, so the returned text names the copy the read
// resolved against — the registry entry was registered with the startup
// directory, so a read that ignored the binding would return the main copy.
func TestWorktreeReadsFollowTheActiveCheckout(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	a.tools.Register(tools.ReadTool{BaseDir: repo})

	const rel = "src/shared.go"
	writeCopy := func(t *testing.T, root, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	read := func(t *testing.T) string {
		t.Helper()
		args, err := json.Marshal(map[string]string{"path": rel})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		out, err := a.executeToolCall(ctx, message.ToolCall{ID: "call-read", Name: tools.NameRead, Args: args})
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return out.Result
	}

	writeCopy(t, repo, "package main // copy: main checkout")
	if got := read(t); !strings.Contains(got, "copy: main checkout") {
		t.Fatalf("read from the startup checkout = %q, want the main copy", got)
	}

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-read"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	writeCopy(t, res.Path, "package main // copy: worktree checkout")
	got := read(t)
	if !strings.Contains(got, "copy: worktree checkout") {
		t.Errorf("read inside the worktree = %q, want the worktree copy", got)
	}
	if strings.Contains(got, "copy: main checkout") {
		t.Errorf("read inside the worktree returned the main checkout's copy: %q", got)
	}

	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-read"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	if got := read(t); !strings.Contains(got, "copy: main checkout") {
		t.Errorf("read after leaving the worktree = %q, want the main copy again", got)
	}
}

// TestWorktreeSwitchLeavesMainCheckoutUntouched pins the switch half of the
// "main checkout is not touched" acceptance: creation was covered by
// TestCreateConfiguredRootKeepsMainCheckoutClean, the switch itself was not.
// The worktree is placed inside the repository (D2's opt-in root), so anything
// the switch wrote into the main checkout would show up in its status.
func TestWorktreeSwitchLeavesMainCheckoutUntouched(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t)
	// Mirror the real repository, which ignores .chord/: the test harness keeps
	// its session directory there, and it must not count as main-checkout dirt.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".chord/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runAgentTestGit(t, repo, "add", ".gitignore")
	runAgentTestGit(t, repo, "commit", "-q", "-m", "ignore chord state")

	a := newTestMainAgent(t, repo)
	a.SetWorktreeRuntime(WorktreeRuntime{
		PathLocator:  newWorktreeTestLocator(t),
		RepoRoot:     repo,
		BranchPrefix: worktree.DefaultBranchPrefix,
		Root:         ".chord/worktrees",
		SessionID:    "session-main-checkout",
	})

	branchBefore := agentTestGitOutput(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	headBefore := agentTestGitOutput(t, repo, "rev-parse", "HEAD")

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-inside"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if !strings.HasPrefix(res.Path, repo+string(filepath.Separator)) {
		t.Fatalf("worktree path = %q, want it inside the repository %q", res.Path, repo)
	}

	if got := agentTestGitOutput(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != branchBefore {
		t.Errorf("main checkout branch after the switch = %q, want %q", got, branchBefore)
	}
	if got := agentTestGitOutput(t, repo, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("main checkout HEAD after the switch = %q, want %q", got, headBefore)
	}
	if status := agentTestGitOutput(t, repo, "status", "--porcelain", "-uall"); status != "" {
		t.Errorf("main checkout is not clean after the switch:\n%s", status)
	}

	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-inside"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	if status := agentTestGitOutput(t, repo, "status", "--porcelain", "-uall"); status != "" {
		t.Errorf("main checkout is not clean after leaving the worktree:\n%s", status)
	}
}

// readToolActivityJournal returns the persisted started records keyed by call
// id. It reads the file the manager wrote rather than an in-memory view, so the
// assertions cover what a crash replay would actually find on disk.
func readToolActivityJournal(t *testing.T, path string) map[string]recovery.ToolActivityRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tool-activity journal %s: %v", path, err)
	}
	records := make(map[string]recovery.ToolActivityRecord)
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec recovery.ToolActivityRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode journal line %q: %v", line, err)
		}
		records[rec.CallID] = rec
	}
	return records
}

// TestToolActivityJournalCarriesTheActiveCheckout pins the recovery half of the
// worktree contract: a crash replay resolves a call's relative paths against the
// checkout the call ran in, so the started record and the snapshot must carry
// the effective base directory and its generation. Recording only the call id
// would make replay resolve a worktree session's paths against the main
// checkout (and vice versa), which is exactly the "replay into the new cwd"
// failure the binding generation exists to detect.
func TestToolActivityJournalCarriesTheActiveCheckout(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	if a.recoveryManager() == nil {
		t.Fatal("test agent has no recovery manager to journal through")
	}
	journal := filepath.Join(a.sessionDir, recovery.ToolActivityFilename)

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-journal"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	bindingGeneration := a.workDirState.load().Generation
	if bindingGeneration == 0 {
		t.Fatalf("switch published binding = %#v, want a non-zero generation", a.workDirState.load())
	}
	if got := a.toolExecutionPipeline().toolBaseDirGeneration; got != bindingGeneration {
		t.Fatalf("pipeline generation = %d, want the live binding's %d", got, bindingGeneration)
	}

	write := func(t *testing.T, callID, name string) {
		t.Helper()
		args, err := json.Marshal(map[string]string{"path": name, "content": name})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		if _, err := a.executeToolCall(ctx, message.ToolCall{ID: callID, Name: tools.NameWrite, Args: args}); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(t, "call-checkout", "src/main.go")
	write(t, "call-machine-state", ".chord/plans/20260922-journal.md")

	records := readToolActivityJournal(t, journal)
	checkout := records["call-checkout"]
	if checkout.WorkDir != res.Path {
		t.Errorf("checkout call workdir = %q, want the session's checkout %q", checkout.WorkDir, res.Path)
	}
	if checkout.WorkDirGeneration != bindingGeneration {
		t.Errorf("checkout call generation = %d, want %d", checkout.WorkDirGeneration, bindingGeneration)
	}
	// Machine state is rebound to the content root, and the journal has to
	// record the directory the call really used, not the session's checkout.
	machineState := records["call-machine-state"]
	if machineState.WorkDir != repo {
		t.Errorf("machine-state call workdir = %q, want the content root %q", machineState.WorkDir, repo)
	}

	// The snapshot carries the same fact per active agent, so a restore can
	// re-establish each worker's own checkout instead of the session's startup
	// directory.
	sub := newControllableTestSubAgent(t, a, "task-journal")
	subRes, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-sub-journal"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	snapshotState := a.buildRecoverySnapshot()
	var snapshot *recovery.AgentSnapshot
	for i := range snapshotState.ActiveAgents {
		if snapshotState.ActiveAgents[i].InstanceID == sub.instanceID {
			snapshot = &snapshotState.ActiveAgents[i]
			break
		}
	}
	if snapshot == nil {
		t.Fatalf("recovery snapshot has no entry for %s", sub.instanceID)
	}
	if snapshot.WorkDir != subRes.Path {
		t.Errorf("snapshot workdir = %q, want the worker's checkout %q", snapshot.WorkDir, subRes.Path)
	}
	if snapshot.WorkDirGeneration != sub.workDirState.load().Generation {
		t.Errorf("snapshot generation = %d, want the live worker binding's %d", snapshot.WorkDirGeneration, sub.workDirState.load().Generation)
	}

	// Leaving the checkout moves the recorded directory back with it, so a
	// crash after the switch back does not replay into the retired checkout.
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-journal"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	write(t, "call-after-exit", "src/after.go")
	after := readToolActivityJournal(t, journal)["call-after-exit"]
	if after.WorkDir != repo {
		t.Errorf("record after leaving the worktree = %#v, want workdir %q", after, repo)
	}
	if want := a.workDirState.load().Generation; after.WorkDirGeneration != want {
		t.Errorf("record generation after leaving = %d, want the live binding's %d", after.WorkDirGeneration, want)
	}
}

func TestWorktreeToolsUnavailableWithoutRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	_, err := a.WorktreeEnter(context.Background(), tools.WorktreeEnterRequest{Name: "nowhere"})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want an unavailable error without SetWorktreeRuntime", err)
	}
}

func TestWorktreeSwitchRebindsLSPAndSurfacesFailure(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t)
	a := newTestMainAgent(t, repo)
	var calls []string
	failNext := false
	a.SetWorktreeRuntime(WorktreeRuntime{
		PathLocator:  newWorktreeTestLocator(t),
		RepoRoot:     repo,
		BranchPrefix: worktree.DefaultBranchPrefix,
		SessionID:    "session-lsp",
		RebindLSP: func(dir string) error {
			calls = append(calls, dir)
			if failNext {
				return errors.New("gopls unavailable")
			}
			return nil
		},
	})

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-lsp"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if len(calls) != 1 || calls[0] != res.Path {
		t.Fatalf("rebind calls = %v, want [%s]", calls, res.Path)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %v, want none when the rebind succeeds", res.Warnings)
	}

	// A failed rebind is a warning, never a rollback: the switch stays published.
	failNext = true
	exit, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-lsp"})
	if err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	if last := calls[len(calls)-1]; last != repo {
		t.Errorf("last rebind call = %q, want %q", last, repo)
	}
	if len(exit.Warnings) == 0 || !strings.Contains(strings.Join(exit.Warnings, "\n"), "language servers could not be reconfigured") {
		t.Errorf("warnings = %v, want a rebind warning", exit.Warnings)
	}
	if got := a.effectiveToolBaseDir(); got != repo {
		t.Errorf("effectiveToolBaseDir = %q, want the switch kept despite the rebind failure", got)
	}
}

// TestRehydrateTaskRestoresRecordedWorktree verifies a rehydrated worker
// resumes inside the checkout it was working in with its identity intact: the
// per-instance metadata records the worktree path, and the resumed binding
// must carry the name, branch, and base that the environment reminder and
// completion report read.
func TestRehydrateTaskRestoresRecordedWorktree(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-restore-worktree")
	configureNestedDelegationTestRuntime(a, 1)

	sub := newControllableTestSubAgent(t, a, "adhoc-restore-worktree")
	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-restore"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	// The switch persists the instance metadata a rehydrated worker resumes from.
	if _, err := loadSubAgentMeta(a.sessionDir, sub.instanceID); err != nil {
		t.Fatalf("loadSubAgentMeta after switch: %v", err)
	}

	record := &DurableTaskRecord{
		TaskID:             "adhoc-restore-worktree",
		AgentDefName:       "worker",
		TaskDesc:           "resume in the recorded worktree",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyNotify,
		LatestInstanceID:   sub.instanceID,
		InstanceHistory:    []string{sub.instanceID},
		RuntimeParked:      true,
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("root instructions\n"), 0o644); err != nil {
		t.Fatalf("write main checkout AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "AGENTS.md"), []byte("checkout instructions\n"), 0o644); err != nil {
		t.Fatalf("write checkout AGENTS.md: %v", err)
	}

	// A fresh agent on the same repository and session directory replays the
	// task the way a resumed process does.
	restoredAgent := newWorktreeTestAgentOn(t, repo, "session-restore-worktree-2")
	if restoredAgent.sessionDir != a.sessionDir {
		t.Fatalf("restored agent session dir = %q, want %q", restoredAgent.sessionDir, a.sessionDir)
	}
	configureNestedDelegationTestRuntime(restoredAgent, 1)
	restoredAgent.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	restoredAgent.ReloadAgentsMD()

	restored, _, err := restoredAgent.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if restored == nil {
		t.Fatal("expected the rehydrated worker")
	}
	state := restored.workDirState.load()
	if state.Path != res.Path || state.WorktreeID != "feat-restore" || state.Branch != res.Branch {
		t.Fatalf("restored binding = %#v, want feat-restore at %s", state, res.Path)
	}
	if strings.TrimSpace(state.BaseSHA) == "" {
		t.Fatalf("restored binding has no base commit: %#v", state)
	}
	// The worker resumes in the recorded checkout, so it must read the
	// instructions of that checkout rather than the parent's snapshot.
	reminder := subAgentReminderContent(t, restored)
	assertEnvBlockStatesWorktree(t, reminder, res.Path, "feat-restore", res.Branch)
	if !strings.Contains(reminder, "checkout instructions") || strings.Contains(reminder, "root instructions") {
		t.Fatalf("restored reminder = %q, want the checkout's instructions", reminder)
	}
}

// A recorded checkout that is no longer a worktree of this repository must not
// be resumed as one: the binding falls back to the plain working directory.
func TestRehydrateTaskIgnoresStaleRecordedWorktree(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-restore-stale")
	configureNestedDelegationTestRuntime(a, 1)

	sub := newControllableTestSubAgent(t, a, "adhoc-restore-stale")
	if _, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-stale"}); err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	if err := worktree.Remove(ctx, repo, "feat-stale", worktree.RemoveOptions{Force: true}, a.worktreeRT.PathLocator); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	record := &DurableTaskRecord{
		TaskID:             "adhoc-restore-stale",
		AgentDefName:       "worker",
		TaskDesc:           "resume after the worktree was removed",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyNotify,
		LatestInstanceID:   sub.instanceID,
		InstanceHistory:    []string{sub.instanceID},
		RuntimeParked:      true,
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	restoredAgent := newWorktreeTestAgentOn(t, repo, "session-restore-stale-2")
	configureNestedDelegationTestRuntime(restoredAgent, 1)
	restoredAgent.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	restored, _, err := restoredAgent.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if state := restored.workDirState.load(); state.WorktreeID != "" || state.Path != "" {
		t.Fatalf("restored binding = %#v, want none for a removed worktree", state)
	}
	if content := subAgentReminderContent(t, restored); strings.Contains(content, "Worktree:") {
		t.Fatalf("reminder should not claim a removed worktree:\n%s", content)
	}
}

// /new continues in the checkout the session was working in, so the new
// session's metadata must record it: without that a later resume of the new
// session lands in the main checkout and replays the transcript's relative
// paths against a different tree.
func TestNewSessionRecordsActiveWorktreeCheckout(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-new-checkout")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	oldSessionDir := a.sessionDir

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-new"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}

	a.handleNewSessionCommand()

	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("new session recorded no metadata")
	}
	if meta.WorktreePath != res.Path || meta.WorktreeName != res.Name || meta.WorktreeBranch != res.Branch {
		t.Fatalf("new session meta = %+v, want the active checkout %s", meta, res.Path)
	}
	last := meta.WorktreeTimeline[len(meta.WorktreeTimeline)-1]
	if last.Reason != recovery.WorktreeSwitchStartup || last.Path != res.Path {
		t.Fatalf("startup boundary = %+v, want reason=%s path=%s", last, recovery.WorktreeSwitchStartup, res.Path)
	}
}

// An in-process /resume replaces the session without touching the working
// directory; the resumed transcript must be interpreted against the checkout
// the session recorded, not the one the previous session left.
func TestResumeAdoptsRecordedWorktreeCheckout(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-resume-adopt")

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-resume"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		t.Fatalf("projectSessionsDir: %v", err)
	}
	targetDir := filepath.Join(sessionsDir, "target-session")
	persistRestorableSession(t, targetDir)
	if err := recovery.SaveSessionMeta(targetDir, recovery.SessionMeta{
		RepoRoot:       repo,
		WorktreeName:   res.Name,
		WorktreeBranch: res.Branch,
		WorktreePath:   res.Path,
	}); err != nil {
		t.Fatalf("SaveSessionMeta(target): %v", err)
	}
	// Leave the checkout without removing it: the process falls back to its
	// startup directory, which is what the current session records.
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: res.Name}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	if got := a.workDirState.load().Path; got != "" {
		t.Fatalf("binding after exit = %q, want cleared", got)
	}

	a.handleResumeCommand("target-session")

	if a.sessionDir != targetDir {
		t.Fatalf("sessionDir = %q, want %q", a.sessionDir, targetDir)
	}
	state := a.workDirState.load()
	if state.Path != res.Path || state.WorktreeID != res.Name || state.Branch != res.Branch {
		t.Fatalf("resumed binding = %#v, want the recorded checkout %s", state, res.Path)
	}
	meta, err := recovery.LoadSessionMeta(targetDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta(target): %v", err)
	}
	if meta == nil || meta.WorktreePath != res.Path {
		t.Fatalf("resumed session meta = %+v, want the adopted checkout", meta)
	}
}

// A worker delegated in the main checkout must come back to it even after the
// parent moved into a worktree. The recorded directory is not a chord-managed
// worktree, but it still belongs to this repository, so falling back to the
// parent's current checkout would reinterpret the worker's transcript against
// a different tree.
func TestRehydrateTaskKeepsRecordedMainCheckoutAfterParentSwitches(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-restore-maindir")
	configureNestedDelegationTestRuntime(a, 1)

	sub := newControllableTestSubAgent(t, a, "adhoc-restore-maindir")
	if err := a.persistSubAgentMeta(sub); err != nil {
		t.Fatalf("persistSubAgentMeta: %v", err)
	}
	meta, err := loadSubAgentMeta(a.sessionDir, sub.instanceID)
	if err != nil || meta == nil {
		t.Fatalf("loadSubAgentMeta before switch = %+v, %v", meta, err)
	}
	if meta.WorkDir != repo {
		t.Fatalf("recorded workdir = %q, want the main checkout %q", meta.WorkDir, repo)
	}

	if _, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-parent"}); err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if a.effectiveToolBaseDir() == repo {
		t.Fatal("parent should be working in the worktree")
	}

	record := &DurableTaskRecord{
		TaskID:             "adhoc-restore-maindir",
		AgentDefName:       "worker",
		TaskDesc:           "resume in the recorded main checkout",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyNotify,
		LatestInstanceID:   sub.instanceID,
		InstanceHistory:    []string{sub.instanceID},
		RuntimeParked:      true,
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	restoredAgent := newWorktreeTestAgentOn(t, repo, "session-restore-maindir-2")
	if restoredAgent.sessionDir != a.sessionDir {
		t.Fatalf("restored agent session dir = %q, want %q", restoredAgent.sessionDir, a.sessionDir)
	}
	configureNestedDelegationTestRuntime(restoredAgent, 1)
	restoredAgent.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	// Model the resumed process: the main agent is working in the worktree the
	// parent entered, so the worker's record is the only source of its tree.
	restoredAgent.workDirState.store(WorkDirState{Path: a.effectiveToolBaseDir(), WorktreeID: "feat-parent"})

	restored, _, err := restoredAgent.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if got := restored.effectiveToolBaseDir(); got != repo {
		t.Fatalf("restored worker dir = %q, want the recorded main checkout %q", got, repo)
	}
	if state := restored.workDirState.load(); state.WorktreeID != "" || state.Path != "" {
		t.Fatalf("restored binding = %#v, want the plain main checkout without worktree identity", state)
	}
}

// A /new session that cannot record the checkout it continues in must not
// leave a claim on a path a removal already deleted: the claim is refused with
// the removal's error and surfaced as a warning, and the fresh session record
// keeps no binding rather than a dead one.
func TestNewSessionDoesNotClaimACheckoutRemovedUnderIt(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-new-gone-checkout")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	oldSessionDir := a.sessionDir

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-new-gone"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	if err := os.RemoveAll(res.Path); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}

	a.handleNewSessionCommand()
	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta != nil && meta.WorktreePath != "" {
		t.Fatalf("new session claimed the removed checkout %q", meta.WorktreePath)
	}
}

// A worker whose checkout was removed while its record is written must not
// republish the claim either — a removal scans this field for live holders —
// but the rest of its record still has to land, or the worker loses its state.
func TestSubAgentMetaDropsAClaimOnARemovedCheckout(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-sub-gone-checkout")
	sub := newControllableTestSubAgent(t, a, "adhoc-gone-checkout")

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-sub-gone"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	if err := os.RemoveAll(res.Path); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}

	if err := a.persistSubAgentMeta(sub); err != nil {
		t.Fatalf("persistSubAgentMeta: %v", err)
	}
	meta, err := loadSubAgentMeta(a.sessionDir, sub.instanceID)
	if err != nil || meta == nil {
		t.Fatalf("loadSubAgentMeta = %+v, %v", meta, err)
	}
	if meta.WorkDir != "" || meta.WorkDirGeneration != 0 {
		t.Fatalf("worker claim = %q generation %d, want it dropped for the removed checkout", meta.WorkDir, meta.WorkDirGeneration)
	}
	if meta.InstanceID != sub.instanceID || meta.TaskID != sub.taskID {
		t.Fatalf("record = %+v, want the worker's own state preserved", meta)
	}
}
