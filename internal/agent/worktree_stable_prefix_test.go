package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// registerCheckoutSurfaceTools widens the harness surface with the
// collaborator-free part of the production tool set, so the stability
// assertions cover descriptions and schemas beyond the WriteTool that the
// worktree harness installs on its own.
func registerCheckoutSurfaceTools(t *testing.T, a *MainAgent, baseDir string) {
	t.Helper()
	a.tools.Register(tools.ReadTool{BaseDir: baseDir})
	a.tools.Register(tools.EditTool{BaseDir: baseDir})
	a.tools.Register(tools.ApplyPatchTool{BaseDir: baseDir})
	a.tools.Register(tools.DeleteTool{BaseDir: baseDir})
	a.tools.Register(tools.GrepTool{BaseDir: baseDir})
	a.tools.Register(tools.GlobTool{BaseDir: baseDir})
	a.tools.Register(tools.ShellTool{BaseDir: baseDir})
}

func mainAgentReminderContent(t *testing.T, a *MainAgent) string {
	t.Helper()
	ptr := a.cachedSessionReminderContent.Load()
	if ptr == nil {
		t.Fatal("MainAgent has no session-context reminder")
	}
	return *ptr
}

// assertSurfaceOmitsPaths fails when a model-visible surface embeds one of the
// checkouts: the provider caches the system prompt and the tool definitions as
// a prefix, so a directory baked into either one invalidates the whole prefix
// whenever the active checkout changes.
func assertSurfaceOmitsPaths(t *testing.T, label, surface string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if path == "" || !strings.Contains(surface, path) {
			continue
		}
		t.Errorf("%s mentions the checkout path %q; model-visible descriptions and schemas must stay checkout-free", label, path)
	}
}

func assertToolDefsOmitPaths(t *testing.T, defs []message.ToolDefinition, paths ...string) {
	t.Helper()
	for _, def := range defs {
		assertSurfaceOmitsPaths(t, "tool "+def.Name+" description", def.Description, paths...)
		schema, err := json.Marshal(def.InputSchema)
		if err != nil {
			t.Fatalf("marshal the input schema of %s: %v", def.Name, err)
		}
		assertSurfaceOmitsPaths(t, "tool "+def.Name+" input schema", string(schema), paths...)
	}
}

// toolSurfaceChange names the first field that differs between two tool
// surfaces, so a failure points at the tool that changed the cached prefix
// instead of dumping both surfaces.
func toolSurfaceChange(before, after []message.ToolDefinition) string {
	if len(before) != len(after) {
		return fmt.Sprintf("tool count %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Name != after[i].Name {
			return fmt.Sprintf("slot %d: %s -> %s", i, before[i].Name, after[i].Name)
		}
		if before[i].Description != after[i].Description {
			return before[i].Name + ": description changed"
		}
		beforeSchema, beforeErr := json.Marshal(before[i].InputSchema)
		afterSchema, afterErr := json.Marshal(after[i].InputSchema)
		if beforeErr != nil || afterErr != nil || string(beforeSchema) != string(afterSchema) {
			return before[i].Name + ": input schema changed"
		}
	}
	return "surfaces differ in a field this helper does not compare"
}

