package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNotifyOwnerEmitsStructuredPayload(t *testing.T) {
	sender := &recordingEventSender{ch: make(chan any, 1)}
	tool := NewNotifyTool(sender, nil, true, false)
	ctx := WithTaskID(WithAgentID(context.Background(), "reviewer-1"), "adhoc-1")
	result, err := tool.Execute(ctx, json.RawMessage(`{"message":"Tests pass","kind":"progress"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == "" {
		t.Fatal("expected tool result")
	}
	payload := (<-sender.ch).(AgentNotifyPayload)
	if sender.eventType != "agent_notify" || sender.sourceID != "reviewer-1" || payload.Message != "Tests pass" || payload.Kind != "progress" {
		t.Fatalf("event=%q source=%q payload=%#v", sender.eventType, sender.sourceID, payload)
	}
}
