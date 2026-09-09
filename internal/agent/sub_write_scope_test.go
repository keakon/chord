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

func TestSubAgentScopeAndToolsUseTheSameExecutionRoot(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	root := t.TempDir()
	parent.cachedWorkDir = filepath.Join(root, "workspace")
	sub.workDir = parent.cachedWorkDir

	pipeline := sub.toolExecutionPipeline()
	if got := parent.writeScopeBaseDir(); got != sub.workDir {
		t.Fatalf("scope base = %q, want worker root %q", got, sub.workDir)
	}
	if pipeline.toolBaseDir != sub.workDir {
		t.Fatalf("tool base = %q, want worker root %q", pipeline.toolBaseDir, sub.workDir)
	}
	if pipeline.writeScopeDir != sub.workDir {
		t.Fatalf("scope runtime base = %q, want worker root %q", pipeline.writeScopeDir, sub.workDir)
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

// Whether a worker may modify files is decided by its role's permission
// ruleset: a role that denies every file-modifying tool never gets write, edit,
// delete, or apply_patch registered. The write-scope gate therefore only ever
// sees file tools that a role actually allows; an empty expected_write_scope is
// safe exactly because such a role registers none of them.
func TestSubAgentNoFileWriteToolRoleRegistersNoFileMutationTools(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameWrite, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameEdit, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameDelete, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameApplyPatch, Pattern: "*", Action: permission.ActionDeny},
	}
	_, sub := newScopedToolSurfaceTestSubAgent(t, tools.WriteScope{}, ruleset)
	for _, name := range []string{tools.NameWrite, tools.NameEdit, tools.NameDelete, tools.NameApplyPatch} {
		if _, ok := sub.tools.Get(name); ok {
			t.Fatalf("file-modifying tool %s registered for a role that denies it", name)
		}
	}
	for _, name := range []string{tools.NameRead, tools.NameShell, tools.NameSpawn} {
		if _, ok := sub.tools.Get(name); !ok {
			t.Fatalf("non-file tool %s lost for a role that only denies file writes", name)
		}
	}
}

// Command tools bypass the write-scope gate for every scope: whether the
// worker may run shell or spawn is the role permission rules' decision, not
// the task's declared paths — even for a role whose ruleset denies every
// file-modifying tool. The shell tool is registered here (the role keeps it),
// so the gate reaches its shell exemption branch instead of short-circuiting
// on an unknown tool name.
func TestSubAgentNoFileWriteToolRoleAllowsCommandToolsAtTheGate(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameWrite, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameEdit, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameDelete, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameApplyPatch, Pattern: "*", Action: permission.ActionDeny},
	}
	_, sub := newScopedToolSurfaceTestSubAgent(t, tools.WriteScope{PathPrefix: []string{"internal"}}, ruleset)
	for _, name := range []string{tools.NameShell, tools.NameSpawn} {
		args, _ := json.Marshal(map[string]string{"command": "git status", "description": "read-only status check"})
		if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "cmd", Name: name, Args: args}); err != nil {
			t.Fatalf("no-file-write-tool scope gate rejected %s: %v", name, err)
		}
	}
}

func TestSubAgentCoordinationToolsAllowedAtTheGate(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
	args, _ := json.Marshal(map[string]string{"summary": "review complete"})
	if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "complete", Name: tools.NameComplete, Args: args}); err != nil {
		t.Fatalf("path-scoped Complete rejected: %v", err)
	}
}

// Command tools bypass the write-scope gate for every path-based scope too: a
// command line can mutate anywhere it reaches, so its side effects cannot be
// path-validated; availability is decided by the role's permission rules
// instead (see TestSubAgentCommandSurfaceFollowsRoleRulesNotScope). The tools
// are registered so the gate actually reaches its shell/spawn exemption
// branch instead of short-circuiting on an unknown tool name.
func TestSubAgentPathScopeAllowsCommandToolsAtTheGate(t *testing.T) {
	for _, scope := range []tools.WriteScope{
		{PathPrefix: []string{"internal"}},
		{Files: []string{"internal/a.go"}},
		{Modules: []string{"backend"}},
	} {
		_, sub := newMixedBatchTestSubAgent(t)
		sub.tools.Register(tools.NewShellTool("bash"))
		sub.tools.Register(tools.SpawnTool{})
		sub.writeScope = scope
		for _, name := range []string{tools.NameShell, tools.NameSpawn} {
			args, _ := json.Marshal(map[string]string{"command": "go test ./internal/a", "description": "verification"})
			if err := sub.toolExecutionPipeline().validateWriteScope(message.ToolCall{ID: "cmd", Name: name, Args: args}); err != nil {
				t.Fatalf("%s scope gate rejected %s: %v", scope.Summary(), name, err)
			}
		}
	}
}

