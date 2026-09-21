package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestEscalateParametersRequireKindAndReason(t *testing.T) {
	params := EscalateTool{}.Parameters()
	required, ok := params["required"].([]string)
	if !ok || !slices.Equal(required, []string{"kind", "reason"}) {
		t.Fatalf("required = %#v, want exactly [kind reason]", params["required"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", params["properties"])
	}
	kind, ok := properties["kind"].(map[string]any)
	if !ok {
		t.Fatalf("kind = %#v", properties["kind"])
	}
	enum, ok := kind["enum"].([]string)
	if !ok || !slices.Equal(enum, []string{EscalateKindNeedsRepair, EscalateKindBlocked}) {
		t.Fatalf("kind enum = %#v, want [needs_repair blocked]", kind["enum"])
	}
}

func TestEscalateExecuteRejectsInvalidKindOrReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "missing kind", args: `{"reason":"need help"}`},
		{name: "unknown kind", args: `{"kind":"maybe","reason":"need help"}`},
		{name: "missing reason", args: `{"kind":"needs_repair"}`},
		{name: "blank reason", args: `{"kind":"blocked","reason":"   "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := &recordingEventSender{ch: make(chan any, 1)}
			if _, err := NewEscalateTool(sender).Execute(context.Background(), json.RawMessage(tc.args)); err == nil {
				t.Fatal("Execute accepted an invalid escalation")
			}
			if sender.eventType != "" {
				t.Fatalf("event type = %q, want nothing sent", sender.eventType)
			}
		})
	}
}

func TestEscalateExecuteSendsBothKinds(t *testing.T) {
	for _, kind := range []string{EscalateKindNeedsRepair, EscalateKindBlocked} {
		t.Run(kind, func(t *testing.T) {
			sender := &recordingEventSender{ch: make(chan any, 1)}
			ctx := WithAgentID(context.Background(), "worker-1")
			result, err := NewEscalateTool(sender).Execute(ctx, json.RawMessage(`{"kind":"`+kind+`","reason":"need a decision"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if result == "" {
				t.Fatal("expected a tool result")
			}
			if sender.eventType != EventEscalate || sender.sourceID != "worker-1" {
				t.Fatalf("event=%q source=%q, want %q from worker-1", sender.eventType, sender.sourceID, EventEscalate)
			}
			payload, ok := (<-sender.ch).(AgentRequestPayload)
			if !ok || payload.Kind != kind || strings.TrimSpace(payload.Reason) != "need a decision" {
				t.Fatalf("payload = %#v, want kind %q", payload, kind)
			}
		})
	}
}
