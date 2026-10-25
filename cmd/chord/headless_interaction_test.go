package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestHeadlessHandoffEventAndCommand(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\nDo the work."), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &headlessState{subscriptions: map[string]bool{"handoff_request": true}}
	backend := &mockBackend{}

	envs := filterHeadlessEvent(agent.HandoffEvent{PlanPath: planPath, RequestID: "handoff-1"}, state, backend)
	env := findHeadlessEnvelope(envs, "handoff_request")
	if env == nil {
		t.Fatalf("handoff_request envelope not emitted: %v", envs)
	}
	payload, ok := env.Payload.(*headlessHandoffPayload)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload.PlanPath != planPath || payload.PlanText != "# Plan\n\nDo the work." || payload.RequestID != "handoff-1" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if len(payload.Agents) != 2 || payload.Agents[0].Name != "builder" || payload.Agents[0].CurrentModelPool != "fast" {
		t.Fatalf("unexpected agents: %+v", payload.Agents)
	}

	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: payload.RequestID, Action: "accept", Agent: "reviewer", Pool: "smart"}, backend, state, to.writer())

	state.mu.Lock()
	pending := state.pendingHandoff
	state.mu.Unlock()
	if pending != nil {
		t.Fatalf("pending handoff not cleared: %+v", pending)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.executeCalls) != 0 {
		t.Fatalf("execute calls = %+v, want 0 (approval goes through ResolveHandoff)", backend.executeCalls)
	}
	if len(backend.handoffCalls) != 1 || backend.handoffCalls[0].action != "approve" || backend.handoffCalls[0].agentName != "reviewer" || backend.handoffCalls[0].requestID != payload.RequestID {
		t.Fatalf("handoff calls = %+v", backend.handoffCalls)
	}
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "set-agent:reviewer:smart" {
		t.Fatalf("sent messages = %+v", backend.sentMessages)
	}
}

// An empty eligible-target list must reach the client verbatim: fabricating a
// builder default would offer a target the runtime rejects on approval.
func TestHeadlessHandoffRequestEmptyAgentsNotFabricated(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"handoff_request": true}}
	backend := &mockBackend{handoffOptionsSet: true}

	envs := filterHeadlessEvent(agent.HandoffEvent{PlanPath: "/tmp/plan.md", RequestID: "hand-1"}, state, backend)
	env := findHeadlessEnvelope(envs, "handoff_request")
	if env == nil {
		t.Fatalf("handoff_request envelope not emitted: %v", envs)
	}
	payload, ok := env.Payload.(*headlessHandoffPayload)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if len(payload.Agents) != 0 {
		t.Fatalf("agents = %+v, want an empty list without fabricated defaults", payload.Agents)
	}
}

func TestHeadlessHandoffCancelledMatchingClearsAndForwards(t *testing.T) {
	state := &headlessState{
		subscriptions:  map[string]bool{"handoff_cancelled": true},
		pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1", PlanPath: "/tmp/plan.md"},
	}

	envs := filterHeadlessEvent(agent.HandoffCancelledEvent{RequestID: "handoff-1", Reason: "superseded"}, state)

	env := findHeadlessEnvelope(envs, "handoff_cancelled")
	if env == nil {
		t.Fatalf("handoff_cancelled envelope not emitted: %v", envs)
	}
	payload, ok := env.Payload.(map[string]string)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["request_id"] != "handoff-1" || payload["reason"] != "superseded" {
		t.Fatalf("payload = %#v, want request_id handoff-1 / reason superseded", payload)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %+v, want nil after a matching cancellation", state.pendingHandoff)
	}
	if state.updatedAt.IsZero() {
		t.Fatal("updatedAt should be refreshed by the cancellation")
	}
}

func TestHeadlessHandoffCancelledWithoutSubscriptionStillClears(t *testing.T) {
	state := &headlessState{
		subscriptions:  map[string]bool{"idle": true},
		pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1"},
	}

	envs := filterHeadlessEvent(agent.HandoffCancelledEvent{RequestID: "handoff-1", Reason: "superseded"}, state)
	if env := findHeadlessEnvelope(envs, "handoff_cancelled"); env != nil {
		t.Fatalf("handoff_cancelled should not be forwarded without a subscription: %v", env)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %+v, want nil even without a subscription", state.pendingHandoff)
	}
}

