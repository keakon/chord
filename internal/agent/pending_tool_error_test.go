package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type toolOutputErrorStub struct {
	name   string
	result string
	err    error
}

func (t toolOutputErrorStub) Name() string { return t.name }

func (t toolOutputErrorStub) Description() string { return "test stub" }

func (t toolOutputErrorStub) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": true,
	}
}

func (t toolOutputErrorStub) Execute(context.Context, json.RawMessage) (string, error) {
	return t.result, t.err
}

func (t toolOutputErrorStub) IsReadOnly() bool { return true }

type blockingToolStub struct {
	name    string
	started chan struct{}
	release chan struct{}
}

func (t blockingToolStub) Name() string { return t.name }

func (t blockingToolStub) Description() string { return "blocking stub" }

func (t blockingToolStub) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": true,
	}
}

func (t blockingToolStub) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	select {
	case t.started <- struct{}{}:
	default:
	}
	select {
	case <-t.release:
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (t blockingToolStub) IsReadOnly() bool { return false }

func newPersistenceTestSubAgent(parent *MainAgent, instanceID string) *SubAgent {
	ctx, cancel := context.WithCancel(context.Background())
	return &SubAgent{
		instanceID: instanceID,
		parent:     parent,
		parentCtx:  context.Background(),
		cancel:     cancel,
		recovery:   parent.recovery,
		ctxMgr:     ctxmgr.NewManager(8192, false, 0),
		turn: &Turn{
			ID:              1,
			Ctx:             ctx,
			Cancel:          cancel,
			PendingToolMeta: make(map[string]PendingToolCall),
		},
	}
}

func TestHandleAgentErrorFailsPendingToolCalls(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := filepath.Join(projectRoot, ".chord", "sessions", "test")
	a := NewMainAgent(
		context.Background(),
		llm.NewClient(
			llm.NewProviderConfig("test", config.ProviderConfig{
				Type: config.ProviderTypeMessages,
				Models: map[string]config.ModelConfig{
					"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				},
			}, []string{"test-key"}),
			stubProvider{},
			"test-model",
			1024,
			"",
		),
		ctxmgr.NewManager(8192, false, 0),
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"test-model",
		projectRoot,
		&config.Config{},
		nil,
		mcp.ClientInfo{Name: "chord-test", Version: "test"},
	)
	a.startPersistLoop()
	defer func() {
		a.closePersistLoop()
		<-a.persistDone
		a.cancel()
		if a.recovery != nil {
			a.recovery.Close()
		}
	}()

	a.newTurn()
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "tool-1", Name: "Bash", ArgsJSON: `{"command":"sleep 10"}`})

	a.handleAgentError(Event{Type: EventAgentError, TurnID: a.turn.ID, Payload: context.DeadlineExceeded})

	var toolResults []ToolResultEvent
	var idleCount int
	for {
		select {
		case evt := <-a.outputCh:
			switch e := evt.(type) {
			case ToolResultEvent:
				if e.CallID != "tool-1" {
					t.Fatalf("unexpected call id %q", e.CallID)
				}
				if e.Status != ToolResultStatusError {
					t.Fatalf("status = %q, want %q", e.Status, ToolResultStatusError)
				}
				if e.Result == "" {
					t.Fatal("expected failure message")
				}
				toolResults = append(toolResults, e)
			case IdleEvent:
				idleCount++
				if idleCount != 1 {
					t.Fatalf("IdleEvent count = %d, want 1", idleCount)
				}
				if len(toolResults) != 1 {
					t.Fatalf("tool result count = %d, want 1", len(toolResults))
				}
				if a.turn != nil {
					t.Fatal("expected main agent turn cleared after terminal error")
				}
				return
			}
		default:
			t.Fatal("expected failed tool result and idle event")
		}
	}
}

