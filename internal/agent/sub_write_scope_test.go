package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type mutatingScopeTestTool struct{ name string }

func (t mutatingScopeTestTool) Name() string             { return t.name }
func (mutatingScopeTestTool) Description() string        { return "mutates workspace" }
func (mutatingScopeTestTool) Parameters() map[string]any { return nil }
func (mutatingScopeTestTool) IsReadOnly() bool           { return false }
func (mutatingScopeTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestSubAgentWriteScopeAllowsDeclaredFileAndRejectsOtherFile(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{Files: []string{"allowed.txt"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	allowedArgs, _ := json.Marshal(map[string]string{"path": "allowed.txt", "content": "ok"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "allowed", Name: tools.NameWrite, Args: allowedArgs}); err != nil {
		t.Fatalf("declared write failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "allowed.txt")); err != nil || string(got) != "ok" {
		t.Fatalf("allowed file = %q, %v", got, err)
	}

	deniedArgs, _ := json.Marshal(map[string]string{"path": "other.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "denied", Name: tools.NameWrite, Args: deniedArgs}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out-of-scope write error = %v, want scope rejection", err)
	}
	if _, err := os.Stat(filepath.Join(root, "other.txt")); !os.IsNotExist(err) {
		t.Fatalf("out-of-scope file unexpectedly exists: %v", err)
	}
}

func TestSubAgentWriteScopeRejectsLaterApplyPatchTargetOutsideScope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"allowed"}}
	sub.tools.Register(tools.ApplyPatchTool{BaseDir: root})

	args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: allowed/ok.txt\n+ok\n*** Add File: outside.txt\n+blocked\n*** End Patch"}`)
	_, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "patch", Name: tools.NameApplyPatch, Args: args})
	if err == nil || !strings.Contains(err.Error(), "outside this SubAgent task's expected_write_scope") {
		t.Fatalf("error = %v, want second-target scope rejection", err)
	}
	for _, path := range []string{"allowed/ok.txt", "outside.txt"} {
		if _, statErr := os.Stat(filepath.Join(root, path)); !os.IsNotExist(statErr) {
			t.Fatalf("%s exists after rejected patch: %v", path, statErr)
		}
	}
}

func TestSubAgentWriteScopeRejectsApplyPatchMoveTargetOutsideScope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"allowed"}}
	sub.tools.Register(tools.ApplyPatchTool{BaseDir: root})
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "allowed", "old.txt")
	if err := os.WriteFile(source, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: allowed/old.txt\n*** Move to: outside.txt\n@@\n-old\n+new\n*** End Patch"}`)
	_, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "move", Name: tools.NameApplyPatch, Args: args})
	if err == nil || !strings.Contains(err.Error(), "outside this SubAgent task's expected_write_scope") {
		t.Fatalf("error = %v, want move-target scope rejection", err)
	}
	if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "old\n" {
		t.Fatalf("source = %q, %v; want unchanged", got, readErr)
	}
}

func TestSubAgentReadOnlyScopeRejectsMutatingTool(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{ReadOnly: true}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "blocked.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "blocked", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only write error = %v, want rejection", err)
	}
}

func TestSubAgentReadOnlyScopeRejectsShellEvenWhenCommandLooksReadOnly(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{ReadOnly: true}
	sub.tools.Register(tools.ShellTool{})

	args, _ := json.Marshal(map[string]string{"command": "git branch scope-bypass", "description": "attempt hidden mutation"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "shell-read", Name: tools.NameShell, Args: args}); err == nil || !strings.Contains(err.Error(), "shell is unavailable") {
		t.Fatalf("scoped Shell error = %v, want conservative rejection", err)
	}
}

func TestSubAgentReadOnlyScopeAllowsCoordinationTools(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.writeScope = tools.WriteScope{ReadOnly: true}
	args, _ := json.Marshal(map[string]string{"summary": "review complete"})
	if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "complete", Name: tools.NameComplete, Args: args}); err != nil {
		t.Fatalf("read-only Complete rejected: %v", err)
	}
}