func TestHeadlessHandoffCancelledMismatchKeepsPendingButForwards(t *testing.T) {
	state := &headlessState{
		subscriptions:  map[string]bool{"handoff_cancelled": true},
		pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-2", PlanPath: "/tmp/newer.md"},
	}

	envs := filterHeadlessEvent(agent.HandoffCancelledEvent{RequestID: "handoff-1", Reason: "superseded"}, state)

	env := findHeadlessEnvelope(envs, "handoff_cancelled")
	if env == nil {
		t.Fatalf("a stale cancellation must still be forwarded: %v", envs)
	}
	if payload, ok := env.Payload.(map[string]string); !ok || payload["request_id"] != "handoff-1" {
		t.Fatalf("payload = %#v, want request_id handoff-1", env.Payload)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingHandoff == nil || state.pendingHandoff.RequestID != "handoff-2" {
		t.Fatalf("a newer pendingHandoff must survive a stale cancellation, got %+v", state.pendingHandoff)
	}
}

func TestHeadlessHandoffCancelledEmptyRequestIDClears(t *testing.T) {
	state := &headlessState{
		subscriptions:  map[string]bool{"handoff_cancelled": true},
		pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1"},
	}

	envs := filterHeadlessEvent(agent.HandoffCancelledEvent{RequestID: "", Reason: "superseded"}, state)
	if env := findHeadlessEnvelope(envs, "handoff_cancelled"); env == nil {
		t.Fatalf("handoff_cancelled envelope not emitted: %v", envs)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingHandoff != nil {
		t.Fatalf("an empty RequestID should cancel whatever is pending, got %+v", state.pendingHandoff)
	}
}

func TestHeadlessHandoffCancelledWithoutPendingDoesNotPanic(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"handoff_cancelled": true}}

	envs := filterHeadlessEvent(agent.HandoffCancelledEvent{RequestID: "handoff-1", Reason: "superseded"}, state)
	if env := findHeadlessEnvelope(envs, "handoff_cancelled"); env == nil {
		t.Fatalf("handoff_cancelled envelope not emitted: %v", envs)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %+v, want nil", state.pendingHandoff)
	}
}

func TestHeadlessHandoffCancelledClearsStatusPendingHandoff(t *testing.T) {
	// Mirrors a session switch: the agent discards the pending handoff before the
	// client decides, and the next status_response must report null.
	state := &headlessState{subscriptions: map[string]bool{"handoff_cancelled": true}}
	backend := &mockBackend{}
	filterHeadlessEvent(agent.HandoffEvent{PlanPath: "/tmp/plan.md", RequestID: "handoff-1"}, state, backend)
	filterHeadlessEvent(agent.HandoffCancelledEvent{RequestID: "handoff-1", Reason: "superseded"}, state)

	to := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if env == nil {
		t.Fatal("status_response not emitted")
	}
	data, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["pending_handoff"] != nil {
		t.Fatalf("pending_handoff = %#v, want null after the handoff was cancelled", payload["pending_handoff"])
	}
}

func TestHeadlessHandoffDenyContinuesFromContext(t *testing.T) {
	state := &headlessState{pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1", PlanPath: "/tmp/plan.md"}}
	backend := &mockBackend{}
	to := newTestOut(t)

	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: "handoff-1", Action: "deny", DenyReason: "needs more detail"}, backend, state, to.writer())

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.continueCalls != 0 || len(backend.sentMessages) != 0 {
		t.Fatalf("deny should only resolve handoff, continue=%d messages=%v", backend.continueCalls, backend.sentMessages)
	}
	if len(backend.handoffCalls) != 1 || backend.handoffCalls[0].action != "deny" || !strings.Contains(backend.handoffCalls[0].denyReason, "needs more detail") {
		t.Fatalf("handoff calls = %+v", backend.handoffCalls)
	}
}

func TestHeadlessPendingConfirmClearedAfterConfirm(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "confirm",
		Action:    "allow",
		RequestID: "req-1",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingConfirm != nil {
		t.Errorf("pendingConfirm should be nil after confirm command, got %+v", state.pendingConfirm)
	}
}

