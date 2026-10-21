package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// Whether a worker may modify files is decided by its role's permission
// ruleset: a role that denies every file-modifying tool never gets write, edit,
// delete, or apply_patch registered.
func TestSubAgentNoFileWriteToolRoleRegistersNoFileMutationTools(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: tools.NameRead, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameWrite, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameEdit, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameDelete, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameApplyPatch, Pattern: "*", Action: permission.ActionDeny},
	}
	_, sub := newToolSurfaceTestSubAgent(t, ruleset)
	for _, name := range []string{tools.NameWrite, tools.NameEdit, tools.NameDelete, tools.NameApplyPatch} {
		if _, ok := sub.tools.Get(name); ok {
			t.Fatalf("file-modifying tool %s registered for a role that denies it", name)
		}
	}
	for _, name := range []string{tools.NameRead, tools.NameShell, tools.NameJobOutput} {
		if _, ok := sub.tools.Get(name); !ok {
			t.Fatalf("non-file tool %s lost for a role that only denies file writes", name)
		}
	}
}

// newToolSurfaceTestSubAgent builds a SubAgent whose base registry contains
// Shell and the job control tools so tests can assert what the registration
// path keeps for a role ruleset.
func newToolSurfaceTestSubAgent(t *testing.T, ruleset permission.Ruleset) (*MainAgent, *SubAgent) {
	t.Helper()
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.WriteTool{})
	reg.Register(tools.NewShellTool("bash"))
	reg.Register(tools.JobOutputTool{})
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
		Ruleset:      ruleset,
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	sub.turn = &Turn{ID: 1, Epoch: 1, Ctx: context.Background()}
	return parent, sub
}

// TestSubAgentCommandSurfaceFollowsRoleRules pins the delegated command-tool
// surface: Shell and the job control tools stay registered unless the role's
// permission rules deny the tool. A wildcard-deny rule removes the tool from
// the registry, the frozen tool definitions sent to the model, and the
// capability prompt, which then renders the Command Execution Boundary so the
// worker does not chase builds and tests it can never run.
func TestSubAgentCommandSurfaceFollowsRoleRules(t *testing.T) {
	deny := func(name string) permission.Ruleset {
		return permission.Ruleset{{Permission: name, Pattern: "*", Action: permission.ActionDeny}}
	}
	for _, tc := range []struct {
		name   string
		rules  permission.Ruleset
		denied string
	}{
		{name: "shell-kept"},
		{name: "shell-denied", rules: deny(tools.NameShell), denied: tools.NameShell},
		{name: "joboutput-denied", rules: deny(tools.NameJobOutput), denied: tools.NameJobOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sub := newToolSurfaceTestSubAgent(t, tc.rules)
			for _, kept := range []string{tools.NameShell, tools.NameJobOutput} {
				if kept == tc.denied {
					if _, ok := sub.tools.Get(tc.denied); ok {
						t.Fatalf("denied tool %s still registered", tc.denied)
					}
					continue
				}
				if _, ok := sub.tools.Get(kept); !ok {
					t.Fatalf("allowed command tool %s lost", kept)
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
				t.Fatalf("prompt unexpectedly has a command execution boundary:\n%s", prompt)
			}
		})
	}
}
