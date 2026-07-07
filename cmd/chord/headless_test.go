package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
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
//
// stdoutWriter writes asynchronously in another goroutine. We may need multiple
// snapshot iterations before we observe the flush marker; accumulate envelopes
// across iterations so we don't lose messages when the flush marker arrives in
// a later snapshot.
func (t *testOut) drain() []headlessEnvelope {
	flushAck := make(chan struct{})
	if !t.out.emit(map[string]any{"type": "__test_flush__", "payload": map[string]any{"ack": true}}) {
		return nil
	}
	deadline := time.Now().Add(2 * time.Second)
	var result []headlessEnvelope
	for {
		lines := bytes.SplitSeq(t.buf.snapshotAndReset(), []byte{'\n'})
		for line := range lines {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var env headlessEnvelope
			if err := json.Unmarshal(line, &env); err != nil {
				continue
			}
			if env.Type == "__test_flush__" {
				select {
				case <-flushAck:
					// already closed
				default:
					close(flushAck)
				}
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
	executeCalls  []executePlanCall
	continueCalls int
}

type executePlanCall struct {
	planPath  string
	agentName string
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

func (m *mockBackend) ModelsStatusText() string { return "Model pool: thinking\n" }

func (m *mockBackend) SetCurrentModelPool(pool string) error {
	m.SendUserMessage("set-current-model-pool:" + pool)
	return nil
}

func (m *mockBackend) SetAgentModelPool(agentName, pool string) error {
	m.SendUserMessage("set-agent:" + agentName + ":" + pool)
	return nil
}

func (m *mockBackend) HandoffAgentOptions() []agent.HandoffAgentOption {
	return []agent.HandoffAgentOption{
		{Name: "builder", Default: true, ModelPools: []string{"fast", "smart"}, CurrentModelPool: "fast"},
		{Name: "reviewer", ModelPools: []string{"smart"}, CurrentModelPool: "smart"},
	}
}

func (m *mockBackend) ExecutePlan(planPath, agentName string) {
	m.mu.Lock()
	m.executeCalls = append(m.executeCalls, executePlanCall{planPath: planPath, agentName: agentName})
	m.mu.Unlock()
}

func (m *mockBackend) AppendContextMessage(msg message.Message) {
	m.SendUserMessage(msg.Content)
}

func (m *mockBackend) ContinueFromContext() {
	m.mu.Lock()
	m.continueCalls++
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

func TestHeadlessSendCommandRejectsBareMCP(t *testing.T) {
	to := newTestOut()
	backend := &mockBackend{}
	state := &headlessState{}

	hcmd := headlessCommand{Type: "send", Content: "/mcp"}
	handleHeadlessCommand(hcmd, backend, state, to.writer(), "sess")

	envs := to.drain()
	env := findHeadlessEnvelopeValue(envs, "error")
	if env == nil {
		t.Fatalf("expected error envelope, got %v", envs)
	}
	payload, _ := env.Payload.(map[string]any)
	if payload == nil {
		t.Fatalf("unexpected error payload: %#v", env.Payload)
	}
	msg, _ := payload["message"].(string)
	if msg == "" {
		t.Fatalf("unexpected error payload: %#v", env.Payload)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.sentMessages) != 0 {
		t.Fatalf("SendUserMessage should not be called, got %v", backend.sentMessages)
	}
}

func TestHeadlessHandoffEventAndCommand(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\nDo the work."), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &headlessState{subscriptions: map[string]bool{"handoff_request": true}}
	backend := &mockBackend{}

	envs := filterHeadlessEvent(agent.HandoffEvent{PlanPath: planPath}, state, backend)
	env := findHeadlessEnvelope(envs, "handoff_request")
	if env == nil {
		t.Fatalf("handoff_request envelope not emitted: %v", envs)
	}
	payload, ok := env.Payload.(*headlessHandoffPayload)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload.PlanPath != planPath || payload.PlanText != "# Plan\n\nDo the work." {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if len(payload.Agents) != 2 || payload.Agents[0].Name != "builder" || payload.Agents[0].CurrentModelPool != "fast" {
		t.Fatalf("unexpected agents: %+v", payload.Agents)
	}

	to := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: payload.RequestID, Action: "accept", Agent: "reviewer", Pool: "smart"}, backend, state, to.writer(), "sess")

	state.mu.Lock()
	pending := state.pendingHandoff
	state.mu.Unlock()
	if pending != nil {
		t.Fatalf("pending handoff not cleared: %+v", pending)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.executeCalls) != 1 || backend.executeCalls[0].planPath != planPath || backend.executeCalls[0].agentName != "reviewer" {
		t.Fatalf("execute calls = %+v", backend.executeCalls)
	}
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "set-agent:reviewer:smart" {
		t.Fatalf("sent messages = %+v", backend.sentMessages)
	}
}

func TestHeadlessHandoffDenyContinuesFromContext(t *testing.T) {
	state := &headlessState{pendingHandoff: &headlessHandoffPayload{RequestID: "handoff-1", PlanPath: "/tmp/plan.md"}}
	backend := &mockBackend{}
	to := newTestOut()

	handleHeadlessCommand(headlessCommand{Type: "handoff", RequestID: "handoff-1", Action: "deny", DenyReason: "needs more detail"}, backend, state, to.writer(), "sess")

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.continueCalls != 1 {
		t.Fatalf("continue calls = %d, want 1", backend.continueCalls)
	}
	if len(backend.sentMessages) != 1 || !strings.Contains(backend.sentMessages[0], "needs more detail") {
		t.Fatalf("sent messages = %+v", backend.sentMessages)
	}
}

func TestHeadlessPendingConfirmClearedAfterConfirm(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
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
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Shell",
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
	if !reflect.DeepEqual(call.ruleIntent.Patterns, []string{"git *"}) {
		t.Fatalf("rule patterns = %#v, want %#v", call.ruleIntent.Patterns, []string{"git *"})
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
		pendingQuestion: &headlessQuestionPayload{
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
		pendingConfirm: &headlessConfirmPayload{
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
		pendingQuestion: &headlessQuestionPayload{
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

func TestHeadlessAutoCancelHandoffOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{
		Type:    "send",
		Content: "revise the plan instead",
	}
	handleHeadlessCommand(cmd, backend, state, to.writer(), "test-session")

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
func TestHeadlessAutoDenyBothConfirmAndQuestionOnUserMessage(t *testing.T) {
	state := &headlessState{
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
		pendingQuestion: &headlessQuestionPayload{
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
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Delete",
			RequestID: "req-1",
		},
		pendingQuestion: &headlessQuestionPayload{
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

func TestHeadlessBareModelsSendMapsToStatus(t *testing.T) {
	state := &headlessState{}
	to := newTestOut()
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "send", Content: "/models"}, backend, state, to.writer(), "test-session")

	backend.mu.Lock()
	msgs := append([]string(nil), backend.sentMessages...)
	backend.mu.Unlock()
	if len(msgs) != 1 || msgs[0] != "/models status" {
		t.Fatalf("sent messages = %v, want [/models status]", msgs)
	}
}

func TestHeadlessModelsCommandStatus(t *testing.T) {
	state := &headlessState{}
	to := newTestOut()
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "models", Action: "status"}, backend, state, to.writer(), "test-session")

	env := findHeadlessEnvelopeValue(to.drain(), "models_response")
	if env == nil {
		t.Fatal("models_response missing")
	}
	payload := env.Payload.(map[string]any)
	if payload["ok"] != true {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	if payload["status"] != "Model pool: thinking\n" {
		t.Fatalf("status = %q", payload["status"])
	}
}

func TestHeadlessModelsCommandSetCurrentModelPool(t *testing.T) {
	state := &headlessState{}
	to := newTestOut()
	backend := &mockBackend{}

	handleHeadlessCommand(headlessCommand{Type: "models", Action: "set_current_model_pool", Pool: "fast"}, backend, state, to.writer(), "test-session")

	backend.mu.Lock()
	msgs := append([]string(nil), backend.sentMessages...)
	backend.mu.Unlock()
	want := []string{"set-current-model-pool:fast"}
	if len(msgs) != len(want) {
		t.Fatalf("sent messages = %v, want %v", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Fatalf("sent messages = %v, want %v", msgs, want)
		}
	}

	responses := 0
	for _, env := range to.drain() {
		if env.Type == "models_response" {
			responses++
			payload := env.Payload.(map[string]any)
			if payload["ok"] != true {
				t.Fatalf("models_response ok = %v, want true", payload["ok"])
			}
		}
	}
	if responses != 1 {
		t.Fatalf("models_response count = %d, want 1", responses)
	}
}

func TestHeadlessUnsupportedRemoteCommand(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"/models", false},
		{"/models fast", false},
		{"/models status", false},
		{"/models --agent reviewer strong", false},
		{"/new", false},
		{"/resume abc", false},
		{"/export", true},
		{"hello world", false},
		{"/help", false},
		{"", false},
		{"use /models in your code", false}, // /model is not the first word
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

func TestHeadlessLocalShellCommandEmitsResult(t *testing.T) {
	to := newTestOut()
	backend := &mockBackend{}
	state := &headlessState{}

	handleHeadlessCommand(headlessCommand{Type: "local_shell", Command: "printf chord-local-shell"}, backend, state, to.writer(), "test-session")

	items := to.drain()
	env := findHeadlessEnvelopeValue(items, "local_shell_result")
	if env == nil {
		t.Fatalf("missing local_shell_result in %#v", items)
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["command"] != "printf chord-local-shell" {
		t.Fatalf("command = %q", payload["command"])
	}
	if payload["output"] != "chord-local-shell" {
		t.Fatalf("output = %q", payload["output"])
	}
	if payload["failed"] != false {
		t.Fatalf("failed = %v", payload["failed"])
	}
}

func TestHeadlessLocalShellEmptyCommandEmitsFailure(t *testing.T) {
	to := newTestOut()
	backend := &mockBackend{}
	state := &headlessState{}

	handleHeadlessCommand(headlessCommand{Type: "local_shell", Command: "   "}, backend, state, to.writer(), "test-session")

	items := to.drain()
	env := findHeadlessEnvelopeValue(items, "local_shell_result")
	if env == nil {
		t.Fatalf("missing local_shell_result in %#v", items)
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["failed"] != true {
		t.Fatalf("failed = %v", payload["failed"])
	}
	if !strings.Contains(payload["error"].(string), "empty local shell command") {
		t.Fatalf("error = %q", payload["error"])
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
		Events: []string{"nonexistent_event"},
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
		ToolName:            "Shell",
		ArgsJSON:            `{"command":"rm -rf /"}`,
		RequestID:           "req-1",
		Timeout:             30 * time.Second,
		NeedsApproval:       []string{"a.go", "b/c.txt"},
		AlreadyAllowed:      []string{"d.go"},
		NeedsApprovalRules:  []string{"ask Shell(rm*)"},
		AlreadyAllowedRules: []string{"allow Shell(ls*)"},
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
	if payload["timeout_ms"] != float64(30000) {
		t.Errorf("timeout_ms = %v, want 30000", payload["timeout_ms"])
	}
	if got := stringSliceFromAny(t, payload["needs_approval_rules"]); !reflect.DeepEqual(got, ev.NeedsApprovalRules) {
		t.Errorf("needs_approval_rules = %#v, want %#v", got, ev.NeedsApprovalRules)
	}
	if got := stringSliceFromAny(t, payload["already_allowed_rules"]); !reflect.DeepEqual(got, ev.AlreadyAllowedRules) {
		t.Errorf("already_allowed_rules = %#v, want %#v", got, ev.AlreadyAllowedRules)
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
	if !reflect.DeepEqual(pc.NeedsApprovalRules, ev.NeedsApprovalRules) {
		t.Errorf("pendingConfirm.NeedsApprovalRules = %#v, want %#v", pc.NeedsApprovalRules, ev.NeedsApprovalRules)
	}
	if !reflect.DeepEqual(pc.AlreadyAllowedRules, ev.AlreadyAllowedRules) {
		t.Errorf("pendingConfirm.AlreadyAllowedRules = %#v, want %#v", pc.AlreadyAllowedRules, ev.AlreadyAllowedRules)
	}
}

func stringSliceFromAny(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("value = %#v (%T), want []any", v, v)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("item = %#v (%T), want string", item, item)
		}
		out = append(out, s)
	}
	return out
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
		{"/models", false},
		{"/models fast", false},
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
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Shell",
			RequestID: "req-1",
		},
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
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
	pendingHandoff, ok := payload["pending_handoff"].(map[string]any)
	if !ok {
		t.Fatalf("pending_handoff = %#v, want object", payload["pending_handoff"])
	}
	if pendingHandoff["request_id"] != "handoff-1" || pendingHandoff["plan_path"] != "/tmp/plan.md" {
		t.Errorf("pending_handoff = %#v, want handoff-1 /tmp/plan.md", pendingHandoff)
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
		pendingConfirm: &headlessConfirmPayload{
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
		pendingQuestion: &headlessQuestionPayload{
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

func TestHeadlessToolResultEventIsNotForwarded(t *testing.T) {
	state := &headlessState{}

	envs := filterHeadlessEvent(agent.ToolResultEvent{
		CallID: "call-1",
		Name:   "Shell",
		Status: agent.ToolResultStatusSuccess,
	}, state)

	if len(envs) != 0 {
		t.Fatalf("envs = %+v, want none", envs)
	}
}

func TestHeadlessDoneToolResultEmitsDoneCompletion(t *testing.T) {
	state := &headlessState{}
	handleHeadlessCommand(headlessCommand{Type: "subscribe", Events: []string{"done_completion"}}, &mockBackend{}, state, newTestOut().writer(), "test-session")

	envs := filterHeadlessEvent(agent.ToolResultEvent{
		CallID:     "call-done",
		Name:       "Done",
		ArgsJSON:   `{"reason":"ready","report":"from args"}`,
		Status:     agent.ToolResultStatusSuccess,
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
	handleHeadlessCommand(headlessCommand{Type: "subscribe", Events: []string{"done_completion"}}, &mockBackend{}, state, newTestOut().writer(), "test-session")

	envs := filterHeadlessEvent(agent.ToolResultEvent{
		CallID: "call-done",
		Name:   "Done",
		Status: agent.ToolResultStatusSuccess,
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

type fakeHeadlessRuntime struct {
	events  chan agent.AgentEvent
	backend headlessBackend
	closed  bool
	onClose func()
}

func (f *fakeHeadlessRuntime) Close() {
	if f.onClose != nil {
		f.onClose()
	}
	f.closed = true
}
func (f *fakeHeadlessRuntime) Events() <-chan agent.AgentEvent { return f.events }
func (f *fakeHeadlessRuntime) Backend() headlessBackend        { return f.backend }

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func decodeHeadlessJSONLines(t *testing.T, data []byte) []headlessEnvelope {
	t.Helper()
	var envs []headlessEnvelope
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var env headlessEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		envs = append(envs, env)
	}
	return envs
}

func TestStdoutWriterCloseRejectsEmitAfterClose(t *testing.T) {
	var buf bytes.Buffer
	out := newStdoutWriter(context.Background(), &buf)
	go out.run()
	if !out.emit(headlessEnvelope{Type: "ready"}) {
		t.Fatal("initial emit returned false")
	}
	out.close()
	if out.emit(headlessEnvelope{Type: "late"}) {
		t.Fatal("emit after close returned true")
	}
	envs := decodeHeadlessJSONLines(t, buf.Bytes())
	if len(envs) != 1 || envs[0].Type != "ready" {
		t.Fatalf("envs = %+v, want one ready envelope", envs)
	}
}

func TestRuntimeHeadlessAdapterNilRuntimeIsSafe(t *testing.T) {
	adapter := runtimeHeadlessAdapter{}
	adapter.Close()
	if backend := adapter.Backend(); backend != nil {
		t.Fatalf("Backend() = %#v, want nil", backend)
	}
	events := adapter.Events()
	if events == nil {
		t.Fatal("Events() returned nil channel")
	}
	if _, ok := <-events; ok {
		t.Fatal("nil runtime Events() channel should be closed")
	}
}

func TestRuntimeHeadlessAdapterUsesRuntimeAgent(t *testing.T) {
	ac := newTestAppContext(t)
	rt := &Runtime{Agent: ac.MainAgent}
	adapter := runtimeHeadlessAdapter{rt: rt}
	if got := adapter.Backend(); got != ac.MainAgent {
		t.Fatalf("Backend() = %#v, want main agent", got)
	}
	if got := adapter.Events(); got != ac.MainAgent.Events() {
		t.Fatal("Events() did not return main agent event channel")
	}
	adapter.Close()
}

func TestDefaultHeadlessRunDeps(t *testing.T) {
	deps := defaultHeadlessRunDeps()
	if deps.initApp == nil {
		t.Fatal("default initApp is nil")
	}
	if deps.createRuntime == nil {
		t.Fatal("default createRuntime is nil")
	}
	if deps.stdin != os.Stdin {
		t.Fatal("default stdin should be os.Stdin")
	}
	if deps.stdout != os.Stdout {
		t.Fatal("default stdout should be os.Stdout")
	}
	if !deps.watchParent {
		t.Fatal("default parent watcher should be enabled")
	}
	if deps.parentCheckInterval != time.Second {
		t.Fatalf("parentCheckInterval = %v, want %v", deps.parentCheckInterval, time.Second)
	}
	if deps.getppid == nil || deps.getppid() <= 0 {
		t.Fatal("default getppid should return a process id")
	}
}

func TestDefaultHeadlessRunDepsCreateRuntimePropagatesRuntimeError(t *testing.T) {
	deps := defaultHeadlessRunDeps()
	rt, err := deps.createRuntime(&AppContext{})
	if err == nil || rt != nil {
		t.Fatalf("createRuntime empty app context = (%#v, %v), want nil runtime and error", rt, err)
	}
	if !strings.Contains(err.Error(), "runtime requires an initialized main agent") {
		t.Fatalf("unexpected createRuntime error: %v", err)
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
		{name: "empty fallback", options: []agent.HandoffAgentOption{{Name: " "}}, want: "builder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultHandoffAgent(tt.options); got != tt.want {
				t.Fatalf("defaultHandoffAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunHeadlessWithDepsEmitsReadyAndExitsOnStdinClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-abc")}
	events := make(chan agent.AgentEvent)
	close(events)
	rt := &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return rt, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	if !rt.closed {
		t.Fatal("runtime was not closed")
	}
	envs := decodeHeadlessJSONLines(t, stdout.Bytes())
	ready := findHeadlessEnvelopeValue(envs, "ready")
	if ready == nil {
		t.Fatalf("ready envelope not found in %+v", envs)
	}
	payload, ok := ready.Payload.(map[string]any)
	if !ok || payload["session_id"] != "session-abc" {
		t.Fatalf("ready payload = %#v, want session_id session-abc", ready.Payload)
	}
}

func TestRunHeadlessWithDepsReportsScannerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-scan")}
	events := make(chan agent.AgentEvent)
	close(events)
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}, nil
		},
		stdin:       failingReader{err: errors.New("scan boom")},
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	envs := decodeHeadlessJSONLines(t, stdout.Bytes())
	if findHeadlessEnvelopeValue(envs, "ready") == nil {
		t.Fatalf("ready envelope not found in %+v", envs)
	}
	errEnv := findHeadlessEnvelopeValue(envs, "error")
	if errEnv == nil {
		t.Fatalf("error envelope not found in %+v", envs)
	}
	payload, _ := errEnv.Payload.(map[string]any)
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "stdin read error: scan boom") {
		t.Fatalf("error payload = %#v", errEnv.Payload)
	}
}

func TestReadHeadlessStdinLinesReportsTooLongAndContinues(t *testing.T) {
	input := strings.Repeat("x", headlessStdinMaxLineBytes+1) + "\n{}\n"
	lines := make(chan headlessStdinLine, 4)
	readHeadlessStdinLines(context.Background(), strings.NewReader(input), lines)

	first, ok := <-lines
	if !ok {
		t.Fatal("expected too-long error")
	}
	if first.code != "stdin_line_too_long" || first.err == nil || first.fatal {
		t.Fatalf("first line = %+v, want recoverable stdin_line_too_long", first)
	}
	second, ok := <-lines
	if !ok {
		t.Fatal("expected reader to continue after too-long line")
	}
	if string(second.line) != "{}" || second.err != nil {
		t.Fatalf("second line = %+v, want valid JSON line", second)
	}
}

func TestHeadlessParentWatcherCancelsOnParentChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel}
	calls := make(chan int, 4)
	getppid := func() int {
		calls <- 42
		return 42
	}
	startHeadlessParentWatcher(ac, 7, time.Millisecond, getppid)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent watcher did not cancel context after parent changed")
	}
}

func TestRunHeadlessWithDepsClosesRuntimeBeforeDeferredAppClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-close-order")}
	closeOrder := make([]string, 0, 2)
	origCancel := ac.Cancel
	ac.Cancel = func() {
		closeOrder = append(closeOrder, "app")
		origCancel()
	}
	// AppContext.Close is a method, so verify ordering by observing runtime close
	// before context cancellation triggered by deferred ac.Close().
	events := make(chan agent.AgentEvent)
	close(events)
	rt := &fakeHeadlessRuntime{
		events:  events,
		backend: &mockBackend{},
		onClose: func() { closeOrder = append(closeOrder, "runtime") },
	}
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return rt, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	if len(closeOrder) < 3 {
		t.Fatalf("close order = %#v, want stdin-cancel, runtime close, final app close", closeOrder)
	}
	if closeOrder[len(closeOrder)-2] != "runtime" || closeOrder[len(closeOrder)-1] != "app" {
		t.Fatalf("close order = %#v, want runtime immediately before final app close", closeOrder)
	}
}

func TestRunHeadlessWithDepsReportsRuntimeInitFailureAndClosesApp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-init-fail")}
	closed := make(chan struct{}, 1)
	origCancel := ac.Cancel
	ac.Cancel = func() {
		select {
		case closed <- struct{}{}:
		default:
		}
		origCancel()
	}

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return nil, errors.New("runtime boom")
		},
		stdin:       strings.NewReader(""),
		stdout:      &bytes.Buffer{},
		watchParent: false,
	})
	if err == nil || !strings.Contains(err.Error(), "runtime boom") {
		t.Fatalf("runHeadlessWithDeps err = %v, want runtime boom", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("app context close/cancel was not triggered on runtime init failure")
	}
}

func TestRunHeadlessWithDepsParentWatcherIsolationDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ac := &AppContext{Ctx: ctx, Cancel: cancel, SessionDir: filepath.Join(t.TempDir(), "session-no-watch")}
	events := make(chan agent.AgentEvent)
	close(events)
	var getppidCalls int
	var stdout bytes.Buffer

	err := runHeadlessWithDeps(headlessRunDeps{
		initApp: func(bool, string, sessionStartupOptions) (*AppContext, error) { return ac, nil },
		createRuntime: func(*AppContext) (headlessRuntime, error) {
			return &fakeHeadlessRuntime{events: events, backend: &mockBackend{}}, nil
		},
		stdin:       strings.NewReader(""),
		stdout:      &stdout,
		watchParent: false,
		getppid: func() int {
			getppidCalls++
			return 1
		},
	})
	if err != nil {
		t.Fatalf("runHeadlessWithDeps: %v", err)
	}
	if getppidCalls != 0 {
		t.Fatalf("getppid calls = %d, want 0 when parent watcher disabled", getppidCalls)
	}
}
