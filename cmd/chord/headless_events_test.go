package main

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestHeadlessPendingClearedAfterGlobalIdleEvent(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
		questions: map[string]*headlessQuestionPayload{"req-2": {
			ToolName:  "Question",
			RequestID: "req-2",
		}},
		lastError: "some error",
	}

	envs := filterHeadlessEvent(agent.GlobalIdleEvent{}, state)

	state.mu.Lock()
	pc := state.pendingConfirm
	pq := testPendingQuestion(state)
	le := state.lastError
	state.mu.Unlock()

	if pc != nil {
		t.Errorf("pendingConfirm should be nil after GlobalIdleEvent, got %+v", pc)
	}
	if pq == nil {
		t.Errorf("pending questions must survive GlobalIdleEvent, got %+v", pq)
	}
	if le != "" {
		t.Errorf("lastError should be empty after GlobalIdleEvent, got %q", le)
	}
	if len(envs) == 0 {
		t.Fatal("GlobalIdleEvent should produce an envelope")
	}
}

func TestHeadlessMainIdleKeepsActiveSubAgentPhase(t *testing.T) {
	state := &headlessState{}
	filterHeadlessEvent(agent.AgentActivityEvent{AgentID: "agent-1", Type: agent.ActivityStreaming, Detail: "working"}, state)
	filterHeadlessEvent(agent.IdleEvent{}, state)

	state.mu.Lock()
	busy := state.busy
	phase := state.phase
	detail := state.phaseDetail
	state.mu.Unlock()
	if !busy || phase != string(agent.ActivityStreaming) || detail != "working" {
		t.Fatalf("state after main idle = busy=%v phase=%q detail=%q, want active SubAgent streaming state", busy, phase, detail)
	}
}

func TestHeadlessLastErrorSetAfterErrorClearedAfterGlobalIdle(t *testing.T) {
	state := &headlessState{}

	filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("rate limit exceeded"), AgentID: ""}, state)

	state.mu.Lock()
	le := state.lastError
	state.mu.Unlock()

	if le != "rate limit exceeded" {
		t.Errorf("lastError after ErrorEvent = %q, want %q", le, "rate limit exceeded")
	}

	filterHeadlessEvent(agent.GlobalIdleEvent{}, state)

	state.mu.Lock()
	le = state.lastError
	state.mu.Unlock()

	if le != "" {
		t.Errorf("lastError after GlobalIdleEvent = %q, want empty", le)
	}
}

func TestHeadlessPendingOutcomePriority(t *testing.T) {
	state := &headlessState{}

	// ErrorEvent sets pendingOutcome="error"
	filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("fail"), AgentID: ""}, state)

	state.mu.Lock()
	po := state.pendingOutcome
	state.mu.Unlock()
	if po != "error" {
		t.Fatalf("pendingOutcome after ErrorEvent = %q, want %q", po, "error")
	}

	// AgentActivityEvent (non-idle, non-compacting) should NOT overwrite "error"
	filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state)

	state.mu.Lock()
	po = state.pendingOutcome
	state.mu.Unlock()
	if po != "error" {
		t.Errorf("pendingOutcome after activity = %q, want %q (should not be overwritten)", po, "error")
	}
}

func TestHeadlessPendingOutcomeCancelled(t *testing.T) {
	state := &headlessState{}
	to := newTestOut()
	backend := &mockBackend{}

	// Simulate cancel command
	cmd := headlessCommand{Type: "cancel"}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	po := state.pendingOutcome
	state.mu.Unlock()
	if po != "cancelled" {
		t.Fatalf("pendingOutcome after cancel = %q, want %q", po, "cancelled")
	}

	// AgentActivityEvent should NOT overwrite "cancelled"
	filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state)

	state.mu.Lock()
	po = state.pendingOutcome
	state.mu.Unlock()
	if po != "cancelled" {
		t.Errorf("pendingOutcome after activity = %q, want %q (should not be overwritten)", po, "cancelled")
	}
}

