package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type taskTestCreator struct{}

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
