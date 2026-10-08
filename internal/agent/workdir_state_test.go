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
	_ = sessionID
	a := newTestMainAgent(t, repo)
	a.SetWorktreeRuntime(WorktreeRuntime{
		PathLocator:  newWorktreeTestLocator(t),
		RepoRoot:     repo,
		BranchPrefix: worktree.DefaultBranchPrefix,
	})
	a.tools.Register(tools.WriteTool{BaseDir: repo})
	return a
}

// createTestWorktree creates a chord-managed worktree through the same
// worktree package the CLI uses, so the tests exercise production creation
// rather than a hand-built directory tree.
func createTestWorktree(t *testing.T, a *MainAgent, name string) *worktree.Info {
	t.Helper()
	if a.worktreeRT.PathLocator == nil {
		t.Fatal("test agent has no worktree runtime installed")
	}
	info, err := worktree.Create(context.Background(), worktree.CreateOptions{
		Name:         name,
		RepoRoot:     a.worktreeRT.RepoRoot,
		PathLocator:  a.worktreeRT.PathLocator,
		BranchPrefix: a.worktreeRT.BranchPrefix,
	})
	if err != nil {
		t.Fatalf("create worktree %s: %v", name, err)
	}
	return info
}

// installTestCheckout puts the agent into a fresh chord-managed worktree
// through the same binding path a session start uses, and returns the installed
// binding.
func installTestCheckout(t *testing.T, a *MainAgent, name string) WorkDirState {
	t.Helper()
	info := createTestWorktree(t, a, name)
	state := WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		Generation: 1,
	}
	if notice := a.RestoreWorkDirBinding(context.Background(), state, recovery.WorktreeSwitchResume); notice != "" {
		t.Fatalf("install %s: %s", name, notice)
	}
	got := a.workDirState.load()
	if got.Path != info.Path || got.Generation != 1 {
		t.Fatalf("installed binding = %#v, want %s at generation 1", got, info.Path)
	}
	if strings.TrimSpace(got.BaseSHA) == "" {
		t.Fatalf("installed binding has no base commit: %#v", got)
	}
	return got
}

// adoptTestCheckout switches the process into a (new or resumed) chord-managed
// worktree through the same path an in-process /resume takes, and returns the
// resulting binding.
func adoptTestCheckout(t *testing.T, a *MainAgent, name string) WorkDirState {
	t.Helper()
	info := createTestWorktree(t, a, name)
	prev := a.workDirState.load()
	notice := a.adoptResumedSessionCheckout(context.Background(), WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
	})
	if notice != "" {
		t.Fatalf("adopt %s: %s", name, notice)
	}
	got := a.workDirState.load()
	if got.Path != info.Path || got.WorktreeID != info.Name {
		t.Fatalf("adopted binding = %#v, want the %s checkout at %s", got, name, info.Path)
	}
	if got.Generation != prev.Generation+1 {
		t.Fatalf("adopted generation = %d, want %d", got.Generation, prev.Generation+1)
	}
	return got
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

// TestWorkDirSwitchRefreshesInjectedGitStatus pins the per-checkout facts the
// runtime injects into every request — the git status that names the branch and
// the virtualenv path — to the live binding. Leaving either at the startup
// value tells the model it is on the branch of the checkout it left.
func TestWorkDirSwitchRefreshesInjectedGitStatus(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t)
	writeTestVenv(t, repo)
	a := newWorktreeTestAgentOn(t, repo, "session-owner")
	a.waitGitStatus(ctx)

	_, startupStatus, _, startupVenv := a.promptMetaSnapshot()
	if !strings.Contains(startupStatus, "(branch main,") {
		t.Fatalf("startup git status = %q, want the startup branch", startupStatus)
	}
	if want := filepath.Join(repo, ".venv"); startupVenv != want {
		t.Fatalf("startup venv = %q, want %q", startupVenv, want)
	}

	installed := installTestCheckout(t, a, "feat-status")
	_, inWorktree, _, worktreeVenv := a.promptMetaSnapshot()
	if !strings.Contains(inWorktree, "(branch "+installed.Branch+",") {
		t.Errorf("git status inside the checkout = %q, want branch %q", inWorktree, installed.Branch)
	}
	if strings.Contains(inWorktree, "(branch main,") {
		t.Errorf("git status inside the checkout still names the startup branch: %q", inWorktree)
	}
	// The checkout has no environment of its own, so the content root's venv
	// stays in the hint rather than disappearing.
	if want := filepath.Join(repo, ".venv"); worktreeVenv != want {
		t.Errorf("venv inside the checkout = %q, want the content root's %q", worktreeVenv, want)
	}

	// A checkout that does carry its own environment wins over the fallback.
	ownVenv := writeTestVenv(t, installed.Path)
	adoptTestCheckout(t, a, "feat-status-away")
	back := adoptTestCheckout(t, a, "feat-status")
	if back.Path != installed.Path {
		t.Fatalf("re-adopted path = %q, want %q", back.Path, installed.Path)
	}
	if _, _, _, backVenv := a.promptMetaSnapshot(); backVenv != ownVenv {
		t.Errorf("venv after re-entering the checkout = %q, want its own %q", backVenv, ownVenv)
	}
}