// TestHeadlessConfirmResolvedMatchingClearsAndForwards pins the terminal push a
// client without a local countdown depends on: a confirmation that ended on the
// runtime side (auto-deny timeout or cancellation) clears the cached pending
// request and is forwarded with its outcome.
func TestHeadlessConfirmResolvedMatchingClearsAndForwards(t *testing.T) {
	state := &headlessState{
		subscriptions: map[string]bool{"confirm_resolved": true},
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Read",
			RequestID: "req-1",
		},
	}

	envs := filterHeadlessEvent(agent.ConfirmResolvedEvent{RequestID: "req-1", ToolName: "Read", AgentID: "main", Outcome: agent.ConfirmOutcomeTimeout}, state)

	env := findHeadlessEnvelope(envs, "confirm_resolved")
	if env == nil {
		t.Fatalf("confirm_resolved envelope not emitted: %v", envs)
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["request_id"] != "req-1" || payload["outcome"] != "timeout" || payload["tool_name"] != "Read" || payload["agent_id"] != "main" {
		t.Fatalf("payload = %#v, want req-1 timeout for main Read", payload)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingConfirm != nil {
		t.Fatalf("pendingConfirm = %+v, want nil after a matching resolution", state.pendingConfirm)
	}
	if state.updatedAt.IsZero() {
		t.Fatal("updatedAt should be refreshed by the resolution")
	}
}

// TestHeadlessConfirmResolvedWithoutSubscriptionStillClears mirrors the
// handoff cancellation rule: the cached state drops the stale request even
// when the client never subscribed to the resolution event.
func TestHeadlessConfirmResolvedWithoutSubscriptionStillClears(t *testing.T) {
	state := &headlessState{
		subscriptions: map[string]bool{"idle": true},
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Read",
			RequestID: "req-1",
		},
	}

	envs := filterHeadlessEvent(agent.ConfirmResolvedEvent{RequestID: "req-1", Outcome: agent.ConfirmOutcomeCancelled}, state)
	if env := findHeadlessEnvelope(envs, "confirm_resolved"); env != nil {
		t.Fatalf("confirm_resolved should not be forwarded without a subscription: %v", env)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingConfirm != nil {
		t.Fatalf("pendingConfirm = %+v, want nil even without a subscription", state.pendingConfirm)
	}
}

// TestHeadlessConfirmResolvedMismatchKeepsPendingButForwards covers a
// resolution for an older request while a newer one is already pending: the
// newer request stays actionable, the terminal fact is still forwarded.
func TestHeadlessConfirmResolvedMismatchKeepsPendingButForwards(t *testing.T) {
	state := &headlessState{
		subscriptions: map[string]bool{"confirm_resolved": true},
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Edit",
			RequestID: "req-2",
		},
	}

	envs := filterHeadlessEvent(agent.ConfirmResolvedEvent{RequestID: "req-1", Outcome: agent.ConfirmOutcomeTimeout}, state)
	if env := findHeadlessEnvelope(envs, "confirm_resolved"); env == nil {
		t.Fatalf("confirm_resolved envelope not emitted: %v", envs)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingConfirm == nil || state.pendingConfirm.RequestID != "req-2" {
		t.Fatalf("pendingConfirm = %+v, want the newer req-2 kept", state.pendingConfirm)
	}
}

func TestHeadlessConfirmSupportsRuleIntent(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Shell",
			RequestID: "req-1",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:        "confirm",
		Action:      "allow",
		RequestID:   "req-1",
		RulePattern: "git *",
		RuleScope:   "project",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.confirmCalls) != 1 {
		t.Fatalf("confirm calls = %d, want 1", len(backend.confirmCalls))
	}
	call := backend.confirmCalls[0]
	if call.ruleIntent == nil {
		t.Fatal("expected rule intent in confirm call")
	}
	if !reflect.DeepEqual(call.ruleIntent.Rules, []agent.ConfirmRule{{Pattern: "git *"}}) {
		t.Fatalf("rule patterns = %#v, want %#v", call.ruleIntent.Rules, []agent.ConfirmRule{{Pattern: "git *"}})
	}
	if call.ruleIntent.Scope != int(permission.ScopeProject) {
		t.Fatalf("rule scope = %d, want %d(project)", call.ruleIntent.Scope, int(permission.ScopeProject))
	}
}

// rule_tool lets a headless client install the rule under the tool whose ask
// actually blocked the call (a delete rule for an apply_patch Move), which the
// confirmed tool alone cannot express.
func TestHeadlessConfirmRuleIntentNamesTool(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "apply_patch",
			RequestID: "req-1",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:        "confirm",
		Action:      "allow",
		RequestID:   "req-1",
		RuleTool:    "delete",
		RulePattern: ".chord/notes/*",
		RuleScope:   "session",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.confirmCalls) != 1 {
		t.Fatalf("confirm calls = %d, want 1", len(backend.confirmCalls))
	}
	call := backend.confirmCalls[0]
	if call.ruleIntent == nil {
		t.Fatal("expected rule intent in confirm call")
	}
	want := []agent.ConfirmRule{{Tool: "delete", Pattern: ".chord/notes/*"}}
	if !reflect.DeepEqual(call.ruleIntent.Rules, want) {
		t.Fatalf("rule intent rules = %#v, want %#v", call.ruleIntent.Rules, want)
	}
	if call.ruleIntent.Scope != int(permission.ScopeSession) {
		t.Fatalf("rule scope = %d, want %d(session)", call.ruleIntent.Scope, int(permission.ScopeSession))
	}
}

func TestHeadlessConfirmRejectsInvalidRuleScope(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:        "confirm",
		Action:      "allow",
		RequestID:   "req-1",
		RulePattern: "git *",
		RuleScope:   "invalid-scope",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	backend.mu.Lock()
	callCount := len(backend.confirmCalls)
	backend.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("confirm calls = %d, want 0 when scope is invalid", callCount)
	}
	envs := to.drain()
	env := findHeadlessEnvelopeValue(envs, "error")
	if env == nil {
		t.Fatalf("expected error envelope, got %+v", envs)
	}
}

