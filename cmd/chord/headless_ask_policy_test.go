package main

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/jsonx"
)

func TestHeadlessToolAskPolicyDenied(t *testing.T) {
	for _, subscribed := range []bool{true, false} {
		state := &headlessState{subscriptions: map[string]bool{headlessToolAskPolicyDenied: subscribed}}
		filterHeadlessEvent(agent.ConfirmRequestEvent{RequestID: "req-existing", ToolName: "compact_context"}, state)
		pending := state.pendingConfirm
		event := agent.ToolAskPolicyDeniedEvent{CallID: "call-1", ToolName: "shell", AgentID: "worker-1", Policy: config.ConfirmAskPolicyDeny, Reason: "This is not user feedback."}
		envs := filterHeadlessEvent(event, state)
		env := findHeadlessEnvelope(envs, headlessToolAskPolicyDenied)
		if (env != nil) != subscribed {
			t.Fatalf("subscribed=%v envelopes=%v", subscribed, envs)
		}
		if state.pendingConfirm != pending {
			t.Fatal("policy event changed pending confirmation")
		}
		for _, kind := range []string{"confirm_request", "confirm_resolved", "notification"} {
			if findHeadlessEnvelope(envs, kind) != nil {
				t.Fatalf("policy event produced %s", kind)
			}
		}
		if !subscribed {
			continue
		}
		data, err := jsonx.Marshal(env.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var got agent.ToolAskPolicyDeniedEvent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got != event || !headlessEventTypes[headlessToolAskPolicyDenied] {
			t.Fatalf("payload=%+v", got)
		}
	}
}

func TestHeadlessToolAskPolicyDeniedDefaultSubscriptionAndMainIdentity(t *testing.T) {
	// A nil subscription map means no subscribe command has been received and
	// every event type is forwarded; the main agent must not be labelled with
	// a worker instance ID.
	state := &headlessState{}
	event := agent.ToolAskPolicyDeniedEvent{
		CallID: "call-1", ToolName: "shell", AgentID: identity.MainAgentID,
		Policy: config.ConfirmAskPolicyDeny, Reason: "This is not user feedback.",
	}
	env := findHeadlessEnvelope(filterHeadlessEvent(event, state), headlessToolAskPolicyDenied)
	if env == nil {
		t.Fatal("default subscription dropped the terminal policy event")
	}
	data, err := jsonx.Marshal(env.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var got agent.ToolAskPolicyDeniedEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.AgentID != identity.MainAgentID || got != event {
		t.Fatalf("payload=%+v", got)
	}
}
