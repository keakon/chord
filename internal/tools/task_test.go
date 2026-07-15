package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	result, err := NewDelegateTool(creator).Execute(context.Background(), json.RawMessage(`{"description":"implement feature","agent_type":"builder"}`))
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

func TestDelegateToolDescriptionIncludesWriteScopeParallelismGuard(t *testing.T) {
	desc := NewDelegateTool(taskTestCreator{}).Description()
	for _, want := range []string{
		"Only parallelize tasks when their write scopes are clearly independent",
		"do not create concurrent workers that may edit the same file or tightly coupled targets",
		"reuse it with Notify or Cancel instead of spawning a duplicate delegate for follow-up",
		"Use Notify(existing) for the same task's follow-up",
		"use Delegate(new) for a genuinely new, independently trackable task",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q in %q", want, desc)
		}
	}
}
