package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
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
	tb  testing.TB
	buf *synchronizedBuffer
	out *stdoutWriter
}

func newTestOut(tb testing.TB) *testOut {
	tb.Helper()
	buf := &synchronizedBuffer{}
	ctx := context.Background()
	out := newStdoutWriter(ctx, buf)
	go out.run()
	tb.Cleanup(func() {
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		if !out.closeUntil(deadline.C) {
			tb.Error("stdout writer did not stop")
		}
	})
	return &testOut{
		tb:  tb,
		buf: buf,
		out: out,
	}
}

// writer returns the *stdoutWriter for passing to handleHeadlessCommand.
func (t *testOut) writer() *stdoutWriter {
	return t.out
}

// drain waits for all previously queued output and returns its parsed envelopes.
func (t *testOut) drain() []headlessEnvelope {
	t.tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !t.out.flush(ctx) {
		t.tb.Fatal("stdout writer did not flush")
	}
	var result []headlessEnvelope
	for line := range bytes.SplitSeq(t.buf.snapshotAndReset(), []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var env headlessEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.tb.Fatalf("decode stdout envelope: %v", err)
		}
		result = append(result, env)
	}
	return result
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
	handoffCalls  []handoffCall

	handoffOptions    []agent.HandoffAgentOption
	handoffOptionsSet bool

	availableRoles  []string
	currentRole     string
	switchRoleErr   error
	switchRoleCalls []string

	// resolveQuestionFail makes ApplyQuestionOperation reject the answer.
	resolveQuestionFail bool

	// resolveQuestionTerminal reports the outcome when the deadline wins.
	resolveQuestionTerminal string

	// callOrder records mutating backend calls in the order they happened, so
	// tests can assert sequencing of accepted input and other operations.
	callOrder []string
}

type executePlanCall struct {
	planPath  string
	agentName string
}

type handoffCall struct {
	requestID  string
	action     string
	agentName  string
	denyReason string
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
	reason    string
	requestID string
}

func (m *mockBackend) SendUserMessage(content string) {
	m.mu.Lock()
	m.sentMessages = append(m.sentMessages, content)
	m.callOrder = append(m.callOrder, "send:"+content)
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

func (m *mockBackend) ApplyQuestionOperation(_ context.Context, op agent.QuestionOperation) (agent.QuestionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch op.Operation {
	case agent.QuestionOpAnswer, agent.QuestionOpInteract, agent.QuestionOpPresented, agent.QuestionOpRevise, agent.QuestionOpWithdraw, agent.QuestionOpReplace, agent.QuestionOpNewTask:
	default:
		return agent.QuestionReceipt{Error: "invalid operation"}, nil
	}
	reason := tools.QuestionOutcomeAnswered
	if op.Operation == agent.QuestionOpWithdraw {
		reason = tools.QuestionOutcomeCancelled
	}
	m.questionCalls = append(m.questionCalls, questionCall{answers: op.Answers, reason: reason, requestID: op.QuestionID})
	if m.resolveQuestionFail {
		return agent.QuestionReceipt{Error: "rejected"}, nil
	}
	if m.resolveQuestionTerminal != "" {
		return agent.QuestionReceipt{Status: m.resolveQuestionTerminal, Error: "deadline decided: " + m.resolveQuestionTerminal}, nil
	}
	return agent.QuestionReceipt{Accepted: true, Status: reason}, nil
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

func (m *mockBackend) AvailableRoles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.availableRoles != nil {
		return append([]string(nil), m.availableRoles...)
	}
	return []string{"builder", "planner"}
}

func (m *mockBackend) CurrentRole() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentRole != "" {
		return m.currentRole
	}
	return "builder"
}

func (m *mockBackend) SwitchRole(role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.switchRoleCalls = append(m.switchRoleCalls, role)
	if m.switchRoleErr != nil {
		return m.switchRoleErr
	}
	m.currentRole = role
	return nil
}

func (m *mockBackend) HandoffAgentOptions() []agent.HandoffAgentOption {
	if m.handoffOptionsSet {
		return m.handoffOptions
	}
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

func (m *mockBackend) ResolveHandoff(requestID, action, agentName, denyReason string) {
	m.mu.Lock()
	m.handoffCalls = append(m.handoffCalls, handoffCall{requestID: requestID, action: action, agentName: agentName, denyReason: denyReason})
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

// headlessErrorMessage extracts the "message" field from an error envelope.
// drain() re-parses envelopes from JSONL, so payloads arrive as map[string]any.
func headlessErrorMessage(t *testing.T, env headlessEnvelope) string {
	t.Helper()
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v, want a decoded map", env.Payload)
	}
	message, _ := payload["message"].(string)
	return message
}
