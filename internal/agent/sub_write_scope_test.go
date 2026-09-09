package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// Whether a worker may modify files is decided by its role's permission
// ruleset: a role that denies every file-modifying tool never gets write, edit,
// delete, or apply_patch registered. expected_write_scope is a required
// declaration only for roles that can register file tools; an empty
// declaration is safe exactly because such a role registers none of them, and
// file access is enforced by the ruleset, not by declared paths.
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
// write scope never removes command tools: which tools a role may use is
// decided by its permission rules — the file-modifying tools the rules keep
// registered, and whether shell/spawn survive wildcard-deny rules.
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

// expected_write_scope is a declaration, not an execution gate: a scoped
// worker may write any file its role's permission rules allow, within or
// beyond the declared paths. Scope declarations inform task placement and
// overlap advice; file access is controlled by the ruleset exactly as for the
// MainAgent, so no declared scope may suppress or require permission for a
// file write.
func TestSubAgentWriteScopeDoesNotGateFileToolExecution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope tools.WriteScope
	}{
		{name: "empty", scope: tools.WriteScope{}},
		{name: "path-prefix", scope: tools.WriteScope{PathPrefix: []string{"internal"}}},
		{name: "files", scope: tools.WriteScope{Files: []string{"declared.txt"}}},
		{name: "modules", scope: tools.WriteScope{Modules: []string{"backend"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			root := t.TempDir()
			parent.projectRoot = root
			sub.workDir = root
			sub.writeScope = tc.scope
			sub.tools.Register(tools.WriteTool{BaseDir: root})

			for _, target := range []string{"declared.txt", "elsewhere.txt"} {
				args, _ := json.Marshal(map[string]string{"path": target, "content": "ok"})
				if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "w-" + target, Name: tools.NameWrite, Args: args}); err != nil {
					t.Fatalf("scope %s: write to %q rejected: %v", tc.scope.Summary(), target, err)
				}
				if got, err := os.ReadFile(filepath.Join(root, target)); err != nil || string(got) != "ok" {
					t.Fatalf("scope %s: %s = %q, %v; want written", tc.scope.Summary(), target, got, err)
				}
			}
		})
	}
}