// TestWorktreeSwitchKeepsModelVisibleSurfaceStable pins the cache contract for
// a volatile working directory: the system prompt and the tool descriptions and
// schemas are the provider's cached prefix, so entering or leaving a worktree
// must not rewrite them. The active checkout reaches the model only through the
// per-request session-context reminder, which is rebuilt on the switch.
func TestWorktreeSwitchKeepsModelVisibleSurfaceStable(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-stable-prefix")
	registerCheckoutSurfaceTools(t, a, repo)
	a.refreshSessionContextReminder()

	prompt := a.currentSystemPromptCandidate()
	defs := llmToolDefinitionsFromVisibleTools(a.stableVisibleLLMTools())
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("expected a rendered system prompt")
	}
	if len(defs) < 2 {
		t.Fatalf("expected a multi-tool surface, got %d definitions", len(defs))
	}
	assertSurfaceOmitsPaths(t, "the system prompt", prompt, repo)
	assertToolDefsOmitPaths(t, defs, repo)
	if reminder := mainAgentReminderContent(t, a); !strings.Contains(reminder, "Working directory: "+repo) {
		t.Fatalf("the reminder should state the startup directory %q:\n%s", repo, reminder)
	}

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-stable"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}

	promptAfter := a.currentSystemPromptCandidate()
	if promptAfter != prompt {
		t.Errorf("the system prompt changed when a worktree was entered; it must stay the cached prefix")
	}
	entered := llmToolDefinitionsFromVisibleTools(a.stableVisibleLLMTools())
	if !reflect.DeepEqual(entered, defs) {
		t.Errorf("the tool surface changed when a worktree was entered: %s", toolSurfaceChange(defs, entered))
	}
	assertSurfaceOmitsPaths(t, "the system prompt after entering a worktree", promptAfter, res.Path)
	assertToolDefsOmitPaths(t, entered, res.Path)
	if a.surfaceDirty.Load() {
		t.Error("entering a worktree must not mark the runtime surface dirty: rebuilding re-sends the cached prefix")
	}
	assertEnvBlockStatesWorktree(t, mainAgentReminderContent(t, a), res.Path, "feat-stable", res.Branch)

	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-stable"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	if got := a.currentSystemPromptCandidate(); got != prompt {
		t.Error("the system prompt changed when the worktree was left; it must stay the cached prefix")
	}
	left := llmToolDefinitionsFromVisibleTools(a.stableVisibleLLMTools())
	if !reflect.DeepEqual(left, defs) {
		t.Errorf("the tool surface changed when the worktree was left: %s", toolSurfaceChange(defs, left))
	}
	after := mainAgentReminderContent(t, a)
	if !strings.Contains(after, "Working directory: "+repo) {
		t.Fatalf("the reminder should go back to the startup directory %q:\n%s", repo, after)
	}
	if strings.Contains(after, "Worktree:") {
		t.Fatalf("the reminder still claims a worktree after leaving it:\n%s", after)
	}
}

// TestWorktreeSwitchReanchorsShellWorkingDirectory pins the execution half of
// the contract: a tool call runs against the checkout that was active when its
// batch was dispatched, so a shell command issued inside a fresh worktree runs
// there even though the registered ShellTool still carries the startup
// directory.
func TestWorktreeSwitchReanchorsShellWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	a, repo := newWorktreeTestAgent(t, "session-shell-reanchor")
	a.tools.Register(tools.ShellTool{BaseDir: repo})

	assertShellPwd(t, a.toolExecutionPipeline(), repo)

	res, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-shell"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	assertShellPwd(t, a.toolExecutionPipeline(), res.Path)

	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-shell"}); err != nil {
		t.Fatalf("WorktreeExit: %v", err)
	}
	assertShellPwd(t, a.toolExecutionPipeline(), repo)
}

// assertShellPwd runs `pwd` through the pipeline and compares the printed
// directory with want after resolving symlinks, because a temp directory is
// commonly reached through a symlinked path.
func assertShellPwd(t *testing.T, p toolExecutionPipeline, want string) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": "pwd"})
	if err != nil {
		t.Fatalf("marshal the shell arguments: %v", err)
	}
	out, err := p.executeToolForCall(context.Background(), message.ToolCall{ID: "shell-pwd", Name: tools.NameShell, Args: args})
	if err != nil {
		t.Fatalf("shell pwd in %s: %v (output %q)", want, err, out)
	}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	got, gotErr := filepath.EvalSymlinks(strings.TrimSpace(firstLine))
	wantResolved, wantErr := filepath.EvalSymlinks(want)
	if gotErr != nil || wantErr != nil {
		t.Fatalf("EvalSymlinks pwd=%q err=%v want=%q err=%v (output %q)", firstLine, gotErr, want, wantErr, out)
	}
	if got != wantResolved {
		t.Errorf("shell pwd = %q, want %q", got, wantResolved)
	}
}
