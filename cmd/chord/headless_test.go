package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/protocol"
	"github.com/keakon/chord/internal/tools"
)

// ---------------------------------------------------------------------------
// testOut — wraps a real stdoutWriter backed by a threadsafe buffer for testing
// ---------------------------------------------------------------------------

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) snapshotAndReset() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	data := append([]byte(nil), b.buf.Bytes()...)
	b.buf.Reset()
	return data
}

type testOut struct {
	buf *synchronizedBuffer
	out *stdoutWriter
}

func newTestOut() *testOut {
	buf := &synchronizedBuffer{}
	ctx := context.Background()
	out := newStdoutWriter(ctx, buf)
	go out.run()
	return &testOut{
		buf: buf,
		out: out,
	}
}

// writer returns the *stdoutWriter for passing to handleHeadlessCommand.
func (t *testOut) writer() *stdoutWriter {
	return t.out
}

// drain reads all buffered JSONL lines and returns parsed envelopes.
func (t *testOut) drain() []headlessEnvelope {
	flushAck := make(chan struct{})
	if !t.out.emit(map[string]any{"type": "__test_flush__", "payload": map[string]any{"ack": true}}) {
		return nil
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var result []headlessEnvelope
		lines := bytes.Split(t.buf.snapshotAndReset(), []byte{'\n'})
		for _, line := range lines {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var env headlessEnvelope
			if err := json.Unmarshal(line, &env); err != nil {
				continue
			}
			if env.Type == "__test_flush__" {
				close(flushAck)
				continue
			}
			result = append(result, env)
		}
		select {
		case <-flushAck:
			return result
		default:
		}
		if time.Now().After(deadline) {
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// mockBackend — fake headlessBackend for testing
// ---------------------------------------------------------------------------

type mockBackend struct {
	mu            sync.Mutex
	sentMessages  []string
	confirmCalls  []confirmCall
	questionCalls []questionCall
	cancelCalls   int
}

type confirmCall struct {
	action        string
	finalArgsJSON string
	editSummary   string
	denyReason    string
	requestID     string
	ruleIntent    *agent.ConfirmRuleIntent
}

type questionCall struct {
	answers   []string
	cancelled bool
	requestID string
}

func (m *mockBackend) SendUserMessage(content string) {
	m.mu.Lock()
	m.sentMessages = append(m.sentMessages, content)
	m.mu.Unlock()
}

func (m *mockBackend) CancelCurrentTurn() bool {
	m.mu.Lock()
	m.cancelCalls++
	m.mu.Unlock()
	return true
}

func (m *mockBackend) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
	m.mu.Lock()
	m.confirmCalls = append(m.confirmCalls, confirmCall{
		action:        action,
		finalArgsJSON: finalArgsJSON,
		editSummary:   editSummary,
		denyReason:    denyReason,
		requestID:     requestID,
	})
	m.mu.Unlock()
}

func (m *mockBackend) ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *agent.ConfirmRuleIntent) {
	m.mu.Lock()
	var copiedIntent *agent.ConfirmRuleIntent
	if ruleIntent != nil {
		intentCopy := *ruleIntent
		copiedIntent = &intentCopy
	}
	m.confirmCalls = append(m.confirmCalls, confirmCall{
		action:        action,
		finalArgsJSON: finalArgsJSON,
		editSummary:   editSummary,
		denyReason:    denyReason,
		requestID:     requestID,
		ruleIntent:    copiedIntent,
	})
	m.mu.Unlock()
}

func (m *mockBackend) ResolveQuestion(answers []string, cancelled bool, requestID string) {
	m.mu.Lock()
	m.questionCalls = append(m.questionCalls, questionCall{answers, cancelled, requestID})
	m.mu.Unlock()
}

func findHeadlessEnvelope(envs []*headlessEnvelope, typ string) *headlessEnvelope {
	for _, env := range envs {
		if env != nil && env.Type == typ {
			return env
		}
	}
	return nil
}

func findHeadlessEnvelopeValue(envs []headlessEnvelope, typ string) *headlessEnvelope {
	for i := range envs {
		if envs[i].Type == typ {
			return &envs[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tests for headlessState tracking via filterHeadlessEvent
// ---------------------------------------------------------------------------

func TestHeadlessPendingConfirmClearedAfterConfirm(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "confirm",
		Action:    "allow",
		RequestID: "req-1",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingConfirm != nil {
		t.Errorf("pendingConfirm should be nil after confirm command, got %+v", state.pendingConfirm)
	}
}

func TestHeadlessConfirmSupportsRuleIntent(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Bash",
			RequestID: "req-1",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:        "confirm",
		Action:      "allow",
		RequestID:   "req-1",
		RulePattern: "git *",
		RuleScope:   "project",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.confirmCalls) != 1 {
		t.Fatalf("confirm calls = %d, want 1", len(backend.confirmCalls))
	}
	call := backend.confirmCalls[0]
	if call.ruleIntent == nil {
		t.Fatal("expected rule intent in confirm call")
	}
	if call.ruleIntent.Pattern != "git *" {
		t.Fatalf("rule pattern = %q, want %q", call.ruleIntent.Pattern, "git *")
	}
	if call.ruleIntent.Scope != int(permission.ScopeProject) {
		t.Fatalf("rule scope = %d, want %d(project)", call.ruleIntent.Scope, int(permission.ScopeProject))
	}
}

func TestHeadlessConfirmRejectsInvalidRuleScope(t *testing.T) {
	state := &headlessState{}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:        "confirm",
		Action:      "allow",
		RequestID:   "req-1",
		RulePattern: "git *",
		RuleScope:   "invalid-scope",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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

func TestHeadlessPendingQuestionClearedAfterQuestion(t *testing.T) {
	state := &headlessState{
		pendingQuestion: &protocol.QuestionRequestPayload{
			ToolName:  "Question",
			RequestID: "req-2",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "question",
		Answers:   []string{"yes"},
		RequestID: "req-2",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingQuestion != nil {
		t.Errorf("pendingQuestion should be nil after question command, got %+v", state.pendingQuestion)
	}
}

func TestHeadlessAutoDenyConfirmOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "do something else",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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

func TestHeadlessAutoCancelQuestionOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingQuestion: &protocol.QuestionRequestPayload{
			ToolName:  "Question",
			RequestID: "req-2",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "skip the question",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	state.mu.Lock()
	pq := state.pendingQuestion
	state.mu.Unlock()
	if pq != nil {
		t.Errorf("pendingQuestion should be nil after send command auto-cancelled it, got %+v", pq)
	}

	backend.mu.Lock()
	calls := backend.questionCalls
	msgs := backend.sentMessages
	backend.mu.Unlock()
	if len(calls) != 1 || !calls[0].cancelled || calls[0].requestID != "req-2" {
		t.Errorf("question calls = %v, want [cancelled req-2]", calls)
	}
	if len(msgs) != 1 || msgs[0] != "skip the question" {
		t.Errorf("sent messages = %v, want [skip the question]", msgs)
	}
}

func TestHeadlessAutoDenyBothConfirmAndQuestionOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
		pendingQuestion: &protocol.QuestionRequestPayload{
			ToolName:  "Question",
			RequestID: "req-2",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "new message",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	state.mu.Lock()
	pc := state.pendingConfirm
	pq := state.pendingQuestion
	state.mu.Unlock()
	if pc != nil {
		t.Errorf("pendingConfirm should be nil, got %+v", pc)
	}
	if pq != nil {
		t.Errorf("pendingQuestion should be nil, got %+v", pq)
	}

	backend.mu.Lock()
	cc := backend.confirmCalls
	qc := backend.questionCalls
	msgs := backend.sentMessages
	backend.mu.Unlock()
	if len(cc) != 1 || cc[0].action != "deny" {
		t.Errorf("confirm calls = %v, want [deny]", cc)
	}
	if len(qc) != 1 || !qc[0].cancelled {
		t.Errorf("question calls = %v, want [cancelled]", qc)
	}
	if len(msgs) != 1 || msgs[0] != "new message" {
		t.Errorf("sent messages = %v, want [new message]", msgs)
	}
}

func TestHeadlessNoAutoDenyWhenNoPendingConfirm(t *testing.T) {
	state := &headlessState{}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "hello",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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

func TestHeadlessPendingClearedAfterIdleEvent(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
		pendingQuestion: &protocol.QuestionRequestPayload{
			ToolName:  "Question",
			RequestID: "req-2",
		},
		lastError: "some error",
	}

	envs := filterHeadlessEvent(agent.IdleEvent{}, state)

	state.mu.Lock()
	pc := state.pendingConfirm
	pq := state.pendingQuestion
	le := state.lastError
	state.mu.Unlock()

	if pc != nil {
		t.Errorf("pendingConfirm should be nil after IdleEvent, got %+v", pc)
	}
	if pq != nil {
		t.Errorf("pendingQuestion should be nil after IdleEvent, got %+v", pq)
	}
	if le != "" {
		t.Errorf("lastError should be empty after IdleEvent, got %q", le)
	}
	if len(envs) == 0 {
		t.Fatal("IdleEvent should produce an envelope")
	}
}

func TestHeadlessLastErrorSetAfterErrorClearedAfterIdle(t *testing.T) {
	state := &headlessState{}

	filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("rate limit exceeded"), AgentID: ""}, state)

	state.mu.Lock()
	le := state.lastError
	state.mu.Unlock()

	if le != "rate limit exceeded" {
		t.Errorf("lastError after ErrorEvent = %q, want %q", le, "rate limit exceeded")
	}

	filterHeadlessEvent(agent.IdleEvent{}, state)

	state.mu.Lock()
	le = state.lastError
	state.mu.Unlock()

	if le != "" {
		t.Errorf("lastError after IdleEvent = %q, want empty", le)
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
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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

func TestHeadlessUnsupportedRemoteCommand(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"/model gpt-4", true},
		{"/new", false},
		{"/resume abc", false},
		{"/export", true},
		{"hello world", false},
		{"/help", false},
		{"", false},
		{"use /model in your code", false}, // /model is not the first word
	}

	for _, tt := range tests {
		got := isUnsupportedHeadlessCommand(tt.content)
		if got != tt.want {
			t.Errorf("isUnsupportedHeadlessCommand(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

func TestHeadlessIdleEventLastOutcome(t *testing.T) {
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

			envs := filterHeadlessEvent(agent.IdleEvent{}, state)
			if len(envs) == 0 {
				t.Fatal("IdleEvent should produce an envelope")
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
		AgentID:   "sub-1",
		Text:      "Task done",
		ToolCalls: 0,
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("AssistantMessageEvent from sub should produce an envelope")
	}
	payload := envs[0].Payload.(map[string]any)
	if payload["agent_id"] != "sub-1" {
		t.Errorf("agent_id = %q, want %q", payload["agent_id"], "sub-1")
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
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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

	// IdleEvent should pass through
	envs = filterHeadlessEvent(agent.IdleEvent{}, state)
	if len(envs) == 0 {
		t.Fatal("IdleEvent should pass through when subscribed")
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

	envs = filterHeadlessEvent(agent.IdleEvent{}, state)
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
		Events: []string{"notification"},
	}
	to := newTestOut()
	backend := &mockBackend{}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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
	if envs := filterHeadlessEvent(agent.IdleEvent{}, state); len(envs) != 0 {
		t.Fatalf("idle envs = %#v, want none for unknown-only subscription", envs)
	}
	if envs := filterHeadlessEvent(agent.AssistantMessageEvent{Text: "hello"}, state); len(envs) != 0 {
		t.Fatalf("assistant envs = %#v, want none for unknown-only subscription", envs)
	}
}

// ---------------------------------------------------------------------------
// Tests for event envelope payloads
// ---------------------------------------------------------------------------

func TestHeadlessConfirmRequestEventPayload(t *testing.T) {
	state := &headlessState{}

	ev := agent.ConfirmRequestEvent{
		ToolName:       "Bash",
		ArgsJSON:       `{"command":"rm -rf /"}`,
		RequestID:      "req-1",
		Timeout:        30 * time.Second,
		NeedsApproval:  []string{"a.go", "b/c.txt"},
		AlreadyAllowed: []string{"d.go"},
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

	if payload["tool_name"] != "Bash" {
		t.Errorf("tool_name = %v, want Bash", payload["tool_name"])
	}
	if payload["request_id"] != "req-1" {
		t.Errorf("request_id = %v, want req-1", payload["request_id"])
	}
	if payload["timeout_ms"] != float64(30000) {
		t.Errorf("timeout_ms = %v, want 30000", payload["timeout_ms"])
	}

	state.mu.Lock()
	pc := state.pendingConfirm
	state.mu.Unlock()

	if pc == nil {
		t.Fatal("pendingConfirm should be set")
	}
	if pc.ToolName != "Bash" {
		t.Errorf("pendingConfirm.ToolName = %q, want Bash", pc.ToolName)
	}
}

func TestHeadlessQuestionRequestEventPayload(t *testing.T) {
	state := &headlessState{}

	ev := agent.QuestionRequestEvent{
		ToolName:      "Question",
		Header:        "Task sub-1",
		Question:      "Continue?",
		Options:       []string{"yes", "no"},
		OptionDetails: []string{"Yes, proceed", "No, stop"},
		DefaultAnswer: "yes",
		Multiple:      false,
		RequestID:     "req-2",
		Timeout:       60 * time.Second,
	}

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

	if payload["tool_name"] != "Question" {
		t.Errorf("tool_name = %v, want Question", payload["tool_name"])
	}
	if payload["header"] != "Task sub-1" {
		t.Errorf("header = %v, want Task sub-1", payload["header"])
	}
	if payload["question"] != "Continue?" {
		t.Errorf("question = %v, want Continue?", payload["question"])
	}
	if payload["timeout_ms"] != float64(60000) {
		t.Errorf("timeout_ms = %v, want 60000", payload["timeout_ms"])
	}

	state.mu.Lock()
	pq := state.pendingQuestion
	state.mu.Unlock()

	if pq == nil {
		t.Fatal("pendingQuestion should be set")
	}
	if pq.ToolName != "Question" {
		t.Errorf("pendingQuestion.ToolName = %q, want Question", pq.ToolName)
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

func TestHeadlessSendCommandUnsupportedRejection(t *testing.T) {
	state := &headlessState{}

	tests := []struct {
		content string
		want    bool // true = should be rejected (error envelope emitted)
	}{
		{"/model gpt-4", true},
		{"/new session", false},
		{"/resume abc", false},
		{"/export", true},
		{"do something", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			to := newTestOut()
			backend := &mockBackend{}

			cmd := headlessCommand{Type: "send", Content: tt.content}
			handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

			items := to.drain()
			if tt.want {
				if len(items) == 0 {
					t.Error("expected error envelope for unsupported command")
				} else if items[0].Type != "error" {
					t.Errorf("envelope type = %q, want error", items[0].Type)
				}
			} else {
				for _, item := range items {
					if item.Type == "error" {
						t.Errorf("unexpected error envelope for supported command: %v", item)
					}
				}
				backend.mu.Lock()
				sent := len(backend.sentMessages)
				backend.mu.Unlock()
				if sent != 1 {
					t.Errorf("expected 1 message sent to backend, got %d", sent)
				}
			}
		})
	}
}

func TestHeadlessStatusCommand(t *testing.T) {
	state := &headlessState{
		busy:        true,
		phase:       "streaming",
		phaseDetail: "analyzing code",
		lastOutcome: "completed",
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Bash",
			RequestID: "req-1",
		},
		lastError: "",
		updatedAt: time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{Type: "status"}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	items := to.drain()
	if len(items) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(items))
	}
	env := items[0]
	if env.Type != "status_response" {
		t.Errorf("type = %q, want status_response", env.Type)
	}

	data, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["session_id"] != "test-session" {
		t.Errorf("session_id = %v, want test-session", payload["session_id"])
	}
	if payload["busy"] != true {
		t.Errorf("busy = %v, want true", payload["busy"])
	}
	if payload["phase"] != "streaming" {
		t.Errorf("phase = %v, want streaming", payload["phase"])
	}
	if payload["last_outcome"] != "completed" {
		t.Errorf("last_outcome = %v, want completed", payload["last_outcome"])
	}
}

func TestHeadlessStatusResponseLastOutcome(t *testing.T) {
	tests := []struct {
		name           string
		lastOutcome    string
		pendingOutcome string
		want           string
	}{
		{
			name:        "completed",
			lastOutcome: "completed",
			want:        "completed",
		},
		{
			name:        "error",
			lastOutcome: "error",
			want:        "error",
		},
		{
			name:        "cancelled",
			lastOutcome: "cancelled",
			want:        "cancelled",
		},
		{
			name:        "empty (no idle yet)",
			lastOutcome: "",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &headlessState{
				lastOutcome: tt.lastOutcome,
			}
			to := newTestOut()
			backend := &mockBackend{}

			cmd := headlessCommand{Type: "status"}
			handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

			items := to.drain()
			if len(items) != 1 {
				t.Fatalf("expected 1 envelope, got %d", len(items))
			}

			data, err := json.Marshal(items[0].Payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}

			got, _ := payload["last_outcome"].(string)
			if got != tt.want {
				t.Errorf("last_outcome = %q, want %q", got, tt.want)
			}

			// last_outcome in status_response must equal state.lastOutcome
			if got != state.lastOutcome {
				t.Errorf("status last_outcome = %q != state.lastOutcome = %q", got, state.lastOutcome)
			}
		})
	}
}

func TestHeadlessConfirmMismatchedRequestID(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &protocol.ConfirmRequestPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "confirm",
		Action:    "allow",
		RequestID: "req-wrong",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	state.mu.Lock()
	pc := state.pendingConfirm
	state.mu.Unlock()
	if pc == nil {
		t.Error("pendingConfirm should NOT be nil when request_id doesn't match")
	}
}

func TestHeadlessQuestionMismatchedRequestID(t *testing.T) {
	state := &headlessState{
		pendingQuestion: &protocol.QuestionRequestPayload{
			ToolName:  "Question",
			RequestID: "req-2",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:      "question",
		Answers:   []string{"yes"},
		RequestID: "req-wrong",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

	state.mu.Lock()
	pq := state.pendingQuestion
	state.mu.Unlock()
	if pq == nil {
		t.Error("pendingQuestion should NOT be nil when request_id doesn't match")
	}
}

func TestHeadlessToolResultEvent(t *testing.T) {
	state := &headlessState{}

	ev := agent.ToolResultEvent{
		CallID:   "call-1",
		Name:     "Bash",
		ArgsJSON: `{"command":"ls -la"}`,
		Result:   "file1.go\nfile2.go",
		Status:   agent.ToolResultStatusSuccess,
		AgentID:  "",
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("ToolResultEvent should produce an envelope")
	}
	if envs[0].Type != "tool_result" {
		t.Errorf("type = %q, want %q", envs[0].Type, "tool_result")
	}

	payload, ok := envs[0].Payload.(map[string]string)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]string", envs[0].Payload)
	}
	if payload["call_id"] != "call-1" {
		t.Errorf("call_id = %q, want %q", payload["call_id"], "call-1")
	}
	if payload["name"] != "Bash" {
		t.Errorf("name = %q, want %q", payload["name"], "Bash")
	}
	if payload["status"] != "success" {
		t.Errorf("status = %q, want %q", payload["status"], "success")
	}

	// Verify payload does NOT contain result or args_json (too large for IM notifications)
	if _, exists := payload["result"]; exists {
		t.Error("payload should not contain 'result' key")
	}
	if _, exists := payload["args_json"]; exists {
		t.Error("payload should not contain 'args_json' key")
	}
}

func TestHeadlessEventEnvelopeTypes(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{"confirm_request": true, "question_request": true, "error": true, "idle": true}}

	confirmEnvs := filterHeadlessEvent(agent.ConfirmRequestEvent{ToolName: "Edit", RequestID: "req-1"}, state)
	if len(confirmEnvs) != 1 || confirmEnvs[0].Type != "confirm_request" {
		t.Fatalf("confirm envs = %#v, want confirm_request only", confirmEnvs)
	}

	questionEnvs := filterHeadlessEvent(agent.QuestionRequestEvent{ToolName: "Question", Header: "h", Question: "q", RequestID: "req-2"}, state)
	if len(questionEnvs) != 1 || questionEnvs[0].Type != "question_request" {
		t.Fatalf("question envs = %#v, want question_request only", questionEnvs)
	}

	_ = filterHeadlessEvent(agent.AgentActivityEvent{Type: agent.ActivityStreaming, Detail: "working"}, state)
	errorEnvs := filterHeadlessEvent(agent.ErrorEvent{Err: errors.New("blocked by missing input")}, state)
	if len(errorEnvs) != 1 || errorEnvs[0].Type != "error" {
		t.Fatalf("error envs = %#v, want error only", errorEnvs)
	}

	state = &headlessState{subscriptions: map[string]bool{"idle": true}, pendingOutcome: "completed"}
	idleEnvs := filterHeadlessEvent(agent.IdleEvent{}, state)
	if len(idleEnvs) != 1 || idleEnvs[0].Type != "idle" {
		t.Fatalf("idle envs = %#v, want idle only", idleEnvs)
	}

	// Idle with error outcome is also represented by a single idle envelope.
	state = &headlessState{subscriptions: map[string]bool{"idle": true}, pendingOutcome: "error", lastError: "something failed"}
	idleErrorEnvs := filterHeadlessEvent(agent.IdleEvent{}, state)
	if len(idleErrorEnvs) != 1 || idleErrorEnvs[0].Type != "idle" {
		t.Fatalf("idle error envs = %#v, want idle only", idleErrorEnvs)
	}
}

func TestHeadlessTodosUpdatedEvent(t *testing.T) {
	state := &headlessState{}

	ev := agent.TodosUpdatedEvent{
		Todos: []tools.TodoItem{
			{ID: "1", Content: "Implement feature", Status: "in_progress", ActiveForm: "editing main.go"},
			{ID: "2", Content: "Write tests", Status: "pending"},
		},
	}

	envs := filterHeadlessEvent(ev, state)

	if len(envs) == 0 {
		t.Fatal("TodosUpdatedEvent should produce an envelope")
	}
	if envs[0].Type != "todos" {
		t.Errorf("type = %q, want %q", envs[0].Type, "todos")
	}

	payload, ok := envs[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", envs[0].Payload)
	}

	todosRaw, ok := payload["todos"]
	if !ok {
		t.Fatal("payload should contain 'todos' key")
	}

	data, err := json.Marshal(todosRaw)
	if err != nil {
		t.Fatalf("marshal todos: %v", err)
	}

	var todos []tools.TodoItem
	if err := json.Unmarshal(data, &todos); err != nil {
		t.Fatalf("unmarshal todos: %v", err)
	}

	if len(todos) != 2 {
		t.Fatalf("len(todos) = %d, want 2", len(todos))
	}
	if todos[0].ID != "1" {
		t.Errorf("todos[0].ID = %q, want %q", todos[0].ID, "1")
	}
	if todos[0].Content != "Implement feature" {
		t.Errorf("todos[0].Content = %q, want %q", todos[0].Content, "Implement feature")
	}
	if todos[0].Status != "in_progress" {
		t.Errorf("todos[0].Status = %q, want %q", todos[0].Status, "in_progress")
	}
	if todos[1].ID != "2" {
		t.Errorf("todos[1].ID = %q, want %q", todos[1].ID, "2")
	}
}

func TestHeadlessToolResultEventStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status agent.ToolResultStatus
		want   string
	}{
		{
			name:   "success",
			status: agent.ToolResultStatusSuccess,
			want:   "success",
		},
		{
			name:   "error",
			status: agent.ToolResultStatusError,
			want:   "error",
		},
		{
			name:   "cancelled",
			status: agent.ToolResultStatusCancelled,
			want:   "cancelled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &headlessState{}

			ev := agent.ToolResultEvent{
				CallID:  "call-1",
				Name:    "Bash",
				Status:  tt.status,
				AgentID: "",
			}

			envs := filterHeadlessEvent(ev, state)

			if len(envs) == 0 {
				t.Fatal("ToolResultEvent should produce an envelope")
			}

			payload, ok := envs[0].Payload.(map[string]string)
			if !ok {
				t.Fatalf("payload type = %T, want map[string]string", envs[0].Payload)
			}
			if payload["status"] != tt.want {
				t.Errorf("status = %q, want %q", payload["status"], tt.want)
			}
		})
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
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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
