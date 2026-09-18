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
	return []AgentInfo{{Name: "builder", Description: "General coding"}}
}

func TestDelegateToolParametersExposeIdentityAndScopeMetadata(t *testing.T) {
	params := NewDelegateTool(taskTestCreator{}).Parameters()
	text := fmt.Sprint(params)
	for _, want := range []string{
		"plan_task_ref",
		"semantic_task_key",
		"expected_write_scope",
		"non_empty_scope=required",
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
			for _, want := range []string{"files/path_prefix/modules", "no file-writing tools"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Execute() error = %q, want it to mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "Available read-only agent types") {
				t.Fatalf("Execute() error = %q, want no read-only list appended when no available agent type is read-only", err)
			}
			if creator.calls != 0 {
				t.Fatalf("CreateSubAgent() calls = %d, want the delegation rejected before admission", creator.calls)
			}
		})
	}
}

// noFileWriteToolsCreator is a SubAgentCreator whose target role registers no
// file-modifying tools (write/edit/delete/apply_patch denied).
type noFileWriteToolsCreator struct {
	countingTaskCreator
}

func (*noFileWriteToolsCreator) AgentRoleRegistersNoFileWriteTools(agentType string) bool {
	return true
}

// A role whose ruleset registers no file-modifying tools cannot write files,
// so an empty expected_write_scope is a valid "nothing to declare" instead of
// an undeclared writing task. Roles that can write files (the default for
// creators without AgentFileWriteSurface) must still declare paths.
func TestDelegateToolAcceptsEmptyScopeForNoFileWriteToolRole(t *testing.T) {
	for _, scope := range []string{`{}`, `{"files":[]}`} {
		creator := &noFileWriteToolsCreator{}
		args := `{"description":"survey the parser","agent_type":"builder","expected_write_scope":` + scope + `}`
		if _, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(args)); err != nil {
			t.Fatalf("Execute() error = %v, want empty scope accepted for a no-file-write-tool role", err)
		}
		if creator.calls != 1 {
			t.Fatalf("CreateSubAgent() calls = %d, want 1", creator.calls)
		}
	}
}

func TestDelegateToolEmptyScopeNeverBypassesRequiredField(t *testing.T) {
	creator := &noFileWriteToolsCreator{}
	if _, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(
		`{"description":"survey the parser","agent_type":"builder"}`,
	)); err == nil {
		t.Fatal("Execute() error = nil, want the omitted expected_write_scope field rejected even for a no-file-write-tool role")
	}
	if creator.calls != 0 {
		t.Fatalf("CreateSubAgent() calls = %d, want 0", creator.calls)
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
		"task_id is the stable durable handle",
		"delegation workflow governs task selection, follow-up, and safe parallelism",
		"Roles that can write files must declare a non-empty expected_write_scope",
		"a read-only delegation pairs a read-only role with an empty scope object {}",
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
		"Prefer using Read",
		"with Notify or Cancel",
	} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("Description() should not duplicate workflow-block strategy %q in %q", unwanted, desc)
		}
	}
}

// mixedRolesCreator reports two agents with different file-write surfaces: a
// write-capable "builder" role and a "surveyor" role that registers no
// file-writing tools, so the empty-scope rule can be exercised per agent type
// instead of per creator.
type mixedRolesCreator struct {
	countingTaskCreator
}

func (*mixedRolesCreator) AgentRoleRegistersNoFileWriteTools(agentType string) bool {
	return agentType == "surveyor"
}

func (*mixedRolesCreator) AvailableSubAgents() []AgentInfo {
	return []AgentInfo{
		{Name: "builder", Description: "General coding"},
		{Name: "surveyor", Description: "Read-only analysis"},
	}
}

// scopeRuleTokens extracts the bracketed meta tokens of a rendered agent_type
// row so scope-rule assertions compare exact tokens instead of substrings.
func scopeRuleTokens(row string) []string {
	start := strings.Index(row, "[")
	end := strings.Index(row, "]")
	if start < 0 || end <= start {
		return nil
	}
	return strings.Split(row[start+1:end], "; ")
}

// TestDelegateToolParametersAnnotateAgentRowsWithEmptyScopeRule verifies each
// rendered agent_type row carries the empty-scope rule of its role:
// empty_scope=allowed for a role that registers no file-writing tools, and
// non_empty_scope=required for a role that can write files (which the runtime
// rejects an empty scope for).
func TestDelegateToolParametersAnnotateAgentRowsWithEmptyScopeRule(t *testing.T) {
	params := NewDelegateTool(&mixedRolesCreator{}).Parameters()
	text := fmt.Sprint(params)
	rows := make(map[string]string, 2)
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "- builder") {
			rows["builder"] = line
		}
		if strings.HasPrefix(line, "- surveyor") {
			rows["surveyor"] = line
		}
	}
	if got := scopeRuleTokens(rows["builder"]); !slices.Equal(got, []string{"non_empty_scope=required"}) {
		t.Fatalf("builder row %q: scope rule tokens = %v, want [non_empty_scope=required], text: %s", rows["builder"], got, text)
	}
	if got := scopeRuleTokens(rows["surveyor"]); !slices.Equal(got, []string{"empty_scope=allowed"}) {
		t.Fatalf("surveyor row %q: scope rule tokens = %v, want [empty_scope=allowed], text: %s", rows["surveyor"], got, text)
	}
}

// TestDelegateToolAcceptsEmptyScopeForReadOnlyAgentType verifies an empty
// expected_write_scope stays accepted when agent_type names the read-only role
// of a creator that also exposes write-capable roles.
func TestDelegateToolAcceptsEmptyScopeForReadOnlyAgentType(t *testing.T) {
	creator := &mixedRolesCreator{}
	if _, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(
		`{"description":"survey the parser","agent_type":"surveyor","expected_write_scope":{}}`,
	)); err != nil {
		t.Fatalf("Execute() error = %v, want empty scope accepted for the read-only agent type", err)
	}
	if creator.calls != 1 {
		t.Fatalf("CreateSubAgent() calls = %d, want 1", creator.calls)
	}
}

// TestDelegateToolEmptyScopeForWriteCapableAgentTypeNamesReadOnlyAlternatives
// verifies the repair instruction for an empty scope on a write-capable role
// lists the read-only agent types that accept an empty scope, appended after
// the original guidance.
func TestDelegateToolEmptyScopeForWriteCapableAgentTypeNamesReadOnlyAlternatives(t *testing.T) {
	creator := &mixedRolesCreator{}
	_, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(
		`{"description":"implement feature","agent_type":"builder","expected_write_scope":{}}`,
	))
	if err == nil {
		t.Fatal("Execute() error = nil, want the write-capable builder role to require a non-empty scope")
	}
	for _, want := range []string{
		"files/path_prefix/modules",
		"no file-writing tools",
		"Available read-only agent types: surveyor",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Execute() error = %q, want it to contain %q", err, want)
		}
	}
	if creator.calls != 0 {
		t.Fatalf("CreateSubAgent() calls = %d, want the delegation rejected before admission", creator.calls)
	}
}
