package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type dummyTool struct{ name string }

func (d dummyTool) Name() string        { return d.name }
func (d dummyTool) Description() string { return "dummy tool" }
func (d dummyTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
	}
}
func (d dummyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "ok", nil
}
func (d dummyTool) IsReadOnly() bool { return true }

func newMixedBatchTestSubAgent(t *testing.T) (*MainAgent, *SubAgent) {
	t.Helper()
	parent := newTestMainAgent(t, t.TempDir())
	reg := tools.NewRegistry()
	reg.Register(dummyTool{name: "Dummy"})
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "worker",
		TaskDesc:     "do work",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recovery,
		Parent:       parent,
		ParentCtx:    parent.parentCtx,
		Cancel:       func() {},
		BaseTools:    reg,
		WorkDir:      t.TempDir(),
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	sub.turn = &Turn{ID: 1, Epoch: 1, Ctx: context.Background()}
	return parent, sub
}

func mustJSONToolCall(t *testing.T, id, name string, args any) messageToolCall {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return messageToolCall{ID: id, Name: name, Args: raw}
}

type messageToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

func convertCalls(calls []messageToolCall) []message.ToolCall {
	out := make([]message.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = message.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args}
	}
	return out
}

func TestSubAgentRejectsCompleteAndEscalateInSameBatch(t *testing.T) {
	_, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{
			ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "Complete", map[string]any{"summary": "done"}),
				mustJSONToolCall(t, "call-2", "Escalate", map[string]any{"reason": "need help"}),
			}),
		},
	})
	if sub.pendingComplete != nil {
		t.Fatal("pendingComplete should remain nil for invalid control mix")
	}
	if sub.pendingEscalate != "" {
		t.Fatal("pendingEscalate should remain empty for invalid control mix")
	}
}

func TestSubAgentCompleteToolResultUsesRawSummary(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{
			ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "Complete", map[string]any{"summary": "Status: success\nFiles modified: []"}),
			}),
		},
	})

	evt := <-parent.eventCh
	if evt.Type != EventAgentDone {
		t.Fatalf("event.Type = %q, want %q", evt.Type, EventAgentDone)
	}
	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}
	got := msgs[len(msgs)-1]
	if got.Role != "tool" {
		t.Fatalf("last message role = %q, want tool", got.Role)
	}
	if got.Content != "Status: success\nFiles modified: []" {
		t.Fatalf("tool result = %q, want raw summary", got.Content)
	}
}

func TestSubAgentDefersEscalateUntilRegularToolsComplete(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{
			ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "Escalate", map[string]any{"reason": "need help"}),
				mustJSONToolCall(t, "call-2", "Dummy", map[string]any{"value": "x"}),
			}),
		},
	})
	if sub.pendingEscalate != "need help" {
		t.Fatalf("pendingEscalate = %q, want %q", sub.pendingEscalate, "need help")
	}
	if sub.State() == SubAgentStateWaitingPrimary {
		t.Fatal("worker entered waiting_primary before regular tool batch completed")
	}
	sub.handleToolResult(&toolResult{
		CallID:   "call-2",
		Name:     "Dummy",
		ArgsJSON: `{"value":"x"}`,
		Result:   "ok",
		TurnID:   1,
	})
	evt := <-parent.eventCh
	if evt.Type != EventEscalate {
		t.Fatalf("event.Type = %q, want %q", evt.Type, EventEscalate)
	}
}

func TestSubAgentEmitsExecutingActivityWhenRegularToolsStart(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{
			ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "Dummy", map[string]any{"value": "x"}),
			}),
		},
	})

	deadline := time.After(time.Second)
	for {
		select {
		case evt := <-parent.Events():
			act, ok := evt.(AgentActivityEvent)
			if !ok {
				continue
			}
			if act.AgentID == sub.instanceID && act.Type == ActivityExecuting {
				return
			}
		case <-deadline:
			t.Fatal("expected SubAgent executing activity event before tool result completion")
		}
	}
}

func TestSubAgentCompleteWithOutstandingJoinChildEntersWaitingDescendant(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.taskID = "adhoc-parent"
	sub.instanceID = "worker-parent"
	sub.semHeld = true
	parent.sem <- struct{}{}
	parent.mu.Lock()
	delete(parent.subAgents, "worker-1")
	parent.subAgents[sub.instanceID] = sub
	parent.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID:           sub.taskID,
		LatestInstanceID: sub.instanceID,
		State:            string(SubAgentStateRunning),
	}
	parent.taskRecords["adhoc-child"] = &DurableTaskRecord{
		TaskID:           "adhoc-child",
		OwnerAgentID:     sub.instanceID,
		OwnerTaskID:      sub.taskID,
		JoinToOwner:      true,
		State:            string(SubAgentStateRunning),
		LatestInstanceID: "worker-child",
	}
	parent.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{
			ToolCalls: convertCalls([]messageToolCall{
				mustJSONToolCall(t, "call-1", "Complete", map[string]any{"summary": "final summary"}),
			}),
		},
	})

	if sub.State() != SubAgentStateWaitingDescendant {
		t.Fatalf("sub.State() = %q, want %q", sub.State(), SubAgentStateWaitingDescendant)
	}
	if sub.semHeld {
		t.Fatal("waiting_descendant worker should release semaphore slot")
	}
	pending := sub.PendingCompleteIntent()
	if pending == nil || pending.Summary != "final summary" {
		t.Fatalf("PendingCompleteIntent = %#v, want summary %q", pending, "final summary")
	}
	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "tool" {
		t.Fatalf("last message = %+v, want Complete tool result", msgs)
	}
	if got := msgs[len(msgs)-1].Content; got != deferredCompleteResult(1) {
		t.Fatalf("deferred Complete tool result = %q, want %q", got, deferredCompleteResult(1))
	}
}