func TestSubAgentPathScopeRejectsMutatingShell(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	sub.tools.Register(tools.ShellTool{})

	args, _ := json.Marshal(map[string]string{"command": "touch internal/file.txt", "description": "mutate file"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "shell", Name: tools.NameShell, Args: args}); err == nil || !strings.Contains(err.Error(), "shell is unavailable") {
		t.Fatalf("mutating shell error = %v, want scope-safe rejection", err)
	}
}

// TestSubAgentPathScopeRejectsReadOnlyShellCommand pins the P1-4 contract that
// a path-scoped task rejects every shell command — including one whose command
// line looks read-only — because command side effects cannot be path-validated.
// The scoped worker therefore cannot execute any verification command, which is
// why its tool surface no longer advertises Shell at all.
func TestSubAgentPathScopeRejectsReadOnlyShellCommand(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	sub.tools.Register(tools.ShellTool{})

	args, _ := json.Marshal(map[string]string{"command": "git status", "description": "read-only status check"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "shell-readonly", Name: tools.NameShell, Args: args}); err == nil || !strings.Contains(err.Error(), "shell is unavailable") {
		t.Fatalf("read-only shell error = %v, want scope-safe rejection", err)
	}
}

// newScopedToolSurfaceTestSubAgent builds a SubAgent whose base registry
// contains Shell so tests can assert what the registration path keeps for a
// given write scope.
func newScopedToolSurfaceTestSubAgent(t *testing.T, scope tools.WriteScope) (*MainAgent, *SubAgent) {
	t.Helper()
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.WriteTool{})
	reg.Register(tools.NewShellTool("bash"))
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-scoped",
		TaskID:       "adhoc-scoped",
		AgentDefName: "worker",
		TaskDesc:     "do scoped work",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recoveryManager(),
		SessionEpoch: parent.recoverySessionEpoch(),
		Parent:       parent,
		ParentCtx:    parent.parentCtx,
		Cancel:       func() {},
		BaseTools:    reg,
		WriteScope:   scope,
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	sub.turn = &Turn{ID: 1, Epoch: 1, Ctx: context.Background()}
	return parent, sub
}

// TestSubAgentScopedAndReadOnlySurfaceOmitsShell pins the P1-4 fix: scoped
// (path/file/module) and read-only delegated tasks no longer register Shell, so
// Shell disappears from the tool registry, the frozen tool definitions sent to
// the model, and the capability prompt — and the prompt explicitly says command
// execution and execution-based verification belong to the owner agent instead
// of guiding the worker to run tests it can never execute.
func TestSubAgentScopedAndReadOnlySurfaceOmitsShell(t *testing.T) {
	for _, scope := range []tools.WriteScope{
		{PathPrefix: []string{"internal"}},
		{Files: []string{"internal/a.go"}},
		{Modules: []string{"backend"}},
		{ReadOnly: true},
	} {
		scope := scope
		t.Run(scope.Summary(), func(t *testing.T) {
			_, sub := newScopedToolSurfaceTestSubAgent(t, scope)
			if _, ok := sub.tools.Get(tools.NameShell); ok {
				t.Fatal("scoped SubAgent registry still contains Shell")
			}
			for _, def := range sub.frozenToolDefs {
				if def.Name == tools.NameShell {
					t.Fatalf("scoped SubAgent frozen tool definitions still contain Shell: %#v", def)
				}
			}
			prompt := sub.buildSystemPrompt()
			for _, want := range []string{
				"## Command Execution Boundary",
				"`shell` tool is not available in this task",
				"Execution-based verification is the owner agent's responsibility",
			} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("scoped SubAgent prompt missing %q:\n%s", want, prompt)
				}
			}
		})
	}
}

// TestSubAgentUnscopedSurfaceKeepsShell guards the opposite side of the P1-4
// fix: an unscoped delegated task still advertises Shell and gets no command
// execution boundary block.
func TestSubAgentUnscopedSurfaceKeepsShell(t *testing.T) {
	_, sub := newScopedToolSurfaceTestSubAgent(t, tools.WriteScope{})
	if _, ok := sub.tools.Get(tools.NameShell); !ok {
		t.Fatal("unscoped SubAgent registry lost Shell")
	}
	foundShell := false
	for _, def := range sub.frozenToolDefs {
		if def.Name == tools.NameShell {
			foundShell = true
			break
		}
	}
	if !foundShell {
		t.Fatal("unscoped SubAgent frozen tool definitions lost Shell")
	}
	if prompt := sub.buildSystemPrompt(); strings.Contains(prompt, "## Command Execution Boundary") {
		t.Fatalf("unscoped SubAgent prompt unexpectedly got a command execution boundary:\n%s", prompt)
	}
}