func TestHandleAgentErrorPersistsFailedPendingToolCalls(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.newTurn()
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-9",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://missing.example"}`),
		}},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()
	a.turn.PendingToolCalls.Store(1)
	audit := &message.ToolArgsAudit{
		OriginalArgsJSON:  `{"url":"https://original.example"}`,
		EffectiveArgsJSON: `{"url":"https://missing.example"}`,
		UserModified:      true,
	}
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "tool-9", Name: "WebFetch", ArgsJSON: `{"url":"https://missing.example"}`, Audit: audit})

	a.handleAgentError(Event{Type: EventAgentError, TurnID: a.turn.ID, Payload: context.DeadlineExceeded})
	a.flushPersist()

	msgs := a.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", len(msgs))
	}
	if msgs[1].Role != "tool" || !strings.Contains(msgs[1].Content, "Model stopped before completing this tool call") {
		t.Fatalf("persisted tool message = %#v, want synthetic failure result", msgs[1])
	}
	if msgs[1].Audit == nil || !msgs[1].Audit.UserModified || msgs[1].Audit.EffectiveArgsJSON != audit.EffectiveArgsJSON {
		t.Fatalf("persisted audit = %#v, want %#v", msgs[1].Audit, audit)
	}

	restored, err := a.recovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("len(restored main messages) = %d, want 2", len(restored))
	}
	if restored[1].Role != "tool" || !strings.Contains(restored[1].Content, "context deadline exceeded") {
		t.Fatalf("restored tool message = %#v, want persisted failure cause", restored[1])
	}
	if restored[1].Audit == nil || !restored[1].Audit.UserModified || restored[1].Audit.OriginalArgsJSON != audit.OriginalArgsJSON {
		t.Fatalf("restored audit = %#v, want %#v", restored[1].Audit, audit)
	}
}

func TestCancelCurrentTurnRoutesToFocusedSubAgentAndPersistsFailedToolResult(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newPersistenceTestSubAgent(a, "agent-1")
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()
	a.SwitchFocus(sub.instanceID)

	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-sub-cancel",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://slow.example"}`),
		}},
	}
	sub.ctxMgr.Append(assistant)
	if err := a.recovery.PersistMessage(sub.instanceID, assistant); err != nil {
		t.Fatalf("PersistMessage(sub assistant): %v", err)
	}
	sub.turn.PendingToolCalls.Store(1)
	sub.turn.recordPendingToolCall(PendingToolCall{CallID: "tool-sub-cancel", Name: "WebFetch", ArgsJSON: `{"url":"https://slow.example"}`, AgentID: sub.instanceID})

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	msgs := sub.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("len(sub.GetMessages()) = %d, want 2", len(msgs))
	}
	if msgs[1].Role != "tool" || msgs[1].Content != toolCallFailureMessage(context.Canceled) {
		t.Fatalf("sub tool message = %#v, want failed result", msgs[1])
	}

	restored, err := a.recovery.LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages(sub): %v", err)
	}
	if len(restored) != 2 || restored[1].Content != toolCallFailureMessage(context.Canceled) {
		t.Fatalf("restored sub messages = %#v, want persisted failed result", restored)
	}
}

