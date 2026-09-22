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
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tc.name, json.RawMessage(tc.args), nil, permission.PathScope{})
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
		`{"command":"pwd && ls"}`,
		`{"command":"git log | head -20"}`,
	}
	for _, args := range allowed {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameShell, json.RawMessage(args), nil, permission.PathScope{})
		if !decision.Allowed {
			t.Fatalf("Shell args %s rejected: %s", args, decision.Reason)
		}
	}

	rejected := []string{
		`{"command":"go test ./..."}`,
		`{"command":"git checkout main"}`,
		`{"command":"pwd && rm -rf x"}`,
		`{"command":"cat README.md > /tmp/out"}`,
		`{"command":"echo $(pwd)"}`,
		`{"command":"rm README.md"}`,
		`{"command":"ls internal","run_in_background":true}`,
	}
	for _, args := range rejected {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameShell, json.RawMessage(args), nil, permission.PathScope{})
		if decision.Allowed {
			t.Fatalf("Shell args %s allowed, want reject", args)
		}
	}
}

func TestSpeculativeExecutionPolicyRejectsJobOutput(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.JobOutputTool{})
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameJobOutput, json.RawMessage(`{"job_id":"job-1"}`), nil, permission.PathScope{})
	if decision.Allowed {
		t.Fatal("job_output allowed for speculative execution, want reject")
	}
	if decision.Reason != "consumes_job_output" {
		t.Fatalf("reason = %q, want consumes_job_output", decision.Reason)
	}
}

func TestSpeculativeExecutionPolicyRejectsMutationTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	registry.Register(tools.ApplyPatchTool{})
	registry.Register(tools.DeleteTool{})

	cases := []struct {
		name string
		args string
	}{
		{tools.NameWrite, `{"path":"x.txt","content":"x"}`},
		{tools.NameApplyPatch, `{"patch":"*** Begin Patch\n*** Update File: x.txt\n@@\n-old\n+new\n*** End Patch"}`},
		{tools.NameDelete, `{"paths":["x.txt"],"reason":"cleanup"}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tc.name, json.RawMessage(tc.args), nil, permission.PathScope{})
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

	cases := []struct {
		name string
		args string
	}{
		{tools.NameShell, `{"command":"go test ./..."}`},
		{tools.NameQuestion, `{"questions":[{"header":"H","question":"Q?"}]}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tc.name, json.RawMessage(tc.args), nil, permission.PathScope{})
		if decision.Allowed {
			t.Fatalf("%s allowed for speculative execution, want reject", tc.name)
		}
	}
}

func TestSpeculativeExecutionPolicyAllowsTodoWriteCommitOnPromote(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewTodoWriteTool(nil))
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameTodoWrite, json.RawMessage(`{"todos":[{"id":"1","content":"Plan","status":"pending"}]}`), nil, permission.PathScope{})
	if !decision.Allowed {
		t.Fatalf("TodoWrite rejected for speculative preview: %s", decision.Reason)
	}
	if decision.Reason != "commit_on_promote_internal_state" {
		t.Fatalf("reason = %q, want commit_on_promote_internal_state", decision.Reason)
	}
}

func TestSpeculativeExecutionPolicyRejectsAskPermission(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	ruleset := permission.Ruleset{{Permission: tools.NameRead, Pattern: "README.md", Action: permission.ActionAsk}}
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, ruleset, tools.NameRead, json.RawMessage(`{"path":"README.md"}`), nil, permission.PathScope{})
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
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, ruleset, tools.NameShell, json.RawMessage(`{"command":"git status --short"}`), prior, permission.PathScope{})
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
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameRead, json.RawMessage(`{"path":"x.txt"}`), prior, permission.PathScope{})
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
	decision := evaluateSpeculativeExecutionPolicyWithPrefix(registry, nil, tools.NameGrep, json.RawMessage(`{"pattern":"TODO","paths":["internal"]}`), prior, permission.PathScope{})
	if !decision.Allowed {
		t.Fatalf("Grep behind prior read-only call rejected: %s", decision.Reason)
	}
}
