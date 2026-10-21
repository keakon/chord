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
	calls       int
	lastRequest SubAgentRequest
}

func (c *countingTaskCreator) CreateSubAgent(_ context.Context, req SubAgentRequest) (TaskHandle, error) {
	c.calls++
	c.lastRequest = req
	return TaskHandle{Status: "started", TaskID: "adhoc-7", AgentID: "agent-7", Message: "running in background"}, nil
}

func (*countingTaskCreator) AvailableSubAgents() []AgentInfo {
	return []AgentInfo{{Name: "builder"}}
}

func (taskTestCreator) CreateSubAgent(_ context.Context, req SubAgentRequest) (TaskHandle, error) {
	return TaskHandle{Status: "started", TaskID: "adhoc-1", AgentID: "agent-1", Message: req.Description + ":" + req.AgentType, PlanTaskRef: req.PlanTaskRef, SemanticTaskKey: req.SemanticTaskKey}, nil
}

func (taskTestCreator) AvailableSubAgents() []AgentInfo {
	return []AgentInfo{{Name: "builder", Description: "General coding"}}
}

func TestDelegateToolParametersExposeIdentityMetadata(t *testing.T) {
	params := NewDelegateTool(taskTestCreator{}).Parameters()
	text := fmt.Sprint(params)
	for _, want := range []string{
		"plan_task_ref",
		"semantic_task_key",
		"result_schema",
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

func TestDelegateToolDescriptionKeepsUsageSemantics(t *testing.T) {
	desc := NewDelegateTool(taskTestCreator{}).Description()
	for _, want := range []string{
		"delivered asynchronously and flows back to you automatically",
		"task_id is the stable durable handle",
		"delegation workflow governs task selection, follow-up, and safe parallelism",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q in %q", want, desc)
		}
	}
	// Orchestration strategy (Notify-vs-new-delegate, parallelism) lives in the
	// SubAgent Workflow prompt block, not the tool description.
	for _, unwanted := range []string{
		"Spawn", // Delegate results flow back asynchronously; no background-process tool references belong here
		"Use Notify(existing) for the same task's follow-up",
		"Dispatch tasks in parallel only when their work is independent",
		"Prefer using Read",
		"with Notify or Cancel",
	} {
		if strings.Contains(desc, unwanted) {
			t.Fatalf("Description() should not mention %q in %q", unwanted, desc)
		}
	}
}