func TestHeadlessQuestionCommandDefersPendingToResolvedEvent(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-2": {
			ToolName:  "Question",
			RequestID: "req-2",
		}},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "question",
		Answers: []string{"yes"},
		Action:  agent.QuestionOpAnswer, OperationID: "op-answer",
		RequestID: "req-2",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	backend.mu.Lock()
	calls := backend.questionCalls
	backend.mu.Unlock()
	if len(calls) != 1 || calls[0].reason != tools.QuestionOutcomeAnswered || calls[0].requestID != "req-2" {
		t.Fatalf("question calls = %#v, want one answered for req-2", calls)
	}

	// The command must not clear the cache itself: the core resolved event is
	// the authoritative source.
	state.mu.Lock()
	pending := testPendingQuestion(state)
	state.mu.Unlock()
	if pending == nil {
		t.Fatal("pendingQuestion must stay set until the resolved event arrives")
	}

	// A matching resolved event clears it.
	filterHeadlessEvent(agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: "req-2", Outcome: tools.QuestionOutcomeAnswered, Version: 2}}, state)
	state.mu.Lock()
	pending = testPendingQuestion(state)
	state.mu.Unlock()
	if pending != nil {
		t.Fatalf("pendingQuestion = %#v, want nil after the matching resolved event", pending)
	}
}

func TestHeadlessQuestionCommandRejectsInvalidAction(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-2": {ToolName: "Question", RequestID: "req-2"}},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{
		Type:    "question",
		Answers: []string{"yes"},
		Action:  tools.QuestionOutcomeNoResponse, OperationID: "op-answer",
		RequestID: "req-2",
	}, backend, state, to.writer())

	backend.mu.Lock()
	calls := backend.questionCalls
	backend.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("question calls = %#v, want none for an invalid action", calls)
	}
	if env := findHeadlessEnvelopeValue(to.drain(), "question_receipt"); env == nil {
		t.Fatal("expected an error envelope for an invalid action")
	}
}

func TestHeadlessQuestionCommandSurfacesCoreRejection(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-2": {ToolName: "Question", RequestID: "req-2"}},
	}
	to := newTestOut(t)
	backend := &mockBackend{resolveQuestionFail: true}

	handleHeadlessCommand(headlessCommand{
		Type:    "question",
		Answers: []string{"yes"},
		Action:  agent.QuestionOpAnswer, OperationID: "op-answer",
		RequestID: "req-2",
	}, backend, state, to.writer())

	if env := findHeadlessEnvelopeValue(to.drain(), "question_receipt"); env == nil {
		t.Fatal("expected an error envelope when Core rejects the answer")
	}
	// A rejected answer must not clear the pending cache: Core kept the
	// request (or another one is pending), and only its resolved event may.
	state.mu.Lock()
	pending := testPendingQuestion(state)
	state.mu.Unlock()
	if pending == nil || pending.RequestID != "req-2" {
		t.Fatalf("pendingQuestion = %#v, want req-2 preserved after rejection", pending)
	}
}

func TestHeadlessQuestionCommandReportsLostRaceWithDeadline(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-3": {ToolName: "Question", RequestID: "req-3"}},
	}
	to := newTestOut(t)
	backend := &mockBackend{resolveQuestionTerminal: tools.QuestionOutcomeNoResponse}

	handleHeadlessCommand(headlessCommand{
		Type:    "question",
		Answers: []string{"yes"},
		Action:  agent.QuestionOpAnswer, OperationID: "op-answer",
		RequestID: "req-3",
	}, backend, state, to.writer())

	env := findHeadlessEnvelopeValue(to.drain(), "question_receipt")
	if env == nil {
		t.Fatal("expected an error envelope when the deadline decided the outcome")
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("error payload = %T, want map[string]any", env.Payload)
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, tools.QuestionOutcomeNoResponse) {
		t.Fatalf("error message = %q, want the winning terminal reason", message)
	}
	// The answer did not decide the state, so the pending cache stays until the
	// question's own resolved event arrives.
	state.mu.Lock()
	pending := testPendingQuestion(state)
	state.mu.Unlock()
	if pending == nil || pending.RequestID != "req-3" {
		t.Fatalf("pendingQuestion = %#v, want req-3 preserved", pending)
	}
}

func TestHeadlessQuestionResolvedMismatchKeepsPending(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-new": {ToolName: "Question", RequestID: "req-new"}},
	}
	filterHeadlessEvent(agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: "req-old", Outcome: tools.QuestionOutcomeNoResponse, Version: 2}}, state)
	state.mu.Lock()
	defer state.mu.Unlock()
	if testPendingQuestion(state) == nil || testPendingQuestion(state).RequestID != "req-new" {
		t.Fatalf("pendingQuestion = %#v, want req-new preserved", testPendingQuestion(state))
	}
}

func TestHeadlessAutoDenyConfirmOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "do something else",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	pc := state.pendingConfirm
	state.mu.Unlock()
	if pc != nil {
		t.Errorf("pendingConfirm should be nil after send command auto-denied it, got %+v", pc)
	}

	backend.mu.Lock()
	calls := backend.confirmCalls
	msgs := backend.sentMessages
	backend.mu.Unlock()
	if len(calls) != 1 || calls[0].action != "deny" || calls[0].requestID != "req-1" {
		t.Errorf("confirm calls = %v, want [deny req-1]", calls)
	}
	if len(msgs) != 1 || msgs[0] != "do something else" {
		t.Errorf("sent messages = %v, want [do something else]", msgs)
	}
}

func TestHeadlessRetainsQuestionOnUserMessage(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-2": {
			ToolName:  "Question",
			RequestID: "req-2",
		}},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "skip the question",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	// Ordinary input preserves the pending question and does not submit an answer.
	state.mu.Lock()
	pq := testPendingQuestion(state)
	state.mu.Unlock()
	if pq == nil {
		t.Error("the pending question must remain available after ordinary input")
	}

	backend.mu.Lock()
	calls := backend.questionCalls
	msgs := backend.sentMessages
	backend.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("question resolve calls = %v, want none after a regular message", calls)
	}
	if len(msgs) != 1 || msgs[0] != "skip the question" {
		t.Errorf("sent messages = %v, want [skip the question]", msgs)
	}
	// The new message must be accepted before the blocked Question is woken:
	// otherwise the resumed tool could queue a model request ahead of it.
	order := append([]string(nil), backend.callOrder...)
	if len(order) != 1 || order[0] != "send:skip the question" {
		t.Errorf("backend call order = %v, want [send:skip the question]", order)
	}
}

// A question and a handoff can be pending at the same time: the question is
// waiting for input while the agent offers a plan. A regular message retains
// the question and closes the handoff.
func TestHeadlessSendRetainsQuestionAndClearsHandoff(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-2": {
			ToolName:  "Question",
			RequestID: "req-2",
		}},
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "move on",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	// The question remains available; the handoff is explicitly closed.
	state.mu.Lock()
	pq := testPendingQuestion(state)
	ph := state.pendingHandoff
	state.mu.Unlock()
	if pq == nil {
		t.Error("the pending question must remain available after ordinary input")
	}
	if ph != nil {
		t.Errorf("pendingHandoff = %+v, want it cleared by the send", ph)
	}

	backend.mu.Lock()
	handoffCalls := append([]handoffCall(nil), backend.handoffCalls...)
	msgs := append([]string(nil), backend.sentMessages...)
	order := append([]string(nil), backend.callOrder...)
	backend.mu.Unlock()
	if len(handoffCalls) != 1 || handoffCalls[0].action != "cancel" || handoffCalls[0].requestID != "handoff-1" {
		t.Errorf("handoff calls = %v, want [cancel handoff-1]", handoffCalls)
	}
	if len(msgs) != 1 || msgs[0] != "move on" {
		t.Errorf("sent messages = %v, want [move on]", msgs)
	}
	// The handoff cancellation is recorded separately; question submission
	// is never synthesized from this ordinary input.
	if len(order) != 1 || order[0] != "send:move on" {
		t.Errorf("backend call order = %v, want [send:move on]", order)
	}
}

func TestHeadlessAutoCancelHandoffOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "revise the plan instead",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	pending := state.pendingHandoff
	state.mu.Unlock()
	if pending != nil {
		t.Errorf("pendingHandoff should be nil after send command auto-cancelled it, got %+v", pending)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.continueCalls != 0 {
		t.Errorf("ContinueFromContext calls = %d, want 0", backend.continueCalls)
	}
	if len(backend.executeCalls) != 0 {
		t.Errorf("execute calls = %v, want none", backend.executeCalls)
	}
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "revise the plan instead" {
		t.Errorf("sent messages = %v, want [revise the plan instead]", backend.sentMessages)
	}
}

// A send that auto-cancels a pending handoff must push handoff_cancelled to
// subscribers, exactly like the runtime-initiated supersede path, so a
// integration drops its stale approval prompt instead of routing the next
// /handoff to a discarded request.
func TestHeadlessAutoCancelHandoffOnUserMessageEmitsCancelled(t *testing.T) {
	state := &headlessState{
		subscriptions: map[string]bool{"handoff_cancelled": true},
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "revise the plan instead"}, backend, state, to.writer())

	state.mu.Lock()
	pending := state.pendingHandoff
	state.mu.Unlock()
	if pending != nil {
		t.Fatalf("pendingHandoff = %+v, want nil after auto-cancel", pending)
	}

	env := findHeadlessEnvelopeValue(to.drain(), "handoff_cancelled")
	if env == nil {
		t.Fatal("handoff_cancelled envelope not emitted")
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["request_id"] != "handoff-1" {
		t.Errorf("request_id = %v, want handoff-1", payload["request_id"])
	}
	if payload["reason"] != headlessHandoffCancelledReasonSuperseded {
		t.Errorf("reason = %v, want %q", payload["reason"], headlessHandoffCancelledReasonSuperseded)
	}

	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, to.writer())
	statusEnv := findHeadlessEnvelopeValue(to.drain(), "status_response")
	if statusEnv == nil {
		t.Fatal("status_response not emitted")
	}
	data, err := json.Marshal(statusEnv.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if status["pending_handoff"] != nil {
		t.Fatalf("pending_handoff = %#v, want null after auto-cancel", status["pending_handoff"])
	}
}

