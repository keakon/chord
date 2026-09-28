package agent

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// TestWorktreeExitPermissionMatchesAction pins the action-scoped rule syntax:
// `worktree_exit: {remove: deny}` must reject a removal — including one that
// discards changes — while leaving (`keep`, or an omitted action, which
// Execute treats as keep) stays allowed. Without extracting the action every
// call matched "*", so the deny silently fell through to the wildcard rule.
func TestWorktreeExitPermissionMatchesAction(t *testing.T) {
	rs := permissionRuleset(t, `
"*": allow
worktree_exit: {remove: deny}
`)
	cases := []struct {
		name string
		args string
		want permission.Action
		arg  string
	}{
		{name: "remove denied", args: `{"action":"remove"}`, want: permission.ActionDeny, arg: "remove"},
		{name: "remove with discard denied", args: `{"action":"remove","discard_changes":true}`, want: permission.ActionDeny, arg: "remove"},
		{name: "action spelling normalized", args: `{"action":" Remove "}`, want: permission.ActionDeny, arg: "remove"},
		{name: "keep still allowed", args: `{"action":"keep"}`, want: permission.ActionAllow, arg: "keep"},
		{name: "omitted action is keep", args: `{}`, want: permission.ActionAllow, arg: "keep"},
		{name: "no arguments is keep", args: ``, want: permission.ActionAllow, arg: "keep"},
		{name: "unknown action matches no action rule", args: `{"action":"delete"}`, want: permission.ActionAllow, arg: "*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := evaluateToolPermissionInDir(rs, tools.NameWorktreeExit, json.RawMessage(tc.args), permission.PathScope{})
			if dec.Action != tc.want {
				t.Fatalf("action = %q, want %q", dec.Action, tc.want)
			}
			if dec.MatchArgument != tc.arg {
				t.Fatalf("MatchArgument = %q, want %q", dec.MatchArgument, tc.arg)
			}
		})
	}
}

// TestWorktreeExitActionDenyKeepsToolVisible documents that an action-scoped
// deny is not a tool-level disable: the tool stays registered (IsDisabled
// looks only at whole-tool wildcard denials), so the model can still leave a
// worktree, and the enter description may keep pointing at exit.
func TestWorktreeExitActionDenyKeepsToolVisible(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	parent := newTestMainAgent(t, t.TempDir())
	parent.workDirState.store(WorkDirState{WorktreeID: "test-worktree"})
	parent.worktreeTools.Store(true)
	reg := tools.NewRegistry()
	reg.Register(tools.NewWorktreeEnterTool(parent))
	reg.Register(tools.NewWorktreeExitTool(parent))

	rs := permissionRuleset(t, `
"*": allow
worktree_exit: {remove: deny}
`)
	if rs.IsDisabled(tools.NameWorktreeExit) {
		t.Fatal("an action-scoped deny must not disable the whole tool")
	}
	visible := visibleLLMTools(reg, rs, func(string) bool { return false }, toolPermissionContext{})
	if !containsToolNamed(visible, tools.NameWorktreeExit) {
		t.Fatal("worktree_exit must stay on the tool surface under an action-scoped deny")
	}

	defs := llmToolDefinitionsFromVisibleTools(visible)
	desc := toolDefinitionDescription(t, defs, tools.NameWorktreeEnter)
	if !strings.Contains(desc, "until `"+tools.NameWorktreeExit+"`") {
		t.Fatalf("worktree_enter description = %q, want the exit reference while exit is visible", desc)
	}
}

// TestWorktreeEnterDescriptionDropsHiddenExit pins the other half of the
// description gate: once worktree_exit is not on the surface, the enter
// description must stop telling the model to use it.
func TestWorktreeEnterDescriptionDropsHiddenExit(t *testing.T) {
	hidden := llmToolDefinitionsFromVisibleTools([]tools.Tool{tools.NewWorktreeEnterTool(nil)})
	desc := toolDefinitionDescription(t, hidden, tools.NameWorktreeEnter)
	if strings.Contains(desc, tools.NameWorktreeExit) {
		t.Fatalf("worktree_enter description = %q, must not reference a hidden worktree_exit", desc)
	}
	if !strings.Contains(desc, "from then on") {
		t.Fatalf("worktree_enter description = %q, want the duration-only wording", desc)
	}

	shown := llmToolDefinitionsFromVisibleTools([]tools.Tool{
		tools.NewWorktreeEnterTool(nil),
		tools.NewWorktreeExitTool(nil),
	})
	desc = toolDefinitionDescription(t, shown, tools.NameWorktreeEnter)
	if !strings.Contains(desc, "until `"+tools.NameWorktreeExit+"`") {
		t.Fatalf("worktree_enter description = %q, want the exit reference while exit is visible", desc)
	}
}

func toolDefinitionDescription(t *testing.T, defs []message.ToolDefinition, name string) string {
	t.Helper()
	for _, def := range defs {
		if def.Name == name {
			return def.Description
		}
	}
	t.Fatalf("tool definition %q not found in %d definitions", name, len(defs))
	return ""
}