func TestHeadlessCompactingNoPendingOutcome(t *testing.T) {
	state := &headlessState{}

	// Compacting activity should NOT set pendingOutcome="completed"
	filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityCompacting, Detail: "compacting context"}, state)

	state.mu.Lock()
	po := state.pendingOutcome
	busy := state.busy
	phase := state.phase
	state.mu.Unlock()

	if po != "" {
		t.Errorf("pendingOutcome after compacting = %q, want empty (should not be set to completed)", po)
	}
	if !busy {
		t.Error("busy should be true after compacting activity")
	}
	if phase != "compacting" {
		t.Errorf("phase after compacting = %q, want %q", phase, "compacting")
	}
}

func TestHeadlessGlobalIdleEventLastOutcome(t *testing.T) {
	tests := []struct {
		name            string
		setupOutcome    string
		wantLastOutcome string
	}{
		{
			name:            "completed outcome",
			setupOutcome:    "completed",
			wantLastOutcome: "completed",
		},
		{
			name:            "cancelled outcome",
			setupOutcome:    "cancelled",
			wantLastOutcome: "cancelled",
		},
		{
			name:            "error outcome",
			setupOutcome:    "error",
			wantLastOutcome: "error",
		},
		{
			name:            "empty outcome",
			setupOutcome:    "",
			wantLastOutcome: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &headlessState{
				pendingOutcome: tt.setupOutcome,
				lastOutcome:    tt.setupOutcome,
			}

			envs := filterHeadlessEvent(agent.GlobalIdleEvent{}, state)
			if len(envs) == 0 {
				t.Fatal("GlobalIdleEvent should produce an envelope")
			}
			env := findHeadlessEnvelope(envs, "idle")
			if env == nil {
				t.Fatalf("idle envelope missing from %#v", envs)
			}
			payload, ok := env.Payload.(map[string]any)
			if !ok {
				t.Fatalf("payload type = %T, want map[string]any", env.Payload)
			}
			if payload["last_outcome"] != tt.wantLastOutcome {
				t.Errorf("last_outcome = %v, want %q", payload["last_outcome"], tt.wantLastOutcome)
			}
		})
	}
}

func TestHeadlessSuppressedGlobalIdleStillClearsBusyState(t *testing.T) {
	state := &headlessState{
		busy:           true,
		phase:          string(agent.ActivityStreaming),
		phaseDetail:    "working",
		pendingOutcome: "completed",
	}

	envs := filterHeadlessEvent(agent.GlobalIdleEvent{SuppressUserNotification: true}, state)
	env := findHeadlessEnvelope(envs, "idle")
	if env == nil {
		t.Fatalf("idle envelope missing from %#v", envs)
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", env.Payload)
	}
	if got := payload["suppress_user_notification"]; got != true {
		t.Fatalf("suppress_user_notification = %v, want true", got)
	}

	state.mu.Lock()
	busy := state.busy
	phase := state.phase
	detail := state.phaseDetail
	lastOutcome := state.lastOutcome
	state.mu.Unlock()
	if busy || phase != "" || detail != "" || lastOutcome != "completed" {
		t.Fatalf("state after suppressed idle = busy=%v phase=%q detail=%q outcome=%q", busy, phase, detail, lastOutcome)
	}
}

func TestHeadlessNotificationEvent(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"notification": true}}
	envs := filterHeadlessEvent(agent.NotificationEvent{
		Reason:  agent.NotificationReasonUserInputRequired,
		Message: "Chord: Loop requires your decision",
	}, state)
	env := findHeadlessEnvelope(envs, "notification")
	if env == nil {
		t.Fatalf("notification envelope missing from %#v", envs)
	}
	payload, ok := env.Payload.(map[string]string)
	if !ok || payload["reason"] != agent.NotificationReasonUserInputRequired || payload["message"] == "" {
		t.Fatalf("notification payload = %#v", env.Payload)
	}
}

func TestHeadlessActivityIdleFiltered(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(agent.AgentActivityEvent{
		Type:   agent.ActivityIdle,
		Detail: "",
	}, state)

	if len(envs) != 0 {
		t.Error("AgentActivityEvent with Type=ActivityIdle should be filtered (return nil)")
	}
}