func TestHeadlessAutoCancelHandoffWithoutSubscriptionDoesNotEmit(t *testing.T) {
	state := &headlessState{
		subscriptions: map[string]bool{"idle": true},
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "next"}, backend, state, to.writer())

	if env := findHeadlessEnvelopeValue(to.drain(), "handoff_cancelled"); env != nil {
		t.Fatalf("handoff_cancelled should not be forwarded without a subscription: %#v", env)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %+v, want nil even without a subscription", state.pendingHandoff)
	}
}

func TestHeadlessAutoCancelHandoffWithoutPendingDoesNotEmit(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"handoff_cancelled": true}}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "hello"}, backend, state, to.writer())

	if env := findHeadlessEnvelopeValue(to.drain(), "handoff_cancelled"); env != nil {
		t.Fatalf("handoff_cancelled should not be emitted with no pending handoff: %#v", env)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.handoffCalls) != 0 {
		t.Fatalf("handoff calls = %+v, want none", backend.handoffCalls)
	}
}

// An explicit handoff command is the client deciding for itself, so it must not
// manufacture a cancellation event for the client that sent it.
func TestHeadlessExplicitHandoffCancelDoesNotEmitCancelled(t *testing.T) {
	state := &headlessState{
		subscriptions: map[string]bool{"handoff_cancelled": true},
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: "handoff-1", Action: "cancel"}, backend, state, to.writer())

	if env := findHeadlessEnvelopeValue(to.drain(), "handoff_cancelled"); env != nil {
		t.Fatalf("explicit handoff cancel must not emit handoff_cancelled: %#v", env)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.handoffCalls) != 1 || backend.handoffCalls[0].action != "cancel" {
		t.Fatalf("handoff calls = %+v, want [cancel]", backend.handoffCalls)
	}
}

func TestHeadlessAutoDenyConfirmAndRetainsQuestionOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
		questions: map[string]*headlessQuestionPayload{"req-2": {
			ToolName:  "Question",
			RequestID: "req-2",
		}},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "new message",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	pc := state.pendingConfirm
	pq := testPendingQuestion(state)
	state.mu.Unlock()
	if pc != nil {
		t.Errorf("pendingConfirm should be nil, got %+v", pc)
	}
	if pq == nil {
		t.Error("the pending question must remain available after ordinary input")
	}

	backend.mu.Lock()
	cc := backend.confirmCalls
	qc := backend.questionCalls
	msgs := backend.sentMessages
	backend.mu.Unlock()
	if len(cc) != 1 || cc[0].action != "deny" {
		t.Errorf("confirm calls = %v, want [deny]", cc)
	}
	if len(qc) != 0 {
		t.Errorf("question resolve calls = %v, want none", qc)
	}
	if len(msgs) != 1 || msgs[0] != "new message" {
		t.Errorf("sent messages = %v, want [new message]", msgs)
	}
}

func TestHeadlessNoAutoDenyWhenNoPendingConfirm(t *testing.T) {
	state := &headlessState{}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "hello",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	backend.mu.Lock()
	cc := backend.confirmCalls
	qc := backend.questionCalls
	msgs := backend.sentMessages
	backend.mu.Unlock()
	if len(cc) != 0 {
		t.Errorf("confirm calls should be empty, got %v", cc)
	}
	if len(qc) != 0 {
		t.Errorf("question calls should be empty, got %v", qc)
	}
	if len(msgs) != 1 || msgs[0] != "hello" {
		t.Errorf("sent messages = %v, want [hello]", msgs)
	}
}

// ---------------------------------------------------------------------------
// Tests for event envelope payloads
// ---------------------------------------------------------------------------

