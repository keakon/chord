package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
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

func TestHeadlessInitialStatusSeqSurvivesWireEncoding(t *testing.T) {
	var output bytes.Buffer
	writer := newStdoutWriter(t.Context(), &output)
	go writer.run()
	state := &headlessState{sessionID: "session-start"}
	handleHeadlessCommand(headlessCommand{Type: "status"}, &mockBackend{}, state, writer)
	writer.close()

	var envelope struct {
		Type string  `json:"type"`
		Seq  *uint64 `json:"seq"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("decode status envelope: %v", err)
	}
	if envelope.Type != "status_response" || envelope.Seq == nil || *envelope.Seq != headlessGenesisSeq {
		t.Fatalf("initial status must carry genesis seq %d: %s", headlessGenesisSeq, output.String())
	}
}

func TestHeadlessStatusSeqOrdersSnapshotAgainstPushes(t *testing.T) {
	state := &headlessState{
		sessionID:     "sess-old",
		subscriptions: map[string]bool{"session_switched": true},
	}
	backend := sessionDirBackend{mockBackend: &mockBackend{}, dir: "/tmp/chord/sess-new"}

	// Snapshot taken before the switch: still the old session.
	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	// The switch commits and pushes after the snapshot was copied.
	envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, backend)
	push := findHeadlessEnvelope(envs, "session_switched")
	if push == nil {
		t.Fatal("session_switched envelope not emitted")
	}

	// Snapshot taken after the switch: the new session, same version as the push.
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}

	if stale.Seq == 0 {
		t.Fatal("initial status snapshot must have a nonzero version")
	}
	if stale.Seq >= push.Seq {
		t.Errorf("stale status seq = %d, want < push seq = %d", stale.Seq, push.Seq)
	}
	if fresh.Seq != push.Seq {
		t.Errorf("fresh status seq = %d, want push seq = %d", fresh.Seq, push.Seq)
	}
	stalePayload := headlessPayloadMap(t, stale.Payload)
	freshPayload := headlessPayloadMap(t, fresh.Payload)
	if stalePayload["session_id"] != "sess-old" {
		t.Errorf("stale session_id = %v, want sess-old", stalePayload["session_id"])
	}
	if freshPayload["session_id"] != "sess-new" {
		t.Errorf("fresh session_id = %v, want sess-new", freshPayload["session_id"])
	}
}

// Push envelopes are versioned in emission order across event types.
func TestHeadlessPushSeqIncreasesInEmissionOrder(t *testing.T) {
	state := &headlessState{}

	first := filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "one"}, state)
	second := filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "two"}, state)

	find := func(envs []*headlessEnvelope) *headlessEnvelope {
		for _, env := range envs {
			if env.Type == "activity" {
				return env
			}
		}
		return nil
	}
	a, b := find(first), find(second)
	if a == nil || b == nil {
		t.Fatalf("activity envelopes not emitted: %v %v", first, second)
	}
	if a.Seq <= headlessGenesisSeq || b.Seq != a.Seq+1 {
		t.Errorf("push seqs = %d, %d, want consecutive versions after genesis %d", a.Seq, b.Seq, headlessGenesisSeq)
	}
}

// A role set announces role_change on the command path. That push must bump
// seq so a status snapshot copied before the switch is strictly older and a
// later snapshot shares the announcement's version.
func TestHeadlessCommandPathRoleChangeSeqOrdersSnapshot(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"}
	state := &headlessState{role: "builder", subscriptions: map[string]bool{"role_change": true}}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, to.writer())
	push := findHeadlessEnvelopeValue(to.drain(), "role_change")
	if push == nil {
		t.Fatal("role_change not emitted")
	}
	if push.Seq == 0 {
		t.Fatal("command-path role_change must be versioned")
	}
	if stale.Seq >= push.Seq {
		t.Errorf("stale status seq = %d, want < role_change seq = %d", stale.Seq, push.Seq)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if fresh.Seq != push.Seq {
		t.Errorf("fresh status seq = %d, want role_change seq = %d", fresh.Seq, push.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["current_role"] != "planner" {
		t.Errorf("current_role = %v, want planner", payload["current_role"])
	}
}

// A status snapshot copies role and seq under one lock. An event-loop
// RoleChangedEvent that races the copy must not bind the previous role to the
// announcement's version — gateway would then treat that snapshot as current
// and roll CurrentRole back.
func TestHeadlessStatusSnapshotRoleMatchesSeqAgainstEventLoop(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"}
	state := &headlessState{role: "builder", subscriptions: map[string]bool{"role_change": true}}
	to := newTestOut()

	const goroutines = 6
	const statusesPer = 24
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			start.Wait()
			for range statusesPer {
				handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
			}
		})
	}
	start.Done()
	pushEnvs := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state)
	wg.Wait()

	if len(pushEnvs) != 1 || pushEnvs[0].Type != "role_change" {
		t.Fatalf("role_change envelopes = %#v, want one announcement", pushEnvs)
	}
	pushSeq := pushEnvs[0].Seq
	if pushSeq == 0 {
		t.Fatal("event-loop role_change must be versioned")
	}

	sawStatus := false
	for _, env := range to.drain() {
		if env.Type != "status_response" {
			continue
		}
		sawStatus = true
		role := headlessPayloadMap(t, env.Payload)["current_role"]
		switch {
		case env.Seq < pushSeq:
			if role != "builder" {
				t.Errorf("pre-switch status seq=%d current_role=%v, want builder", env.Seq, role)
			}
		case env.Seq == pushSeq:
			if role != "planner" {
				t.Errorf("status seq=%d (role_change version) current_role=%v, want planner", env.Seq, role)
			}
		default:
			t.Errorf("status seq=%d > role_change seq=%d", env.Seq, pushSeq)
		}
	}
	if !sawStatus {
		t.Fatal("expected status_response envelopes from the racing status commands")
	}
}

// An empty role cache backfills outside the snapshot lock. If RoleChangedEvent
// commits and bumps seq while CurrentRole is still in flight, status must use
// the cached role, not the stale backfill return value, with that new seq.
func TestHeadlessStatusSnapshotIgnoresStaleRoleBackfill(t *testing.T) {
	backend := &delayedCurrentRoleBackend{
		mockBackend: &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"},
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	state := &headlessState{subscriptions: map[string]bool{"role_change": true}}
	to := newTestOut()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	}()
	<-backend.started
	pushEnvs := filterHeadlessEvent(agent.RoleChangedEvent{Role: "planner"}, state)
	close(backend.release)
	<-done

	if len(pushEnvs) != 1 || pushEnvs[0].Type != "role_change" {
		t.Fatalf("role_change envelopes = %#v, want one announcement", pushEnvs)
	}
	env := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if env == nil {
		t.Fatal("status_response not emitted")
	}
	if env.Seq != pushEnvs[0].Seq {
		t.Errorf("status seq = %d, want role_change seq %d", env.Seq, pushEnvs[0].Seq)
	}
	if payload := headlessPayloadMap(t, env.Payload); payload["current_role"] != "planner" {
		t.Errorf("current_role = %v, want planner (event-loop cache), not the stale backfill", payload["current_role"])
	}
}

type delayedCurrentRoleBackend struct {
	*mockBackend
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
}

func (b *delayedCurrentRoleBackend) CurrentRole() string {
	b.startedOnce.Do(func() { close(b.started) })
	<-b.release
	return b.mockBackend.CurrentRole()
}

// Auto-cancelling a pending handoff on send is a command-path state mutation.
// Its handoff_cancelled push must bump seq the same way an event-loop cancel
// does, so a snapshot copied while the request was still pending is stale.
func TestHeadlessCommandPathHandoffCancelledSeqOrdersSnapshot(t *testing.T) {
	backend := &mockBackend{}
	state := &headlessState{
		subscriptions:  map[string]bool{"handoff_cancelled": true},
		pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1", PlanPath: "/tmp/plan.md"},
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "revise"}, backend, state, to.writer())
	push := findHeadlessEnvelopeValue(to.drain(), "handoff_cancelled")
	if push == nil {
		t.Fatal("handoff_cancelled not emitted")
	}
	if push.Seq == 0 {
		t.Fatal("command-path handoff_cancelled must be versioned")
	}
	if stale.Seq >= push.Seq {
		t.Errorf("stale status seq = %d, want < handoff_cancelled seq = %d", stale.Seq, push.Seq)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if fresh.Seq != push.Seq {
		t.Errorf("fresh status seq = %d, want handoff_cancelled seq = %d", fresh.Seq, push.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["pending_handoff"] != nil {
		t.Fatalf("pending_handoff = %#v, want null", payload["pending_handoff"])
	}
}

// Auto-denying a pending confirm on send has no cancelled envelope, but the
// cache mutation still bumps seq so a snapshot copied while the request was
// pending is strictly older than a later status that reports it gone.
func TestHeadlessCommandPathAutoDenyConfirmSeqOrdersSnapshot(t *testing.T) {
	backend := &mockBackend{}
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{RequestID: "confirm-1", ToolName: "shell"},
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}
	if payload := headlessPayloadMap(t, stale.Payload); payload["pending_confirm"] == nil {
		t.Fatal("stale status should still report the pending confirm")
	}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "do something else"}, backend, state, to.writer())
	if len(backend.confirmCalls) != 1 || backend.confirmCalls[0].action != "deny" {
		t.Fatalf("auto-deny confirm calls = %#v, want one deny", backend.confirmCalls)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if stale.Seq == 0 || fresh.Seq <= stale.Seq {
		t.Errorf("stale seq = %d, fresh seq = %d, want fresh strictly newer", stale.Seq, fresh.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["pending_confirm"] != nil {
		t.Fatalf("pending_confirm = %#v, want null", payload["pending_confirm"])
	}
}

// Auto-cancelling a pending question on send is the same class of silent
// command-path mutation as auto-denying a confirm: bump seq without a push.
func TestHeadlessCommandPathAutoCancelQuestionSeqOrdersSnapshot(t *testing.T) {
	backend := &mockBackend{}
	state := &headlessState{
		pendingQuestion: &headlessQuestionPayload{RequestID: "question-1", Question: "which file?"},
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "never mind"}, backend, state, to.writer())
	if len(backend.questionCalls) != 1 || !backend.questionCalls[0].cancelled {
		t.Fatalf("auto-cancel question calls = %#v, want one cancelled", backend.questionCalls)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if stale.Seq == 0 || fresh.Seq <= stale.Seq {
		t.Errorf("stale seq = %d, fresh seq = %d, want fresh strictly newer", stale.Seq, fresh.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["pending_question"] != nil {
		t.Fatalf("pending_question = %#v, want null", payload["pending_question"])
	}
}

// An explicit confirm command also clears pending state without a push, so
// the next status_response must be a newer version than a snapshot copied
// while the request was still pending.
func TestHeadlessConfirmCommandSeqOrdersSnapshot(t *testing.T) {
	backend := &mockBackend{}
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{RequestID: "confirm-2", ToolName: "shell"},
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	handleHeadlessCommand(headlessCommand{Type: "confirm", Action: "allow", RequestID: "confirm-2"}, backend, state, to.writer())

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if stale.Seq == 0 || fresh.Seq <= stale.Seq {
		t.Errorf("stale seq = %d, fresh seq = %d, want fresh strictly newer", stale.Seq, fresh.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["pending_confirm"] != nil {
		t.Fatalf("pending_confirm = %#v, want null", payload["pending_confirm"])
	}
}

// Auto-cancelling a pending handoff still bumps seq when the client is not
// subscribed to handoff_cancelled, because the pending cache changed even
// though no envelope is emitted.
func TestHeadlessCommandPathAutoCancelHandoffWithoutSubscriptionSeqOrdersSnapshot(t *testing.T) {
	backend := &mockBackend{}
	state := &headlessState{
		subscriptions:  map[string]bool{"idle": true},
		pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-2", PlanPath: "/tmp/plan.md"},
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "revise"}, backend, state, to.writer())
	if env := findHeadlessEnvelopeValue(to.drain(), "handoff_cancelled"); env != nil {
		t.Fatalf("handoff_cancelled should not be forwarded without a subscription: %#v", env)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if stale.Seq == 0 || fresh.Seq <= stale.Seq {
		t.Errorf("stale seq = %d, fresh seq = %d, want fresh strictly newer", stale.Seq, fresh.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["pending_handoff"] != nil {
		t.Fatalf("pending_handoff = %#v, want null", payload["pending_handoff"])
	}
}

// status_response reports the tracked session id, falling back to the startup
// snapshot only before any restore was observed.
func TestHeadlessStatusReportsTrackedSession(t *testing.T) {
	to := newTestOut()
	state := &headlessState{sessionID: "sess-new"}

	handleHeadlessCommand(headlessCommand{Type: "status"}, &mockBackend{}, state, to.writer())

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
	state := &headlessState{subscriptions: map[string]bool{"background_result": true}, sessionID: "sess-1"}

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
	if payload["session_id"] != "sess-1" {
		t.Errorf("session_id = %v, want the tracked session", payload["session_id"])
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
	state := &headlessState{subscriptions: map[string]bool{"context_notice": true}, sessionID: "sess-1"}

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
	if payload["session_id"] != "sess-1" {
		t.Errorf("session_id = %v, want the tracked session", payload["session_id"])
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
	}, &mockBackend{}, state, to.writer())
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

// An unsubscribed event-loop mutation still bumps seq: the cache moved, so a
// status snapshot copied before it must compare older than a later snapshot.
func TestHeadlessUnsubscribedEventLoopMutationBumpsSeq(t *testing.T) {
	backend := &mockBackend{}
	state := &headlessState{
		sessionID:     "sess-old",
		subscriptions: map[string]bool{"idle": true},
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	// session_switched is not subscribed, so no envelope is emitted, but the
	// tracked session moves and the version must advance.
	switchBackend := sessionDirBackend{mockBackend: &mockBackend{}, dir: "/tmp/chord/sess-new"}
	if envs := filterHeadlessEvent(agent.SessionRestoredEvent{}, state, switchBackend); len(envs) != 0 {
		t.Fatalf("session_switched must not be forwarded without a subscription, got %v", envs)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if stale.Seq == 0 || fresh.Seq <= stale.Seq {
		t.Errorf("stale seq = %d, fresh seq = %d, want fresh strictly newer", stale.Seq, fresh.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["session_id"] != "sess-new" {
		t.Errorf("session_id = %v, want sess-new", payload["session_id"])
	}
}

// A command-path role switch without a role_change subscription still bumps
// seq, so snapshots before and after compare ordered.
func TestHeadlessCommandPathRoleChangeWithoutSubscriptionBumpsSeq(t *testing.T) {
	backend := &mockBackend{availableRoles: []string{"builder", "planner"}, currentRole: "builder"}
	state := &headlessState{role: "builder", subscriptions: map[string]bool{"idle": true}}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	stale := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if stale == nil {
		t.Fatal("status_response not emitted")
	}

	handleHeadlessCommand(headlessCommand{Type: "role", Action: "set", Role: "planner"}, backend, state, to.writer())
	if env := findHeadlessEnvelopeValue(to.drain(), "role_change"); env != nil {
		t.Fatalf("role_change should not be forwarded without a subscription: %#v", env)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	fresh := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if fresh == nil {
		t.Fatal("second status_response not emitted")
	}
	if stale.Seq == 0 || fresh.Seq <= stale.Seq {
		t.Errorf("stale seq = %d, fresh seq = %d, want fresh strictly newer", stale.Seq, fresh.Seq)
	}
	if payload := headlessPayloadMap(t, fresh.Payload); payload["current_role"] != "planner" {
		t.Errorf("current_role = %v, want planner", payload["current_role"])
	}
}