// TestWorkDirSwitchKeepsInFlightSnapshot pins the execution half of the
// binding contract: a pipeline snapshot taken before the switch keeps writing
// the checkout it bound — the permission scope and hook working directory
// included — while a pipeline built afterwards writes the new one.
func TestWorkDirSwitchKeepsInFlightSnapshot(t *testing.T) {
	a, repo := newWorktreeTestAgent(t, "session-owner")

	before := a.toolExecutionPipeline()
	if got := before.effectiveToolBaseDir(); got != repo {
		t.Fatalf("initial base dir = %q, want %q", got, repo)
	}

	installed := installTestCheckout(t, a, "feat-one")
	if got := a.effectiveToolBaseDir(); got != installed.Path {
		t.Errorf("effectiveToolBaseDir = %q, want %q", got, installed.Path)
	}
	if got := a.effectivePathScope().Cwd; got != installed.Path {
		t.Errorf("path scope cwd = %q, want %q", got, installed.Path)
	}

	after := a.toolExecutionPipeline()
	if got := after.effectiveToolBaseDir(); got != installed.Path {
		t.Errorf("pipeline base dir after switch = %q, want %q", got, installed.Path)
	}

	writeThroughPipeline(t, before, "before.txt")
	assertFileContent(t, filepath.Join(repo, "before.txt"), "before.txt")
	assertNoFile(t, filepath.Join(installed.Path, "before.txt"))

	writeThroughPipeline(t, after, "after.txt")
	assertFileContent(t, filepath.Join(installed.Path, "after.txt"), "after.txt")
	assertNoFile(t, filepath.Join(repo, "after.txt"))

	if got := before.effectivePathScope().Cwd; got != repo {
		t.Errorf("in-flight path scope cwd after switch = %q, want %q", got, repo)
	}
	if got := after.effectivePathScope().Cwd; got != installed.Path {
		t.Errorf("post-switch path scope cwd = %q, want %q", got, installed.Path)
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

// TestWorkDirSwitchKeepsInFlightRequestBinding drives a tool call through the
// pipeline and checks the hook envelope's working directory: the pipeline built
// before the switch keeps firing in the checkout it bound, while a call
// dispatched after the switch runs in the new checkout.
func TestWorkDirSwitchKeepsInFlightRequestBinding(t *testing.T) {
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
	installed := installTestCheckout(t, a, "feat-binding")
	if err := os.WriteFile(filepath.Join(installed.Path, rel), []byte("worktree checkout"), 0o644); err != nil {
		t.Fatalf("write checkout %s: %v", rel, err)
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
	if len(got) != 2 || got[1] != installed.Path {
		t.Fatalf("hook dirs = %v, want [%s %s]", got, repo, installed.Path)
	}
}

// TestWorkDirBindingAnchorsMachineStateToContentRoot pins the "machine state
// lives in the main worktree" invariant. A worktree checkout never contains
// chord's own state directories, so a relative plan path that resolved against
// the checkout would be written into a directory `worktree remove` deletes.
func TestWorkDirBindingAnchorsMachineStateToContentRoot(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-owner")
	a.tools.Register(tools.ReadTool{BaseDir: repo})

	planArgs := json.RawMessage(`{"path":".chord/plans/20260922-machine-state.md","content":"plan"}`)
	// In the main checkout machine state and checkout content share the root.
	if got := a.toolExecutionPipeline().withMachineStateBaseDir(message.ToolCall{Name: tools.NameWrite, Args: planArgs}).effectiveToolBaseDir(); got != repo {
		t.Fatalf("main-checkout machine-state base dir = %q, want %q", got, repo)
	}

	installed := installTestCheckout(t, a, "feat-state")
	p := a.toolExecutionPipeline()
	if got := p.effectiveToolBaseDir(); got != installed.Path {
		t.Fatalf("pipeline base dir = %q, want the checkout %q", got, installed.Path)
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
	assertNoFile(t, filepath.Join(installed.Path, filepath.FromSlash(plan)))

	// Checkout content still follows the checkout.
	write(t, "src/main.go", "src/main.go")
	assertFileContent(t, filepath.Join(installed.Path, "src", "main.go"), "src/main.go")
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
	if got := p.withMachineStateBaseDir(message.ToolCall{Name: tools.NameWrite, Args: planArgs}).effectiveToolBaseDir(); got != repo {
		t.Errorf("machine-state call base dir = %q, want the content root %q", got, repo)
	}
	srcArgs, err := json.Marshal(map[string]string{"path": "src/main.go", "content": "x"})
	if err != nil {
		t.Fatalf("marshal source args: %v", err)
	}
	if got := p.withMachineStateBaseDir(message.ToolCall{Name: tools.NameWrite, Args: srcArgs}).effectiveToolBaseDir(); got != installed.Path {
		t.Errorf("checkout-content call base dir = %q, want the checkout %q", got, installed.Path)
	}

	// A mixed call is never redirected: applying half a patch to another
	// checkout would be worse than leaving the call on the session's checkout.
	mixed := json.RawMessage(`{"paths":[".chord/plans/x.md","src/main.go"],"reason":"cleanup"}`)
	if got := p.withMachineStateBaseDir(message.ToolCall{Name: tools.NameDelete, Args: mixed}).effectiveToolBaseDir(); got != installed.Path {
		t.Errorf("mixed call base dir = %q, want the checkout %q", got, installed.Path)
	}
}

// TestWorkDirReadsFollowTheActiveCheckout pins the observable form of "reads
// align with the active checkout": one relative path resolves to the checkout
// the session is bound to, so a read returns that copy's content and never the
// other copy's. Both copies hold the file with different content, so the
// returned text names the copy the read resolved against — the registry entry
// was registered with the startup directory, so a read that ignored the binding
// would return the main copy.
func TestWorkDirReadsFollowTheActiveCheckout(t *testing.T) {
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

	installed := installTestCheckout(t, a, "feat-read")
	writeCopy(t, installed.Path, "package main // copy: worktree checkout")
	got := read(t)
	if !strings.Contains(got, "copy: worktree checkout") {
		t.Errorf("read inside the checkout = %q, want the checkout copy", got)
	}
	if strings.Contains(got, "copy: main checkout") {
		t.Errorf("read inside the checkout returned the main checkout's copy: %q", got)
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
// binding contract: a crash replay resolves a call's relative paths against the
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

	installed := installTestCheckout(t, a, "feat-journal")
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
	if checkout.WorkDir != installed.Path {
		t.Errorf("checkout call workdir = %q, want the session's checkout %q", checkout.WorkDir, installed.Path)
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
	subInfo := createTestWorktree(t, a, "feat-sub-journal")
	subState := WorkDirState{Path: subInfo.Path, WorktreeID: subInfo.Name, Branch: subInfo.Branch, Generation: 2}
	sub := newControllableTestSubAgentWithWorkDir(t, a, "task-journal", subState)
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
	if snapshot.WorkDir != subInfo.Path {
		t.Errorf("snapshot workdir = %q, want the worker's checkout %q", snapshot.WorkDir, subInfo.Path)
	}
	if snapshot.WorkDirGeneration != subState.Generation {
		t.Errorf("snapshot generation = %d, want the worker binding's %d", snapshot.WorkDirGeneration, subState.Generation)
	}

	// Moving to another checkout moves the recorded directory with it, so a
	// crash after the switch does not replay into the retired checkout.
	next := adoptTestCheckout(t, a, "feat-journal-next")
	write(t, "call-after-switch", "src/after.go")
	after := readToolActivityJournal(t, journal)["call-after-switch"]
	if after.WorkDir != next.Path {
		t.Errorf("record after the switch = %#v, want workdir %q", after, next.Path)
	}
	if after.WorkDirGeneration != next.Generation {
		t.Errorf("record generation after the switch = %d, want the live binding's %d", after.WorkDirGeneration, next.Generation)
	}
}

// TestWorkDirSwitchRebindsLSP pins the LSP half of the switch: the language
// servers follow the active checkout, and a failed rebind is a warning, never a
// rollback — an agent that switched but could not reconfigure diagnostics is
// still better off than one stuck in the previous checkout.
func TestWorkDirSwitchRebindsLSP(t *testing.T) {
	repo := newWorktreeTestRepo(t)
	a := newTestMainAgent(t, repo)
	var calls []string
	failNext := false
	a.SetWorktreeRuntime(WorktreeRuntime{
		PathLocator:  newWorktreeTestLocator(t),
		RepoRoot:     repo,
		BranchPrefix: worktree.DefaultBranchPrefix,
		RebindLSP: func(dir string) error {
			calls = append(calls, dir)
			if failNext {
				return errors.New("gopls unavailable")
			}
			return nil
		},
	})

	// Installing the startup binding is not a switch: the servers are already
	// configured for the directory the session starts in.
	installTestCheckout(t, a, "feat-lsp")
	if len(calls) != 0 {
		t.Fatalf("rebind calls after installing the startup binding = %v, want none", calls)
	}
	second := adoptTestCheckout(t, a, "feat-lsp-two")
	if len(calls) != 1 || calls[0] != second.Path {
		t.Fatalf("rebind calls after the switch = %v, want [%s]", calls, second.Path)
	}

	failNext = true
	third := adoptTestCheckout(t, a, "feat-lsp-three")
	if last := calls[len(calls)-1]; last != third.Path {
		t.Errorf("last rebind call = %q, want %q", last, third.Path)
	}
	if got := a.effectiveToolBaseDir(); got != third.Path {
		t.Errorf("effectiveToolBaseDir = %q, want the switch kept despite the rebind failure", got)
	}
}

// TestRehydrateTaskRestoresRecordedWorktree verifies a rehydrated worker
// resumes inside the checkout it was working in with its identity intact: the
// per-instance metadata records the worktree path, and the resumed binding
// must carry the name, branch, and base that the environment reminder and
// completion report read.
func TestRehydrateTaskRestoresRecordedWorktree(t *testing.T) {
	a, repo := newWorktreeTestAgent(t, "session-restore-worktree")
	configureNestedDelegationTestRuntime(a, 1)

	sub := newControllableTestSubAgent(t, a, "adhoc-restore-worktree")
	info := createTestWorktree(t, a, "feat-restore")
	sub.workDirState.store(WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: 2,
	})
	// The worker metadata is what a rehydrated worker resumes from.
	if err := a.persistSubAgentMeta(sub); err != nil {
		t.Fatalf("persistSubAgentMeta: %v", err)
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
	if err := os.WriteFile(filepath.Join(info.Path, "AGENTS.md"), []byte("checkout instructions\n"), 0o644); err != nil {
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
	if state.Path != info.Path || state.WorktreeID != info.Name || state.Branch != info.Branch {
		t.Fatalf("restored binding = %#v, want %s at %s", state, info.Name, info.Path)
	}
	if strings.TrimSpace(state.BaseSHA) == "" {
		t.Fatalf("restored binding has no base commit: %#v", state)
	}
	// The worker resumes in the recorded checkout, so it must read the
	// instructions of that checkout rather than the parent's snapshot.
	reminder := subAgentReminderContent(t, restored)
	assertEnvBlockStatesWorktree(t, reminder, info.Path, info.Name, info.Branch)
	if !strings.Contains(reminder, "checkout instructions") || strings.Contains(reminder, "root instructions") {
		t.Fatalf("restored reminder = %q, want the checkout's instructions", reminder)
	}
}

// A recorded checkout that is no longer a worktree of this repository must not
// be resumed as one: the binding falls back to the inherited directory.
func TestRehydrateTaskIgnoresStaleRecordedWorktree(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-restore-stale")
	configureNestedDelegationTestRuntime(a, 1)

	sub := newControllableTestSubAgent(t, a, "adhoc-restore-stale")
	info := createTestWorktree(t, a, "feat-stale")
	sub.workDirState.store(WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: 2,
	})
	if err := a.persistSubAgentMeta(sub); err != nil {
		t.Fatalf("persistSubAgentMeta: %v", err)
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
	a, _ := newWorktreeTestAgent(t, "session-new-checkout")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	oldSessionDir := a.sessionDir

	installed := installTestCheckout(t, a, "feat-new")

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
	if meta.WorktreePath != installed.Path || meta.WorktreeName != installed.WorktreeID || meta.WorktreeBranch != installed.Branch {
		t.Fatalf("new session meta = %+v, want the active checkout %s", meta, installed.Path)
	}
	last := meta.WorktreeTimeline[len(meta.WorktreeTimeline)-1]
	if last.Reason != recovery.WorktreeSwitchStartup || last.Path != installed.Path {
		t.Fatalf("startup boundary = %+v, want reason=%s path=%s", last, recovery.WorktreeSwitchStartup, installed.Path)
	}
}

// An in-process /resume replaces the session without touching the working
// directory; the resumed transcript must be interpreted against the checkout
// the session recorded, not the one the previous session left.
func TestResumeAdoptsRecordedWorktreeCheckout(t *testing.T) {
	a, repo := newWorktreeTestAgent(t, "session-resume-adopt")

	info := createTestWorktree(t, a, "feat-resume")
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		t.Fatalf("projectSessionsDir: %v", err)
	}
	targetDir := filepath.Join(sessionsDir, "target-session")
	persistRestorableSession(t, targetDir)
	if err := recovery.SaveSessionMeta(targetDir, recovery.SessionMeta{
		RepoRoot:       repo,
		WorktreeName:   info.Name,
		WorktreeBranch: info.Branch,
		WorktreePath:   info.Path,
	}); err != nil {
		t.Fatalf("SaveSessionMeta(target): %v", err)
	}

	a.handleResumeCommand("target-session")

	if a.sessionDir != targetDir {
		t.Fatalf("sessionDir = %q, want %q", a.sessionDir, targetDir)
	}
	state := a.workDirState.load()
	if state.Path != info.Path || state.WorktreeID != info.Name || state.Branch != info.Branch {
		t.Fatalf("resumed binding = %#v, want the recorded checkout %s", state, info.Path)
	}
	if strings.TrimSpace(state.BaseSHA) == "" {
		t.Fatalf("resumed binding has no base commit: %#v", state)
	}
	meta, err := recovery.LoadSessionMeta(targetDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta(target): %v", err)
	}
	if meta == nil || meta.WorktreePath != info.Path {
		t.Fatalf("resumed session meta = %+v, want the adopted checkout", meta)
	}
}

// A worker delegated in the main checkout must come back to it even after the
// parent moved into a worktree. The recorded directory is not a chord-managed
// worktree, but it still belongs to this repository, so falling back to the
// parent's current checkout would reinterpret the worker's transcript against
// a different tree.
func TestRehydrateTaskKeepsRecordedMainCheckoutAfterParentSwitches(t *testing.T) {
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

	installed := installTestCheckout(t, a, "feat-parent")
	if a.effectiveToolBaseDir() == repo {
		t.Fatal("parent should be working in the checkout")
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
	// Model the resumed process: the main agent is working in the checkout the
	// parent installed, so the worker's record is the only source of its tree.
	restoredAgent.workDirState.store(WorkDirState{Path: installed.Path, WorktreeID: installed.WorktreeID, Branch: installed.Branch, Generation: 1})

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
	a, _ := newWorktreeTestAgent(t, "session-new-gone-checkout")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	oldSessionDir := a.sessionDir

	installed := installTestCheckout(t, a, "feat-new-gone")
	if err := os.RemoveAll(installed.Path); err != nil {
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
// republish the claim — but the rest of its record still has to land, or the
// worker loses its state.
func TestSubAgentMetaDropsAClaimOnARemovedCheckout(t *testing.T) {
	a, _ := newWorktreeTestAgent(t, "session-sub-gone-checkout")
	sub := newControllableTestSubAgent(t, a, "adhoc-gone-checkout")

	info := createTestWorktree(t, a, "feat-sub-gone")
	sub.workDirState.store(WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: 3,
	})
	if err := os.RemoveAll(info.Path); err != nil {
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

// TestSubAgentBindingIsIndependentOfMainAgent pins the per-agent ownership of
// the binding: a worker's checkout never moves the session's own.
func TestSubAgentBindingIsIndependentOfMainAgent(t *testing.T) {
	a, repo := newWorktreeTestAgent(t, "session-owner")

	info := createTestWorktree(t, a, "feat-sub")
	sub := newControllableTestSubAgentWithWorkDir(t, a, "task-1", WorkDirState{
		Path:       info.Path,
		WorktreeID: info.Name,
		Branch:     info.Branch,
		BaseSHA:    info.BaseSHA,
		Generation: 1,
	})
	if got := sub.effectiveToolBaseDir(); got != info.Path {
		t.Errorf("sub base dir = %q, want %q", got, info.Path)
	}
	if got := a.effectiveToolBaseDir(); got != repo {
		t.Errorf("main base dir = %q, want it untouched at %q", got, repo)
	}
	if path := a.workDirState.load().Path; path != "" {
		t.Errorf("main binding = %q, want it untouched by the sub's binding", path)
	}
}