func TestSubAgentModuleOnlyScopeRequiresPathDeclaration(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{Modules: []string{"backend"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "backend/file.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "module", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "logical modules") {
		t.Fatalf("module-only scope error = %v, want path declaration requirement", err)
	}
}

func TestSubAgentPathScopeRejectsUnknownMutatingTool(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.projectRoot = t.TempDir()
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	sub.tools.Register(mutatingScopeTestTool{name: "CustomMutator"})

	if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "custom", Name: "CustomMutator"}); err == nil || !strings.Contains(err.Error(), "cannot validate") {
		t.Fatalf("unknown mutating tool error = %v, want conservative rejection", err)
	}
}

func TestSubAgentPathScopeRejectsSymlinkEscape(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "allowed", "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"allowed"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "allowed/link/escaped.txt", "content": "no"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "escape", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape error = %v, want scope rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped file unexpectedly exists: %v", err)
	}
}

func TestSubAgentEmptyScopePreservesLegacyWriteBehavior(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{}
	sub.tools.Register(tools.WriteTool{BaseDir: root})

	args, _ := json.Marshal(map[string]string{"path": "legacy.txt", "content": "ok"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "legacy", Name: tools.NameWrite, Args: args}); err != nil {
		t.Fatalf("empty-scope legacy write failed: %v", err)
	}
}

func TestSubAgentScopeRejectsUserEditedArgsOutsideScope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"allowed"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})
	sub.setRuleset(permission.Ruleset{{Permission: tools.NameWrite, Pattern: "*", Action: permission.ActionAsk}})
	parent.confirmFn = func(context.Context, string, string, []string, []string, []string, []string) (ConfirmResponse, error) {
		return ConfirmResponse{
			Approved:      true,
			FinalArgsJSON: `{"path":"outside.txt","content":"blocked"}`,
			EditSummary:   "changed path",
		}, nil
	}

	args, _ := json.Marshal(map[string]string{"path": "allowed/inside.txt", "content": "ok"})
	result, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "write-confirm", Name: tools.NameWrite, Args: args})
	if err == nil || !strings.Contains(err.Error(), "outside this SubAgent task's expected_write_scope") {
		t.Fatalf("edited write error = %v, want scope rejection", err)
	}
	if result.EffectiveArgsJSON != `{"path":"outside.txt","content":"blocked"}` {
		t.Fatalf("effective args = %q, want edited args", result.EffectiveArgsJSON)
	}
	if _, statErr := os.Stat(filepath.Join(root, "outside.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside file exists after rejected edit: %v", statErr)
	}
}

func TestSubAgentScopeRejectsHookModifiedArgsOutsideScope(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.projectRoot = root
	sub.workDir = root
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"allowed"}}
	sub.tools.Register(tools.WriteTool{BaseDir: root})
	parent.hookEngine = fixedToolHookEngine{result: &hook.Result{
		Action: hook.ActionModify,
		Data: map[string]any{"args": map[string]any{
			"path":    filepath.Join(root, "outside.txt"),
			"content": "blocked",
		}},
	}}

	args, _ := json.Marshal(map[string]string{"path": "allowed/inside.txt", "content": "ok"})
	result, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "write-hook", Name: tools.NameWrite, Args: args})
	if err == nil || !strings.Contains(err.Error(), "outside this SubAgent task's expected_write_scope") {
		t.Fatalf("hook-modified write error = %v, want scope rejection", err)
	}
	if !strings.Contains(result.EffectiveArgsJSON, "outside.txt") {
		t.Fatalf("effective args = %q, want hook-modified path", result.EffectiveArgsJSON)
	}
	if _, statErr := os.Stat(filepath.Join(root, "outside.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside file exists after rejected hook mutation: %v", statErr)
	}
}