func TestHeadlessConfirmRequestEventPayload(t *testing.T) {
	state := &headlessState{}

	ev := agent.ConfirmRequestEvent{
		ToolName:            "Shell",
		ArgsJSON:            `{"command":"rm -rf /"}`,
		RequestID:           "req-1",
		Timeout:             30 * time.Second,
		NeedsApproval:       []string{"a.go", "b/c.txt"},
		AlreadyAllowed:      []string{"d.go"},
		NeedsApprovalRules:  []permission.Rule{{Permission: "Shell", Pattern: "ask Shell(rm*)", Action: permission.ActionAsk}},
		AlreadyAllowedRules: []permission.Rule{{Permission: "Shell", Pattern: "allow Shell(ls*)", Action: permission.ActionAllow}},
		AgentID:             "main-1",
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("ConfirmRequestEvent should produce an envelope")
	}
	env := findHeadlessEnvelope(envs, "confirm_request")
	if env == nil {
		t.Fatalf("confirm_request envelope missing from %#v", envs)
	}

	data, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload["tool_name"] != "Shell" {
		t.Errorf("tool_name = %v, want Shell", payload["tool_name"])
	}
	if payload["request_id"] != "req-1" {
		t.Errorf("request_id = %v, want req-1", payload["request_id"])
	}
	if payload["agent_id"] != "main-1" {
		t.Errorf("agent_id = %v, want main-1", payload["agent_id"])
	}
	if payload["timeout_ms"] != float64(30000) {
		t.Errorf("timeout_ms = %v, want 30000", payload["timeout_ms"])
	}
	if got := stringSliceFromAny(t, payload["needs_approval_rules"]); !reflect.DeepEqual(got, permission.RulePatterns(ev.NeedsApprovalRules)) {
		t.Errorf("needs_approval_rules = %#v, want %#v", got, permission.RulePatterns(ev.NeedsApprovalRules))
	}
	if got := stringSliceFromAny(t, payload["already_allowed_rules"]); !reflect.DeepEqual(got, permission.RulePatterns(ev.AlreadyAllowedRules)) {
		t.Errorf("already_allowed_rules = %#v, want %#v", got, permission.RulePatterns(ev.AlreadyAllowedRules))
	}

	state.mu.Lock()
	pc := state.pendingConfirm
	state.mu.Unlock()

	if pc == nil {
		t.Fatal("pendingConfirm should be set")
	}
	if pc.ToolName != "Shell" {
		t.Errorf("pendingConfirm.ToolName = %q, want Shell", pc.ToolName)
	}
	if pc.AgentID != "main-1" {
		t.Errorf("pendingConfirm.AgentID = %q, want main-1", pc.AgentID)
	}
	if !reflect.DeepEqual(pc.NeedsApprovalRules, permission.RulePatterns(ev.NeedsApprovalRules)) {
		t.Errorf("pendingConfirm.NeedsApprovalRules = %#v, want %#v", pc.NeedsApprovalRules, permission.RulePatterns(ev.NeedsApprovalRules))
	}
	if !reflect.DeepEqual(pc.AlreadyAllowedRules, permission.RulePatterns(ev.AlreadyAllowedRules)) {
		t.Errorf("pendingConfirm.AlreadyAllowedRules = %#v, want %#v", pc.AlreadyAllowedRules, permission.RulePatterns(ev.AlreadyAllowedRules))
	}
}

func TestHeadlessQuestionRequestEventPayload(t *testing.T) {
	state := &headlessState{}

	deadline := time.Now().Add(60 * time.Second).UTC().Truncate(time.Second)
	ev := questionEventForTest("req-2", "Task sub-1", "Continue?", []string{"yes", "no"}, []string{"Yes, proceed", "No, stop"}, false, deadline, "worker-1")

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("QuestionRequestEvent should produce an envelope")
	}
	env := findHeadlessEnvelope(envs, "question_request")
	if env == nil {
		t.Fatalf("question_request envelope missing from %#v", envs)
	}

	data, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload["tool_name"] != tools.NameQuestion {
		t.Errorf("tool_name = %v, want Question", payload["tool_name"])
	}
	if payload["header"] != "Task sub-1" {
		t.Errorf("header = %v, want Task sub-1", payload["header"])
	}
	if payload["question"] != "Continue?" {
		t.Errorf("question = %v, want Continue?", payload["question"])
	}
	if _, ok := payload["default_answer"]; ok {
		t.Errorf("default_answer must not be present, got %v", payload["default_answer"])
	}
	if payload["deadline"] != deadline.Format(time.RFC3339) {
		t.Errorf("deadline = %v, want %v", payload["deadline"], deadline.Format(time.RFC3339))
	}
	if payload["agent_id"] != "worker-1" {
		t.Errorf("agent_id = %v, want worker-1", payload["agent_id"])
	}

	state.mu.Lock()
	pq := testPendingQuestion(state)
	state.mu.Unlock()

	if pq == nil {
		t.Fatal("pendingQuestion should be set")
	}
	if pq.ToolName != tools.NameQuestion {
		t.Errorf("pendingQuestion.ToolName = %q, want Question", pq.ToolName)
	}
	if pq.AgentID != "worker-1" {
		t.Errorf("pendingQuestion.AgentID = %q, want worker-1", pq.AgentID)
	}
}

