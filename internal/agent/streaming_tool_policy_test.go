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
		{tools.NameGrep, `{"pattern":"TODO","path":"internal"}`},
		{tools.NameGlob, `{"pattern":"**/*.go","path":"internal"}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicy(registry, nil, tc.name, json.RawMessage(tc.args))
		if !decision.Allowed {
			t.Fatalf("%s rejected: %s", tc.name, decision.Reason)
		}
	}
}

func TestSpeculativeExecutionPolicyBashReadOnlySubset(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewBashTool("bash"))

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
		decision := evaluateSpeculativeExecutionPolicy(registry, nil, tools.NameBash, json.RawMessage(args))
		if !decision.Allowed {
			t.Fatalf("Bash args %s rejected: %s", args, decision.Reason)
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
		decision := evaluateSpeculativeExecutionPolicy(registry, nil, tools.NameBash, json.RawMessage(args))
		if decision.Allowed {
			t.Fatalf("Bash args %s allowed, want reject", args)
		}
	}
}

func TestSpeculativeExecutionPolicyAllowsRollbackFileMutationTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool{})
	registry.Register(tools.EditTool{})
	registry.Register(tools.DeleteTool{})

	cases := []struct {
		name string
		args string
	}{
		{tools.NameWrite, `{"path":"x.txt","content":"x"}`},
		{tools.NameEdit, `{"path":"x.txt","old_string":"x","new_string":"y"}`},
		{tools.NameDelete, `{"paths":["x.txt"],"reason":"cleanup"}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicy(registry, nil, tc.name, json.RawMessage(tc.args))
		if !decision.Allowed {
			t.Fatalf("%s rejected for speculative execution: %s", tc.name, decision.Reason)
		}
	}
}

func TestSpeculativeExecutionPolicyRejectsHighRiskNonRollbackTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewBashTool("bash"))
	registry.Register(tools.NewQuestionTool(nil))
	registry.Register(tools.NewTodoWriteTool(nil))

	cases := []struct {
		name string
		args string
	}{
		{tools.NameBash, `{"command":"go test ./..."}`},
		{tools.NameQuestion, `{"questions":[{"header":"H","question":"Q?"}]}`},
		{tools.NameTodoWrite, `{"todos":[]}`},
	}
	for _, tc := range cases {
		decision := evaluateSpeculativeExecutionPolicy(registry, nil, tc.name, json.RawMessage(tc.args))
		if decision.Allowed {
			t.Fatalf("%s allowed for speculative execution, want reject", tc.name)
		}
	}
}

func TestSpeculativeExecutionPolicyRejectsAskPermission(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	ruleset := permission.Ruleset{{Permission: tools.NameRead, Pattern: "README.md", Action: permission.ActionAsk}}
	decision := evaluateSpeculativeExecutionPolicy(registry, ruleset, tools.NameRead, json.RawMessage(`{"path":"README.md"}`))
	if decision.Allowed {
		t.Fatal("Read with ask permission allowed for speculative execution, want reject")
	}
	if decision.Reason != "permission_ask" {
		t.Fatalf("reason = %q, want permission_ask", decision.Reason)
	}
}