// ---------------------------------------------------------------------------
// Tests for CompactionStatusEvent
// ---------------------------------------------------------------------------

func TestHeadlessCompactionStatusTerminalEnvelope(t *testing.T) {
	state := &headlessState{}

	ev := agent.CompactionStatusEvent{
		Status:  agent.CompactionStatusSucceeded,
		Trigger: "model_driven",
		Reason:  "checkpoint applied",
		PlanID:  "17",
	}
	envs := filterHeadlessEvent(ev, state)
	if len(envs) != 1 {
		t.Fatalf("terminal compaction status should produce exactly one envelope, got %d", len(envs))
	}
	if envs[0].Type != "compaction_status" {
		t.Fatalf("type = %q, want compaction_status", envs[0].Type)
	}
	payload, ok := envs[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", envs[0].Payload)
	}
	if payload["status"] != agent.CompactionStatusSucceeded {
		t.Errorf("status = %v, want %q", payload["status"], agent.CompactionStatusSucceeded)
	}
	if payload["trigger"] != "model_driven" {
		t.Errorf("trigger = %v, want model_driven", payload["trigger"])
	}
	if payload["reason"] != "checkpoint applied" {
		t.Errorf("reason = %v, want checkpoint applied", payload["reason"])
	}
	if payload["plan_id"] != "17" {
		t.Errorf("plan_id = %v, want 17", payload["plan_id"])
	}
	if payload["synthetic"] != false {
		t.Errorf("synthetic = %v, want false for a real terminal", payload["synthetic"])
	}
}

func TestHeadlessCompactionStatusSkippedCarriesTriggerAndReason(t *testing.T) {
	state := &headlessState{}
	envs := filterHeadlessEvent(agent.CompactionStatusEvent{
		Status:  agent.CompactionStatusSkipped,
		Trigger: "model_driven",
		Reason:  "projected savings too small",
	}, state)
	if len(envs) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(envs))
	}
	payload := envs[0].Payload.(map[string]any)
	if payload["reason"] != "projected savings too small" {
		t.Errorf("reason = %v", payload["reason"])
	}
}

func TestHeadlessCompactionStatusSyntheticStartedCarriesFlag(t *testing.T) {
	state := &headlessState{}
	envs := filterHeadlessEvent(agent.CompactionStatusEvent{
		Status:    agent.CompactionStatusStarted,
		Trigger:   "model_driven",
		PlanID:    "9",
		Synthetic: true,
	}, state)
	if len(envs) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(envs))
	}
	payload := envs[0].Payload.(map[string]any)
	if payload["status"] != agent.CompactionStatusStarted {
		t.Errorf("status = %v, want started", payload["status"])
	}
	if payload["synthetic"] != true {
		t.Errorf("synthetic = %v, want true for the sync-skip started", payload["synthetic"])
	}
	if payload["plan_id"] != "9" {
		t.Errorf("plan_id = %v, want 9", payload["plan_id"])
	}
}

func TestHeadlessCompactionStatusProgressFiltered(t *testing.T) {
	state := &headlessState{}
	if envs := filterHeadlessEvent(agent.CompactionStatusEvent{Status: agent.CompactionStatusProgress}, state); len(envs) != 0 {
		t.Fatalf("progress events must not reach the control plane, got %d", len(envs))
	}
}

func TestHeadlessCompactionStatusRespectsSubscription(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{}}
	if envs := filterHeadlessEvent(agent.CompactionStatusEvent{Status: agent.CompactionStatusSucceeded, Trigger: "model_driven"}, state); len(envs) != 0 {
		t.Fatalf("unsubscribed compaction_status must be filtered, got %d", len(envs))
	}
	state.subscriptions["compaction_status"] = true
	if envs := filterHeadlessEvent(agent.CompactionStatusEvent{Status: agent.CompactionStatusSucceeded, Trigger: "model_driven"}, state); len(envs) != 1 {
		t.Fatalf("subscribed compaction_status must be forwarded, got %d", len(envs))
	}
}