// A question without question_timeout has no close time, so the event must not
// carry the key at all: a nil *time.Time in the payload map would encode as
// "deadline": null, which reads as a close time that exists.
func TestHeadlessQuestionRequestOmitsDeadlineWithoutTimeout(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(questionEventForTest("req-1", "Task sub-1", "Continue?", nil, nil, false, time.Time{}, ""), state)

	env := findHeadlessEnvelope(envs, "question_request")
	if env == nil {
		t.Fatalf("question_request envelope missing from %#v", envs)
	}

	data, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if strings.Contains(string(data), "\"deadline\"") {
		t.Errorf("payload carries a deadline key without a configured timeout: %s", data)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if value, ok := payload["deadline"]; ok {
		t.Errorf("deadline = %v, want the key to be absent", value)
	}

	state.mu.Lock()
	pq := testPendingQuestion(state)
	state.mu.Unlock()
	if pq == nil || pq.Deadline != nil {
		t.Fatalf("pendingQuestion = %#v, want no deadline", pq)
	}
}

func TestHeadlessConfirmMismatchedRequestID(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "confirm",
		Action:    "allow",
		RequestID: "req-wrong",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	pc := state.pendingConfirm
	state.mu.Unlock()
	if pc == nil {
		t.Error("pendingConfirm should NOT be nil when request_id doesn't match")
	}
}

func TestHeadlessQuestionMismatchedRequestID(t *testing.T) {
	state := &headlessState{
		questions: map[string]*headlessQuestionPayload{"req-2": {
			ToolName:  "Question",
			RequestID: "req-2",
		}},
	}
	to := newTestOut(t)
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "question",
		Answers:   []string{"yes"},
		RequestID: "req-wrong",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	state.mu.Lock()
	pq := testPendingQuestion(state)
	state.mu.Unlock()
	if pq == nil {
		t.Error("pendingQuestion should NOT be nil when request_id doesn't match")
	}
}

func TestDefaultHandoffAgent(t *testing.T) {
	tests := []struct {
		name    string
		options []agent.HandoffAgentOption
		want    string
	}{
		{name: "default named option", options: []agent.HandoffAgentOption{{Name: " planner "}, {Name: " builder ", Default: true}}, want: "builder"},
		{name: "first named fallback", options: []agent.HandoffAgentOption{{Name: " "}, {Name: " planner "}, {Name: "builder"}}, want: "planner"},
		// No eligible agent was offered: the caller must report an error
		// instead of inventing a target.
		{name: "empty fallback", options: []agent.HandoffAgentOption{{Name: " "}}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultHandoffAgent(tt.options); got != tt.want {
				t.Fatalf("defaultHandoffAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A handoff approval may only name one of the agents the pending handoff
// offered. Unknown roles, SubAgents, and the current active role are rejected
// with an error envelope while the pending handoff stays open for a corrected
// command or an explicit cancel.
func TestHeadlessHandoffApproveRejectsUnavailableAgent(t *testing.T) {
	state := &headlessState{pendingHandoff: &headlessHandoffPayload{
		RequestID: "handoff-1",
		PlanPath:  "/tmp/plan.md",
		Agents: []agent.HandoffAgentOption{
			{Name: "builder", Default: true},
			{Name: "reviewer"},
		},
	}}
	backend := &mockBackend{}
	to := newTestOut(t)

	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: "handoff-1", Action: "accept", Agent: "ghost"}, backend, state, to.writer())

	envs := to.drain()
	if len(envs) != 1 || envs[0].Type != "error" || !strings.Contains(headlessErrorMessage(t, envs[0]), "not available") {
		t.Fatalf("envelopes = %+v, want a single not-available error", envs)
	}
	if state.pendingHandoff == nil {
		t.Fatal("pending handoff must stay open after a rejected target")
	}
	if len(backend.handoffCalls) != 0 {
		t.Fatalf("handoff calls = %+v, want none (runtime never resolves)", backend.handoffCalls)
	}

	// The current active role is never selectable either.
	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: "handoff-1", Action: "accept", Agent: "planner"}, backend, state, to.writer())
	if state.pendingHandoff == nil || len(backend.handoffCalls) != 0 {
		t.Fatalf("current-role target must also be rejected: pending=%+v calls=%+v", state.pendingHandoff, backend.handoffCalls)
	}

	// With no eligible agents at all, a bare accept cannot invent a target.
	state.pendingHandoff = &headlessHandoffPayload{RequestID: "handoff-2", PlanPath: "/tmp/plan.md"}
	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: "handoff-2", Action: "accept"}, backend, state, to.writer())
	last := to.drain()
	if len(last) == 0 || last[len(last)-1].Type != "error" || !strings.Contains(headlessErrorMessage(t, last[len(last)-1]), "no eligible") {
		t.Fatalf("envelopes = %+v, want no-eligible-agent error", last)
	}
}
