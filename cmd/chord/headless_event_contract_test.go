package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// sessionDirBackend wraps mockBackend with a configurable session directory so
// session-switch contract tests can drive headlessBackendSessionID without
// touching the shared mock.
type sessionDirBackend struct {
	*mockBackend
	dir string
}

func (b sessionDirBackend) SessionDir() string { return b.dir }

func headlessPayloadMap(t *testing.T, payload any) map[string]any {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

// Silent retry telemetry must never surface as an integration-visible error:
// the TUI records it in the error panel only, and a terminal failure is always
// followed by a non-silent error.
func TestHeadlessSilentErrorEventDropped(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("retry 1/6"), AgentID: "", Silent: true}, state)
	if env := findHeadlessEnvelope(envs, "error"); env != nil {
		t.Fatalf("silent ErrorEvent must not emit an error envelope: %#v", env)
	}

	state.mu.Lock()
	po, lastErr := state.pendingOutcome, state.lastError
	state.mu.Unlock()
	if po != "" {
		t.Errorf("pendingOutcome after silent ErrorEvent = %q, want empty", po)
	}
	if lastErr != "" {
		t.Errorf("lastError after silent ErrorEvent = %q, want empty", lastErr)
	}
}

// A turn that hits a silent retry and then recovers must still report
// last_outcome=completed at global idle: the recovered retry must not stick as
// the gateway-visible terminal outcome.
func TestHeadlessSilentErrorDoesNotStickAfterRecovery(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"idle": true}}

	filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("retry 1/6"), Silent: true}, state)
	filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state)
	envs := filterHeadlessEvent(agent.GlobalIdleEvent{}, state)

	env := findHeadlessEnvelope(envs, "idle")
	if env == nil {
		t.Fatal("idle envelope not emitted")
	}
	payload := headlessPayloadMap(t, env.Payload)
	if payload["last_outcome"] != "completed" {
		t.Errorf("last_outcome = %v, want completed after a recovered silent retry", payload["last_outcome"])
	}
}

// A non-silent error keeps the established contract: error envelope,
// pendingOutcome=error, and lastError recorded.
func TestHeadlessNonSilentErrorEventStillReported(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("boom"), AgentID: ""}, state)
	if env := findHeadlessEnvelope(envs, "error"); env == nil {
		t.Fatal("non-silent ErrorEvent must emit an error envelope")
	}

	state.mu.Lock()
	po, lastErr := state.pendingOutcome, state.lastError
	state.mu.Unlock()
	if po != "error" {
		t.Errorf("pendingOutcome = %q, want error", po)
	}
	if lastErr != "boom" {
		t.Errorf("lastError = %q, want boom", lastErr)
	}
}

// An in-band session switch must be announced explicitly: the tracked session
// id moves to the backend's committed session and subscribers get a
// session_switched push (the startup snapshot alone never counts as the
// gateway having seen the new session).
func TestHeadlessSessionRestoredPushesSessionSwitched(t *testing.T) {
	state := &headlessState{
		sessionID:     "sess-old",
		subscriptions: map[string]bool{"session_switched": true},
	}
	backend := sessionDirBackend{mockBackend: &mockBackend{}, dir: "/tmp/chord/sess-new"}

	envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, backend)

	env := findHeadlessEnvelope(envs, "session_switched")
	if env == nil {
		t.Fatal("session_switched envelope not emitted")
	}
	if payload := headlessPayloadMap(t, env.Payload); payload["session_id"] != "sess-new" {
		t.Errorf("session_id = %v, want sess-new", payload["session_id"])
	}

	state.mu.Lock()
	got := state.sessionID
	state.mu.Unlock()
	if got != "sess-new" {
		t.Errorf("tracked sessionID = %q, want sess-new", got)
	}
}

// Restores that keep the session (startup replay, durable compaction rewrite)
// must not push: only an actual change is announced.
func TestHeadlessSessionRestoredSameSessionNoPush(t *testing.T) {
	state := &headlessState{
		sessionID:     "sess-1",
		subscriptions: map[string]bool{"session_switched": true},
	}
	backend := sessionDirBackend{mockBackend: &mockBackend{}, dir: "/tmp/chord/sess-1"}

	if envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, backend); len(envs) != 0 {
		t.Fatalf("same-session restore must not emit envelopes, got %v", envs)
	}
}

// Backends that cannot report a session directory (and empty resolutions)
// leave the tracked id untouched and emit nothing.
func TestHeadlessSessionRestoredWithoutSessionBackend(t *testing.T) {
	state := &headlessState{
		sessionID:     "sess-1",
		subscriptions: map[string]bool{"session_switched": true},
	}

	if envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, &mockBackend{}); len(envs) != 0 {
		t.Fatalf("restore without a session backend must not emit envelopes, got %v", envs)
	}
	emptyDir := sessionDirBackend{mockBackend: &mockBackend{}}
	if envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, emptyDir); len(envs) != 0 {
		t.Fatalf("restore with an empty session dir must not emit envelopes, got %v", envs)
	}

	state.mu.Lock()
	got := state.sessionID
	state.mu.Unlock()
	if got != "sess-1" {
		t.Errorf("tracked sessionID = %q, want sess-1", got)
	}
}

