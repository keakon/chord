package tools

import (
	"context"
	"encoding/json"
	"strings"
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

func TestNotifyOwnerEmitsStructuredNotice(t *testing.T) {
	sender := &recordingEventSender{ch: make(chan any, 1)}
	tool := NewNotifyTool(sender, nil, true, false)
	ctx := WithAgentID(context.Background(), "reviewer-1")
	_, err := tool.Execute(ctx, json.RawMessage(`{"message":"contract changed","message_type":"notice","subtype":"api_contract","correlation_id":"corr-1","payload":{"version":2}}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	payload := (<-sender.ch).(AgentNotifyPayload)
	if payload.MessageType != "notice" || payload.Subtype != "api_contract" || payload.CorrelationID != "corr-1" || string(payload.Payload) != `{"version":2}` {
		t.Fatalf("payload = %#v", payload)
	}
}

type notifyMessengerStub struct{}

func (notifyMessengerStub) NotifySubAgent(context.Context, string, string, string) (TaskHandle, error) {
	return TaskHandle{}, nil
}

func TestNotifyRejectsStructuredTargetedDelivery(t *testing.T) {
	tool := NewNotifyTool(nil, notifyMessengerStub{}, false, true)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"continue","message_type":"notice"}`))
	if err == nil || !strings.Contains(err.Error(), "durable owner-to-child") {
		t.Fatalf("error = %v", err)
	}
}