// Tests for AssistantMessageEvent
// ---------------------------------------------------------------------------

func TestHeadlessAssistantMessageEvent(t *testing.T) {
	state := &headlessState{}

	ev := agent.AssistantMessageEvent{
		AgentID:   "",
		Text:      "I've completed the refactoring.",
		ToolCalls: 2,
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("AssistantMessageEvent should produce an envelope")
	}
	if envs[0].Type != "assistant_message" {
		t.Errorf("type = %q, want %q", envs[0].Type, "assistant_message")
	}

	payload, ok := envs[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", envs[0].Payload)
	}
	if payload["text"] != "I've completed the refactoring." {
		t.Errorf("text = %q, want %q", payload["text"], "I've completed the refactoring.")
	}
	if payload["tool_calls"] != 2 {
		t.Errorf("tool_calls = %v, want 2", payload["tool_calls"])
	}
}

func TestHeadlessAssistantMessageEventFromSubAgent(t *testing.T) {
	state := &headlessState{}

	ev := agent.AssistantMessageEvent{
		AgentID:       "sub-1",
		TaskID:        "adhoc-1",
		AgentType:     "reviewer",
		ParentAgentID: "main",
		Text:          "Task done",
		ToolCalls:     0,
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("AssistantMessageEvent from sub should produce an envelope")
	}
	payload := envs[0].Payload.(map[string]any)
	if payload["agent_id"] != "sub-1" {
		t.Errorf("agent_id = %q, want %q", payload["agent_id"], "sub-1")
	}
	if payload["task_id"] != "adhoc-1" || payload["agent_type"] != "reviewer" || payload["parent_agent_id"] != "main" {
		t.Fatalf("subagent metadata = %#v", payload)
	}
}

func TestHeadlessAgentNotifyEnvelopeCarriesSubtype(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(agent.AgentNotifyEvent{
		AgentID: "agent-1",
		TaskID:  "adhoc-1",
		Kind:    "risk_alert",
		Subtype: "stall_resolved",
		Message: "Worker recovered",
	}, state)
	if len(envs) != 1 || envs[0].Type != "agent_notify" {
		t.Fatalf("envelopes = %#v, want one agent_notify", envs)
	}
	payload := envs[0].Payload.(map[string]string)
	if payload["kind"] != "risk_alert" {
		t.Fatalf("kind = %q, want risk_alert", payload["kind"])
	}
	if payload["subtype"] != "stall_resolved" {
		t.Fatalf("subtype = %q, want stall_resolved", payload["subtype"])
	}

	// Notifies without a subtype keep the previous envelope shape.
	envs = filterHeadlessEvent(agent.AgentNotifyEvent{
		AgentID: "agent-1",
		TaskID:  "adhoc-1",
		Kind:    "progress",
		Message: "Working",
	}, state)
	if len(envs) != 1 || envs[0].Type != "agent_notify" {
		t.Fatalf("envelopes = %#v, want one agent_notify", envs)
	}
	if _, ok := envs[0].Payload.(map[string]string)["subtype"]; ok {
		t.Fatalf("subtype key present without a subtype value: %#v", envs[0].Payload)
	}
}