func TestHandleAgentErrorPersistsFailedSubAgentPendingToolCalls(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newPersistenceTestSubAgent(a, "agent-2")

	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-sub-error",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://missing.example"}`),
		}},
	}
	sub.ctxMgr.Append(assistant)
	if err := a.recovery.PersistMessage(sub.instanceID, assistant); err != nil {
		t.Fatalf("PersistMessage(sub assistant): %v", err)
	}
	sub.turn.PendingToolCalls.Store(1)
	sub.turn.recordPendingToolCall(PendingToolCall{CallID: "tool-sub-error", Name: "WebFetch", ArgsJSON: `{"url":"https://missing.example"}`, AgentID: sub.instanceID})
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()

	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, TurnID: sub.turn.ID, Payload: context.DeadlineExceeded})

	restored, err := a.recovery.LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages(sub): %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("len(restored sub messages) = %d, want 2", len(restored))
	}
	if restored[1].Role != "tool" || !strings.Contains(restored[1].Content, "context deadline exceeded") {
		t.Fatalf("restored sub tool message = %#v, want persisted failure cause", restored[1])
	}
}

func TestMainExecuteToolCallPreservesOutputAndError(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	toolName := "OutputErr"
	a.tools.Register(toolOutputErrorStub{
		name:   toolName,
		result: "stdout\nstderr",
		err:    errors.New("exit code 2"),
	})

	result, err := a.executeToolCall(context.Background(), message.ToolCall{
		Name: toolName,
		Args: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "exit code 2" {
		t.Fatalf("err = %q, want %q", err.Error(), "exit code 2")
	}
	if result.Result != "stdout\nstderr" {
		t.Fatalf("result = %q, want preserved output", result.Result)
	}
}

func TestMainExecuteToolCallRejectsSubAgentOnlyCompleteTool(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.CompleteTool{})

	_, err := a.executeToolCall(context.Background(), message.ToolCall{
		Name: "Complete",
		Args: json.RawMessage(`{"summary":"done"}`),
	})
	if err == nil {
		t.Fatal("expected reserved-tool error")
	}
	if !strings.Contains(err.Error(), "reserved for SubAgents") {
		t.Fatalf("err = %q, want reserved-tool error", err.Error())
	}
}

func TestMainExecuteToolCallSameAgentWriteIsSerialized(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	stub := blockingToolStub{
		name:    "Write",
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	a.tools.Register(stub)

	filePath := filepath.Join(projectRoot, "shared.txt")

	firstDone := make(chan error, 1)
	go func() {
		_, err := a.executeToolCall(context.Background(), message.ToolCall{
			Name: "Write",
			Args: json.RawMessage([]byte(`{"path":` + quoteJSON(filePath) + `,"content":"first"}`)),
		})
		firstDone <- err
	}()
	<-stub.started

	_, err := a.executeToolCall(context.Background(), message.ToolCall{
		Name: "Write",
		Args: json.RawMessage([]byte(`{"path":` + quoteJSON(filePath) + `,"content":"second"}`)),
	})
	if err == nil {
		t.Fatal("expected same-agent concurrent write conflict")
	}
	var ce *filelock.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %T: %v", err, err)
	}

	close(stub.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first write err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first write to finish")
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestComposeToolResultTextsWithOutputError(t *testing.T) {
	display, contextResult, errorText, isError := composeToolResultTexts("stdout\nstderr\n", errors.New("exit code 2"))
	want := "stdout\nstderr\n\nError: exit code 2"
	if !isError {
		t.Fatal("expected error status")
	}
	if errorText != "exit code 2" {
		t.Fatalf("errorText = %q, want %q", errorText, "exit code 2")
	}
	if display != want {
		t.Fatalf("display = %q, want %q", display, want)
	}
	if contextResult != want {
		t.Fatalf("contextResult = %q, want %q", contextResult, want)
	}
}

func TestWrapToolRejectedByUserDisplaysWithoutSentinelDuplication(t *testing.T) {
	err := wrapToolRejectedByUser("Write", "")
	if !errors.Is(err, errToolRejectedByUser) {
		t.Fatalf("errors.Is(err, errToolRejectedByUser) = false for %v", err)
	}
	want := `tool "Write" rejected by user`
	if got := err.Error(); got != want {
		t.Fatalf("err.Error() = %q, want %q", got, want)
	}

	display, contextResult, errorText, isError := composeToolResultTexts("", err)
	if !isError {
		t.Fatal("expected error status")
	}
	if errorText != want {
		t.Fatalf("errorText = %q, want %q", errorText, want)
	}
	if display != want {
		t.Fatalf("display = %q, want %q", display, want)
	}
	if contextResult != "Error: "+want {
		t.Fatalf("contextResult = %q, want %q", contextResult, "Error: "+want)
	}
	if strings.Count(display, "rejected by user") != 1 {
		t.Fatalf("display should mention rejection once, got %q", display)
	}
}

func TestWrapToolRejectedByUserIncludesDenyReasonOnce(t *testing.T) {
	err := wrapToolRejectedByUser("Bash", " risky command ")
	if !errors.Is(err, errToolRejectedByUser) {
		t.Fatalf("errors.Is(err, errToolRejectedByUser) = false for %v", err)
	}
	want := `tool "Bash" rejected by user: risky command`
	if got := err.Error(); got != want {
		t.Fatalf("err.Error() = %q, want %q", got, want)
	}
	if strings.Count(err.Error(), "rejected by user") != 1 {
		t.Fatalf("error should mention rejection once, got %q", err.Error())
	}
	if strings.Contains(err.Error(), ": tool rejected by user") {
		t.Fatalf("error should not append sentinel text, got %q", err.Error())
	}
}

func TestSubAgentDrainPendingToolFailureSets(t *testing.T) {
	s := &SubAgent{instanceID: "agent-1", turn: &Turn{ID: 7, PendingToolMeta: make(map[string]PendingToolCall)}}
	s.turn.recordPendingToolCall(PendingToolCall{CallID: "tool-7", Name: "Read", ArgsJSON: `{"path":"x"}`, AgentID: s.instanceID})
	s.turn.PendingToolCalls.Store(1)

	emit, persist := s.drainPendingToolFailureSets(context.Canceled)
	if len(emit) != 1 || len(persist) != 1 {
		t.Fatalf("emit=%d persist=%d, want 1 each", len(emit), len(persist))
	}
	if emit[0].CallID != "tool-7" || persist[0].CallID != "tool-7" {
		t.Fatalf("call id = %q / %q, want tool-7", emit[0].CallID, persist[0].CallID)
	}
	if got := s.turn.PendingToolCalls.Load(); got != 0 {
		t.Fatalf("pending = %d, want 0", got)
	}
	if remaining := s.turn.cancelPendingToolCalls(); len(remaining) != 0 {
		t.Fatalf("expected pending tool metadata to be cleared, got %d entries", len(remaining))
	}
}

func TestEmitFailedToolResultsMarksErrorStatus(t *testing.T) {
	var events []AgentEvent
	emitFailedToolResults(func(evt AgentEvent) {
		events = append(events, evt)
	}, []PendingToolCall{{CallID: "tool-2", Name: "Bash", ArgsJSON: `{"command":"echo hi"}`, AgentID: "main"}}, context.DeadlineExceeded)

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	result, ok := events[0].(ToolResultEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolResultEvent", events[0])
	}
	if result.Status != ToolResultStatusError {
		t.Fatalf("status = %q, want %q", result.Status, ToolResultStatusError)
	}
	if result.Result == "" {
		t.Fatal("expected non-empty failure message")
	}
}

func TestDiscardSpeculativeStreamToolsEmitsCancelledResult(t *testing.T) {
	var events []AgentEvent
	turn := &Turn{}
	turn.recordStreamingToolCall(PendingToolCall{CallID: "tool-spec-1", Name: "Read", ArgsJSON: `{"path":"internal/llm/provider.go"}`})

	emit := func(evt AgentEvent) {
		events = append(events, evt)
	}
	emitCancelledToolResults(emit, turn.drainStreamingToolCalls())

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	result, ok := events[0].(ToolResultEvent)
	if !ok {
		t.Fatalf("event type = %T, want ToolResultEvent", events[0])
	}
	if result.CallID != "tool-spec-1" {
		t.Fatalf("call_id = %q, want tool-spec-1", result.CallID)
	}
	if result.Status != ToolResultStatusCancelled {
		t.Fatalf("status = %q, want %q", result.Status, ToolResultStatusCancelled)
	}
	if remaining := turn.drainStreamingToolCalls(); len(remaining) != 0 {
		t.Fatalf("streaming tool calls not drained, got %d", len(remaining))
	}
}
