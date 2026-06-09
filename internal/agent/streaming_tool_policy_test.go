package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestSpeculativeExecutionPolicyAllowsSafeReadOnlyTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	registry.Register(tools.GlobTool{})

	cases := []struct {
		name string
		args string
	}{
		{tools.NameRead, `{"path":"README.md"}`},
		{tools.NameGrep, `{"pattern":"TODO","paths":["internal"]}`},
		{tools.NameGlob, `{"patterns":["**/*.go"],"path":"internal"}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tc.name, json.RawMessage(tc.args), nil)
		if !decision.Allowed {
			t.Fatalf("%s rejected: %s", tc.name, decision.Reason)
		}
	}
}

func TestSpeculativeExecutionPolicyBashReadOnlySubset(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))

	allowed := []string{
		`{"command":"pwd"}`,
		`{"command":"ls internal"}`,
		`{"command":"cat README.md"}`,
		`{"command":"which go"}`,
		`{"command":"git status --short"}`,
		`{"command":"git log --oneline -3"}`,
		`{"command":"git diff HEAD"}`,
		`{"command":"git show HEAD"}`,
		`{"command":"git branch --show-current"}`,
		`{"command":"git rev-parse HEAD"}`,
	}
	for _, args := range allowed {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameShell, json.RawMessage(args), nil)
		if !decision.Allowed {
			t.Fatalf("Shell args %s rejected: %s", args, decision.Reason)
		}
	}

	rejected := []string{
		`{"command":"go test ./..."}`,
		`{"command":"git checkout main"}`,
		`{"command":"pwd && ls"}`,
		`{"command":"cat README.md > /tmp/out"}`,
		`{"command":"echo $(pwd)"}`,
		`{"command":"rm README.md"}`,
	}
	for _, args := range rejected {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameShell, json.RawMessage(args), nil)
		if decision.Allowed {
			t.Fatalf("Shell args %s allowed, want reject", args)
		}
	}
}

func TestSpeculativeExecutionPolicyRejectsMutationTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	registry.Register(tools.EditTool{})
	registry.Register(tools.DeleteTool{})

	cases := []struct {
		name string
		args string
	}{
		{tools.NameWrite, `{"path":"x.txt","content":"x"}`},
		{tools.NameEdit, `{"path":"x.txt","patch":"@@\n-old\n+new\n"}`},
		{tools.NameDelete, `{"paths":["x.txt"],"reason":"cleanup"}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tc.name, json.RawMessage(tc.args), nil)
		if decision.Allowed {
			t.Fatalf("%s allowed for speculative execution, want reject", tc.name)
		}
		if decision.Reason != "mutation_tool" {
			t.Fatalf("%s reject reason = %q, want mutation_tool", tc.name, decision.Reason)
		}
	}
}

func TestSpeculativeExecutionPolicyRejectsHighRiskNonRollbackTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	registry.Register(tools.NewQuestionTool(nil))
	registry.Register(tools.NewTodoWriteTool(nil))

	cases := []struct {
		name string
		args string
	}{
		{tools.NameShell, `{"command":"go test ./..."}`},
		{tools.NameQuestion, `{"questions":[{"header":"H","question":"Q?"}]}`},
		{tools.NameTodoWrite, `{"todos":[]}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tc.name, json.RawMessage(tc.args), nil)
		if decision.Allowed {
			t.Fatalf("%s allowed for speculative execution, want reject", tc.name)
		}
	}
}

func TestSpeculativeExecutionPolicyRejectsAskPermission(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	ruleset := permission.Ruleset{{Permission: tools.NameRead, Pattern: "README.md", Action: permission.ActionAsk}}
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, ruleset, tools.NameRead, json.RawMessage(`{"path":"README.md"}`), nil)
	if decision.Allowed {
		t.Fatal("Read with ask permission allowed for speculative execution, want reject")
	}
	if decision.Reason != "permission_ask" {
		t.Fatalf("reason = %q, want permission_ask", decision.Reason)
	}
}

func TestSpeculativeExecutionPolicyRejectsReadOnlyWhenPriorCallNeedsApproval(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewShellTool("bash"))
	ruleset := permission.Ruleset{{Permission: tools.NameShell, Pattern: "git commit *", Action: permission.ActionAsk}, {Permission: tools.NameShell, Pattern: "git status *", Action: permission.ActionAllow}}
	prior := []PendingToolCall{{CallID: "call-1", Name: tools.NameShell, ArgsJSON: `{"command":"git commit -m fix"}`}}
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, ruleset, tools.NameShell, json.RawMessage(`{"command":"git status --short"}`), prior)
	if decision.Allowed {
		t.Fatal("git status allowed for speculative execution behind prior ask-gated commit, want reject")
	}
	if decision.Reason != "prior_pending_non_read_only:shell" {
		t.Fatalf("reason = %q, want prior_pending_non_read_only:shell", decision.Reason)
	}
}

func TestSpeculativeExecutionPolicyRejectsReadOnlyWhenPriorCallIsMutating(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.WriteTool{})
	prior := []PendingToolCall{{CallID: "call-1", Name: tools.NameWrite, ArgsJSON: `{"path":"x.txt","content":"x"}`}}
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameRead, json.RawMessage(`{"path":"x.txt"}`), prior)
	if decision.Allowed {
		t.Fatal("Read allowed for speculative execution behind prior mutating tool, want reject")
	}
	if decision.Reason != "prior_pending_non_read_only:write" {
		t.Fatalf("reason = %q, want prior_pending_non_read_only:write", decision.Reason)
	}
}

func TestSpeculativeExecutionPolicyAllowsReadOnlyPrefix(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.GrepTool{})
	prior := []PendingToolCall{{CallID: "call-1", Name: tools.NameRead, ArgsJSON: `{"path":"README.md"}`}}
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameGrep, json.RawMessage(`{"pattern":"TODO","paths":["internal"]}`), prior)
	if !decision.Allowed {
		t.Fatalf("Grep behind prior read-only call rejected: %s", decision.Reason)
	}
}