// session_switched honors subscriptions like every other push event.
func TestHeadlessSessionSwitchedRespectsSubscription(t *testing.T) {
	state := &headlessState{sessionID: "sess-old", subscriptions: map[string]bool{}}
	backend := sessionDirBackend{mockBackend: &mockBackend{}, dir: "/tmp/chord/sess-new"}

	if envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, backend); len(envs) != 0 {
		t.Fatalf("session_switched must not be forwarded without a subscription, got %v", envs)
	}

	// The tracked id still moves: status_response must report the new session
	// even for clients that did not subscribe to the push.
	state.mu.Lock()
	got := state.sessionID
	state.mu.Unlock()
	if got != "sess-new" {
		t.Errorf("tracked sessionID = %q, want sess-new", got)
	}
}

// status_response reports the tracked session id, falling back to the startup
// snapshot only before any restore was observed.
func TestHeadlessStatusReportsTrackedSession(t *testing.T) {
	to := newTestOut()
	state := &headlessState{sessionID: "sess-new"}

	handleHeadlessCommand(headlessCommand{Type: "status"}, &mockBackend{}, state, to.writer(), "sess-old")

	env := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if env == nil {
		t.Fatal("status_response not emitted")
	}
	if payload := headlessPayloadMap(t, env.Payload); payload["session_id"] != "sess-new" {
		t.Errorf("session_id = %v, want sess-new (tracked id wins over startup snapshot)", payload["session_id"])
	}
}

// A finished background job's durable result is the only delivery channel for
// the JOB RESULT card, so it must be pushed explicitly.
func TestHeadlessBackgroundResultPush(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"background_result": true}}

	envs := filterHeadlessEvent(agent.BackgroundResultAppendedEvent{
		Message:       message.Message{Content: "JOB RESULT job-1\noutput here"},
		TargetAgentID: "main",
		MessageIndex:  7,
	}, state)

	env := findHeadlessEnvelope(envs, "background_result")
	if env == nil {
		t.Fatal("background_result envelope not emitted")
	}
	payload := headlessPayloadMap(t, env.Payload)
	if payload["content"] != "JOB RESULT job-1\noutput here" {
		t.Errorf("content = %v, want the durable result text", payload["content"])
	}
	if payload["target_agent_id"] != "main" {
		t.Errorf("target_agent_id = %v, want main", payload["target_agent_id"])
	}
	if payload["message_index"] != float64(7) {
		t.Errorf("message_index = %v, want 7", payload["message_index"])
	}
}

func TestHeadlessBackgroundResultRespectsSubscription(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{}}

	envs := filterHeadlessEvent(agent.BackgroundResultAppendedEvent{
		Message:       message.Message{Content: "JOB RESULT job-1"},
		TargetAgentID: "main",
	}, state)
	if len(envs) != 0 {
		t.Fatalf("background_result must not be forwarded without a subscription, got %v", envs)
	}
}

// Context-pressure warnings are durable user-facing alerts with no other
// headless channel, so they must be pushed explicitly.
func TestHeadlessContextNoticePush(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"context_notice": true}}

	envs := filterHeadlessEvent(agent.ContextNoticeEvent{
		Level:        "warning",
		Message:      "Context will be compacted at the next safe boundary.",
		MessageIndex: 12,
	}, state)

	env := findHeadlessEnvelope(envs, "context_notice")
	if env == nil {
		t.Fatal("context_notice envelope not emitted")
	}
	payload := headlessPayloadMap(t, env.Payload)
	if payload["level"] != "warning" {
		t.Errorf("level = %v, want warning", payload["level"])
	}
	if payload["message"] != "Context will be compacted at the next safe boundary." {
		t.Errorf("message = %v, want the notice text", payload["message"])
	}
	if payload["message_index"] != float64(12) {
		t.Errorf("message_index = %v, want 12", payload["message_index"])
	}
}

func TestHeadlessContextNoticeRespectsSubscription(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{}}

	envs := filterHeadlessEvent(agent.ContextNoticeEvent{Level: "warning", Message: "pressure"}, state)
	if len(envs) != 0 {
		t.Fatalf("context_notice must not be forwarded without a subscription, got %v", envs)
	}
}

// The new push types are part of the subscribable contract: an explicit
// subscribe listing them forwards them, and unknown names stay ignored.
func TestHeadlessSubscribeAcceptsNewPushTypes(t *testing.T) {
	to := newTestOut()
	state := &headlessState{}

	handleHeadlessCommand(headlessCommand{
		Type:   "subscribe",
		Events: []string{"session_switched", "background_result", "context_notice", "no_such_event"},
	}, &mockBackend{}, state, to.writer(), "test-session")
	to.drain()

	backend := sessionDirBackend{mockBackend: &mockBackend{}, dir: "/sessions/sess-new"}
	state.sessionID = "sess-old"
	if envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, backend); findHeadlessEnvelope(envs, "session_switched") == nil {
		t.Error("session_switched not forwarded after subscribe")
	}
	if envs := filterHeadlessEvent(agent.BackgroundResultAppendedEvent{
		Message: message.Message{Content: "JOB RESULT"},
	}, state); findHeadlessEnvelope(envs, "background_result") == nil {
		t.Error("background_result not forwarded after subscribe")
	}
	if envs := filterHeadlessEvent(agent.ContextNoticeEvent{Level: "warning", Message: "m"}, state); findHeadlessEnvelope(envs, "context_notice") == nil {
		t.Error("context_notice not forwarded after subscribe")
	}

	state.mu.Lock()
	subscribedUnknown := state.subscriptions["no_such_event"]
	state.mu.Unlock()
	if subscribedUnknown {
		t.Error("unknown event names must not enter subscriptions")
	}
}
