package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

func (notifyMessengerStub) NotifySubAgentMessage(context.Context, AgentResponseRequest) (TaskHandle, error) {
	return TaskHandle{Status: "delivered"}, nil
}

func TestNotifyRejectsStructuredTargetedDelivery(t *testing.T) {
	tool := NewNotifyTool(nil, notifyMessengerStub{}, false, true)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"continue","message_type":"notice"}`))
	if err == nil || !strings.Contains(err.Error(), "structured replies require") {
		t.Fatalf("error = %v", err)
	}
}

func TestNotifyDeliversCorrelatedTargetedResponse(t *testing.T) {
	tool := NewNotifyTool(nil, notifyMessengerStub{}, false, true)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"continue","message_type":"response","correlation_id":"corr-1"}`))
	if err != nil || !strings.Contains(result, `"status":"delivered"`) {
		t.Fatalf("result=%q error=%v", result, err)
	}
}

func TestNotifyRejectsStructuredFieldsOnTargetedResponse(t *testing.T) {
	tool := NewNotifyTool(nil, notifyMessengerStub{}, false, true)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"continue","message_type":"response","correlation_id":"corr-1","payload":{"option":"a"}}`))
	if err == nil || !strings.Contains(err.Error(), "unavailable for response") {
		t.Fatalf("error = %v", err)
	}
}

func TestNotifyParametersMatchRoleCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name        string
		owner       bool
		target      bool
		messageType []string
	}{
		{name: "main", target: true, messageType: []string{"response"}},
		{name: "owner only", owner: true, messageType: []string{"progress", "notice"}},
		{name: "nested owner", owner: true, target: true, messageType: []string{"progress", "notice", "response"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := NewNotifyTool(nil, notifyMessengerStub{}, tc.owner, tc.target).Parameters()
			properties := params["properties"].(map[string]any)
			for _, field := range []string{"subtype", "payload"} {
				if _, exposed := properties[field]; exposed != tc.owner {
					t.Fatalf("%s exposed = %t, want %t", field, exposed, tc.owner)
				}
			}
			for _, field := range []string{"target_task_id"} {
				if _, exposed := properties[field]; exposed != tc.target {
					t.Fatalf("%s exposed = %t, want %t", field, exposed, tc.target)
				}
			}
			if got := properties["message_type"].(map[string]any)["enum"].([]string); !slices.Equal(got, tc.messageType) {
				t.Fatalf("message types = %v, want %v", got, tc.messageType)
			}
		})
	}
}

type recordingNotifyMessenger struct {
	responses int
	plain     int
	err       error
}

func (m *recordingNotifyMessenger) NotifySubAgent(context.Context, string, string, string) (TaskHandle, error) {
	m.plain++
	return TaskHandle{Status: "delivered"}, nil
}

func (m *recordingNotifyMessenger) NotifySubAgentMessage(context.Context, AgentResponseRequest) (TaskHandle, error) {
	if m.err != nil {
		return TaskHandle{}, m.err
	}
	m.responses++
	return TaskHandle{Status: "delivered"}, nil
}

func TestNotifyOwnerOnlyRoleRejectsTargetedResponse(t *testing.T) {
	messenger := &recordingNotifyMessenger{}
	tool := NewNotifyTool(nil, messenger, true, false)
	response := json.RawMessage(`{"target_task_id":"task-a","message":"Continue","message_type":"response","correlation_id":"corr-1"}`)
	if _, err := tool.Execute(context.Background(), response); err == nil || !strings.Contains(err.Error(), "not available in this role") {
		t.Fatalf("owner-only role accepted a targeted response: %v", err)
	}
	if messenger.responses != 0 {
		t.Fatal("targeted response was delivered without role capability")
	}
}

func TestNotifyTargetedParametersRequireExactlyMessageAndTargetTaskID(t *testing.T) {
	params := NewNotifyTool(nil, notifyMessengerStub{}, false, true).Parameters()
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("Parameters()[\"required\"] = %#v, want []string", params["required"])
	}
	if !slices.Equal(required, []string{"message", "target_task_id"}) {
		t.Fatalf("required = %v, want exactly [message target_task_id]", required)
	}
}

func TestNotifyDeliversPlainTargetedMessageWithoutMessageType(t *testing.T) {
	messenger := &recordingNotifyMessenger{}
	tool := NewNotifyTool(nil, messenger, false, true)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"Keep going"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, `"status":"delivered"`) {
		t.Fatalf("result = %q, want delivered handle", result)
	}
	if messenger.plain != 1 || messenger.responses != 0 {
		t.Fatalf("messenger calls: plain=%d responses=%d, want one plain delivery only", messenger.plain, messenger.responses)
	}
}

func TestNotifyRejectsResponseWithoutCorrelationID(t *testing.T) {
	messenger := &recordingNotifyMessenger{}
	tool := NewNotifyTool(nil, messenger, false, true)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"Answer","message_type":"response"}`))
	if err == nil || !strings.Contains(err.Error(), "correlation_id is required for response") {
		t.Fatalf("error = %v, want missing-correlation rejection", err)
	}
	if !strings.Contains(err.Error(), "without the message_type and correlation_id keys") {
		t.Fatalf("error = %v, want plain-notify recovery guidance naming the keys to remove", err)
	}
	if messenger.plain != 0 || messenger.responses != 0 {
		t.Fatalf("messenger calls: plain=%d responses=%d, want no delivery for a missing correlation_id", messenger.plain, messenger.responses)
	}
}

func TestNotifyRejectsUnknownCorrelationWithoutFallback(t *testing.T) {
	messenger := &recordingNotifyMessenger{err: errors.New(`unknown pending request "corr-guess" for task task-a`)}
	tool := NewNotifyTool(nil, messenger, false, true)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"Answer","message_type":"response","correlation_id":"corr-guess"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown pending request") {
		t.Fatalf("error = %v, want the unknown-request rejection to surface the original error", err)
	}
	if !strings.Contains(err.Error(), "never be invented, guessed, or reused") {
		t.Fatalf("error = %v, want correlation recovery guidance", err)
	}
	// A response with an unresolvable correlation must not silently degrade to
	// a plain targeted message: both messenger paths stay untouched.
	if messenger.plain != 0 || messenger.responses != 0 {
		t.Fatalf("messenger calls: plain=%d responses=%d, want no delivery or fallback", messenger.plain, messenger.responses)
	}
}