func TestHeadlessSubAgentLifecycleEvents(t *testing.T) {
	state := &headlessState{}
	tests := []struct {
		name       string
		event      agent.AgentEvent
		typeName   string
		messageKey string
		message    string
	}{
		{name: "started", event: agent.AgentStartedEvent{AgentID: "agent-1", PreviousAgentID: "agent-0", TaskID: "adhoc-1", AgentType: "reviewer", ParentAgentID: "main", Description: "Review changes"}, typeName: "agent_started", messageKey: "description", message: "Review changes"},
		{name: "notify", event: agent.AgentNotifyEvent{AgentID: "agent-1", TaskID: "adhoc-1", AgentType: "reviewer", ParentAgentID: "main", TargetAgentID: "main", Kind: "progress", Message: "Tests pass"}, typeName: "agent_notify", messageKey: "message", message: "Tests pass"},
		{name: "done", event: agent.AgentDoneEvent{AgentID: "agent-1", TaskID: "adhoc-1", AgentType: "reviewer", ParentAgentID: "main", Summary: "Reviewed"}, typeName: "agent_done", messageKey: "summary", message: "Reviewed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envs := filterHeadlessEvent(tt.event, state)
			if len(envs) != 1 || envs[0].Type != tt.typeName {
				t.Fatalf("envelopes = %#v", envs)
			}
			payload := envs[0].Payload.(map[string]string)
			if payload["agent_id"] != "agent-1" || payload["task_id"] != "adhoc-1" || payload["agent_type"] != "reviewer" || payload["parent_agent_id"] != "main" || payload[tt.messageKey] != tt.message {
				t.Fatalf("payload = %#v", payload)
			}
			if tt.typeName == "agent_notify" && payload["target_agent_id"] != "main" {
				t.Fatalf("notify payload = %#v", payload)
			}
			if tt.typeName == "agent_started" && payload["previous_agent_id"] != "agent-0" {
				t.Fatalf("started payload = %#v", payload)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for subscribe command
// ---------------------------------------------------------------------------

func TestHeadlessSubscribeFiltersEvents(t *testing.T) {
	state := &headlessState{}

	// Subscribe to only "idle" and "assistant_message"
	cmd := headlessCommand{
		Type:   "subscribe",
		Events: []string{"idle", "assistant_message"},
	}
	to := newTestOut()
	backend := &mockBackend{}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	// Verify subscribe_response was emitted
	items := to.drain()
	if len(items) != 1 || items[0].Type != "subscribe_response" {
		t.Fatalf("expected subscribe_response, got %v", items)
	}

	// Activity event should be filtered (not subscribed)
	envs := filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state)
	if len(envs) != 0 {
		t.Error("activity event should be filtered when not subscribed")
	}

	// GlobalIdleEvent should pass through
	envs = filterHeadlessEvent(agent.GlobalIdleEvent{}, state)
	if len(envs) == 0 {
		t.Fatal("GlobalIdleEvent should pass through when subscribed")
	}

	// AssistantMessageEvent should pass through
	envs = filterHeadlessEvent(agent.AssistantMessageEvent{Text: "hello"}, state)
	if len(envs) == 0 {
		t.Fatal("AssistantMessageEvent should pass through when subscribed")
	}
}

func TestHeadlessSubscribeAllByDefault(t *testing.T) {
	state := &headlessState{} // no subscribe command = all events

	// All events should pass through by default
	envs := filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state)
	if len(envs) == 0 {
		t.Error("activity event should pass through by default")
	}

	envs = filterHeadlessEvent(agent.GlobalIdleEvent{}, state)
	if len(envs) == 0 {
		t.Error("idle event should pass through by default")
	}

	envs = filterHeadlessEvent(agent.AssistantMessageEvent{Text: "hello"}, state)
	if len(envs) == 0 {
		t.Error("assistant_message event should pass through by default")
	}
}

func TestHeadlessSubscribeUnknownOnlyDoesNotFallBackToAll(t *testing.T) {
	state := &headlessState{}
	cmd := headlessCommand{
		Type:   "subscribe",
		Events: []string{"nonexistent_event"},
	}
	to := newTestOut()
	backend := &mockBackend{}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	items := to.drain()
	if len(items) != 1 || items[0].Type != "subscribe_response" {
		t.Fatalf("expected subscribe_response, got %v", items)
	}

	state.mu.Lock()
	subs := state.subscriptions
	state.mu.Unlock()
	if subs == nil {
		t.Fatal("subscriptions = nil, want explicit empty subscription set")
	}
	if len(subs) != 0 {
		t.Fatalf("subscriptions = %#v, want empty set", subs)
	}

	if envs := filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state); len(envs) != 0 {
		t.Fatalf("activity envs = %#v, want none for unknown-only subscription", envs)
	}
	if envs := filterHeadlessEvent(agent.GlobalIdleEvent{}, state); len(envs) != 0 {
		t.Fatalf("idle envs = %#v, want none for unknown-only subscription", envs)
	}
	if envs := filterHeadlessEvent(agent.AssistantMessageEvent{Text: "hello"}, state); len(envs) != 0 {
		t.Fatalf("assistant envs = %#v, want none for unknown-only subscription", envs)
	}
}

func TestHeadlessActivityEventPayload(t *testing.T) {
	state := &headlessState{}

	ev := agent.AgentActivityEvent{
		AgentID: "sub-1",
		Type:    agent.ActivityStreaming,
		Detail:  "analyzing code",
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("AgentActivityEvent should produce an envelope")
	}
	if envs[0].Type != "activity" {
		t.Errorf("type = %q, want %q", envs[0].Type, "activity")
	}

	payload, ok := envs[0].Payload.(map[string]string)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]string", envs[0].Payload)
	}
	if payload["agent_id"] != "sub-1" {
		t.Errorf("agent_id = %q, want sub-1", payload["agent_id"])
	}
	if payload["type"] != "streaming" {
		t.Errorf("type = %q, want streaming", payload["type"])
	}
}