// newScopedToolSurfaceTestSubAgent builds a SubAgent whose base registry
// contains Shell and Spawn so tests can assert what the registration path
// keeps for a given write scope and role ruleset.
func newScopedToolSurfaceTestSubAgent(t *testing.T, scope tools.WriteScope, ruleset permission.Ruleset) (*MainAgent, *SubAgent) {
	t.Helper()
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.WriteTool{})
	reg.Register(tools.NewShellTool("bash"))
	reg.Register(tools.SpawnTool{})
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
		Ruleset:      ruleset,
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	sub.turn = &Turn{ID: 1, Epoch: 1, Ctx: context.Background()}
	return parent, sub
}

// TestSubAgentCommandSurfaceFollowsRoleRulesNotScope pins the delegated
// command-tool surface: Shell and Spawn stay registered for every write scope
// — empty, path/file/module-scoped, and no-file-write-tool-role tasks alike —
// unless the role's permission rules deny the tool. A wildcard-deny rule
// removes the tool from the registry, the frozen tool definitions sent to the
// model, and the capability prompt, which then renders the Command Execution
// Boundary so the worker does not chase builds and tests it can never run. A
// write scope never removes command tools: the execution-time write-scope gate
// checks file-modifying tools only, and a role that denies file writes
// expresses that through the file tools it keeps out of the registry.
func TestSubAgentCommandSurfaceFollowsRoleRulesNotScope(t *testing.T) {
	deny := func(name string) permission.Ruleset {
		return permission.Ruleset{{Permission: name, Pattern: "*", Action: permission.ActionDeny}}
	}
	for _, tc := range []struct {
		name   string
		scope  tools.WriteScope
		rules  permission.Ruleset
		denied string
	}{
		{name: "empty-scope-shell-kept", scope: tools.WriteScope{}},
		{name: "path-scope-shell-kept", scope: tools.WriteScope{PathPrefix: []string{"internal"}}},
		{name: "file-scope-shell-kept", scope: tools.WriteScope{Files: []string{"internal/a.go"}}},
		{name: "module-scope-shell-kept", scope: tools.WriteScope{Modules: []string{"backend"}}},
		{name: "empty-scope-shell-denied", scope: tools.WriteScope{}, rules: deny(tools.NameShell), denied: tools.NameShell},
		{name: "path-scope-shell-denied", scope: tools.WriteScope{PathPrefix: []string{"internal"}}, rules: deny(tools.NameShell), denied: tools.NameShell},
		{name: "empty-scope-spawn-denied", scope: tools.WriteScope{}, rules: deny(tools.NameSpawn), denied: tools.NameSpawn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sub := newScopedToolSurfaceTestSubAgent(t, tc.scope, tc.rules)
			for _, kept := range []string{tools.NameShell, tools.NameSpawn} {
				if kept == tc.denied {
					if _, ok := sub.tools.Get(tc.denied); ok {
						t.Fatalf("denied tool %s still registered for %s", tc.denied, tc.scope.Summary())
					}
					continue
				}
				if _, ok := sub.tools.Get(kept); !ok {
					t.Fatalf("allowed command tool %s lost for scope %s", kept, tc.scope.Summary())
				}
			}
			for _, def := range sub.frozenToolDefs {
				if def.Name == tc.denied {
					t.Fatalf("frozen tool definitions still contain denied tool %s", tc.denied)
				}
			}
			prompt := sub.buildSystemPrompt()
			if tc.denied == tools.NameShell {
				for _, want := range []string{
					"## Command Execution Boundary",
					"`shell` tool is not available in this task",
					"Execution-based verification is the owner agent's responsibility",
				} {
					if !strings.Contains(prompt, want) {
						t.Fatalf("denied-shell prompt missing %q:\n%s", want, prompt)
					}
				}
			} else if strings.Contains(prompt, "## Command Execution Boundary") {
				t.Fatalf("scope %s prompt unexpectedly has a command execution boundary:\n%s", tc.scope.Summary(), prompt)
			}
		})
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
