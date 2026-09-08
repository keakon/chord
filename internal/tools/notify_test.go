package tools

import (
	"context"
	"encoding/json"
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
			for _, field := range []string{"target_task_id", "grant_write_scope"} {
				if _, exposed := properties[field]; exposed != tc.target {
					t.Fatalf("%s exposed = %t, want %t", field, exposed, tc.target)
				}
			}
			if got := properties["message_type"].(map[string]any)["enum"].([]string); !slices.Equal(got, tc.messageType) {
				t.Fatalf("message types = %v, want %v", got, tc.messageType)
			}
			// The clause forbidding a scope grant on a structured reply is
			// only carried by roles that can actually grant scope. Emitting it
			// for an owner-only role would ship a keyword some providers
			// cannot represent in exchange for a constraint that names a
			// property the role's schema does not even declare.
			if _, restricted := params["not"]; restricted != tc.target {
				t.Fatalf("grant/response exclusion present = %t, want %t", restricted, tc.target)
			}
		})
	}
}

type recordingNotifyMessenger struct {
	notifyMessengerStub
	responses int
	grants    int
}

func (m *recordingNotifyMessenger) NotifySubAgentMessage(context.Context, AgentResponseRequest) (TaskHandle, error) {
	m.responses++
	return TaskHandle{Status: "delivered"}, nil
}

func (m *recordingNotifyMessenger) NotifySubAgentWithScopeGrant(context.Context, string, string, string, WriteScope) (TaskHandle, error) {
	m.grants++
	return TaskHandle{Status: "delivered"}, nil
}

func TestNotifyRejectsInvalidGrantsBeforeDelivery(t *testing.T) {
	for _, raw := range []string{
		`{"message":"Continue","grant_write_scope":{"files":["sample.go"]}}`,
		`{"target_task_id":"task-a","message":"Continue","message_type":"response","correlation_id":"corr-1","grant_write_scope":{"files":["sample.go"]}}`,
		`{"target_task_id":"task-a","message":"Continue","grant_write_scope":{}}`,
		`{"target_task_id":"task-a","message":"Continue","grant_write_scope":{"files":["sample.go"],"read_only":true}}`,
		`{"target_task_id":"task-a","message":"Continue","grant_write_scope":{"files":["sample.go"],"verification_commands":["go test ./sample"]}}`,
	} {
		messenger := &recordingNotifyMessenger{}
		tool := NewNotifyTool(nil, messenger, true, true)
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid grant accepted: %s", raw)
		}
		if messenger.responses != 0 || messenger.grants != 0 {
			t.Fatalf("rejected call had side effects: %#v", messenger)
		}
	}
}

func TestNotifyScopeGrantAndResponseCapabilities(t *testing.T) {
	messenger := &recordingNotifyMessenger{}
	tool := NewNotifyTool(nil, messenger, true, false)
	response := json.RawMessage(`{"target_task_id":"task-a","message":"Continue","message_type":"response","correlation_id":"corr-1"}`)
	if _, err := tool.Execute(context.Background(), response); err == nil || !strings.Contains(err.Error(), "not available in this role") {
		t.Fatalf("owner-only role accepted a targeted response: %v", err)
	}
	if messenger.responses != 0 {
		t.Fatal("targeted response was delivered without role capability")
	}
	tool = NewNotifyTool(nil, messenger, false, true)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"target_task_id":"task-a","message":"Update the sample","grant_write_scope":{"files":["sample.go"]}}`)); err != nil {
		t.Fatal(err)
	}
	if messenger.grants != 1 {
		t.Fatal("plain targeted grant was not delivered")
	}
}