func TestHeadlessToolResultEventIsNotForwarded(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(agent.ToolResultEvent{
		CallID: "call-1",
		Name:   "Shell",
		Status: message.ToolStatusSuccess,
	}, state)

	if len(envs) != 0 {
		t.Fatalf("envs = %+v, want none", envs)
	}
}

func TestHeadlessDoneToolResultEmitsDoneCompletion(t *testing.T) {
	state := &headlessState{}
	handleHeadlessCommand(headlessCommand{Type: "subscribe", Events: []string{"done_completion"}}, &mockBackend{}, state, newTestOut().writer())

	envs := filterHeadlessEvent(agent.ToolResultEvent{
		CallID:     "call-done",
		Name:       "Done",
		ArgsJSON:   `{"reason":"ready","report":"from args"}`,
		Status:     message.ToolStatusSuccess,
		DoneReport: "All requested work is complete.",
		AgentID:    "",
	}, state)

	if len(envs) != 1 {
		t.Fatalf("envs len = %d, want 1: %+v", len(envs), envs)
	}
	if envs[0].Type != "done_completion" {
		t.Fatalf("env type = %q, want done_completion", envs[0].Type)
	}
	payload, ok := envs[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", envs[0].Payload)
	}
	if payload["report"] != "All requested work is complete." {
		t.Fatalf("report = %q", payload["report"])
	}
	if payload["reason"] != "ready" {
		t.Fatalf("reason = %q", payload["reason"])
	}
	if payload["mode"] != "normal" {
		t.Fatalf("mode = %q", payload["mode"])
	}
}

func TestHeadlessDoneToolResultWithoutReportDoesNotNotify(t *testing.T) {
	state := &headlessState{}
	handleHeadlessCommand(headlessCommand{Type: "subscribe", Events: []string{"done_completion"}}, &mockBackend{}, state, newTestOut().writer())

	envs := filterHeadlessEvent(agent.ToolResultEvent{
		CallID: "call-done",
		Name:   "Done",
		Status: message.ToolStatusSuccess,
	}, state)

	if len(envs) != 0 {
		t.Fatalf("envs = %+v, want none", envs)
	}
}

func TestHeadlessSubscribeIgnoresUnknownEventTypes(t *testing.T) {
	state := &headlessState{}

	// Subscribe including an unknown event type
	cmd := headlessCommand{
		Type:   "subscribe",
		Events: []string{"idle", "nonexistent_event"},
	}
	to := newTestOut()
	backend := &mockBackend{}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	// Only "idle" should be in subscriptions
	state.mu.Lock()
	subs := state.subscriptions
	state.mu.Unlock()

	if subs["nonexistent_event"] {
		t.Error("unknown event type should not be in subscriptions")
	}
	if !subs["idle"] {
		t.Error("idle should be in subscriptions")
	}
}
