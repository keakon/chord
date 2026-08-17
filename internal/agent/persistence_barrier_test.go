package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

type barrierRecordingTool struct {
	name     string
	readOnly bool
	ran      atomic.Bool
	// startedMustBeSet asserts the started-journal write happened before the
	// execution body when non-nil.
	startedMustBeSet *atomic.Bool
}

func (t *barrierRecordingTool) Name() string               { return t.name }
func (t *barrierRecordingTool) Description() string        { return "test recording tool" }
func (t *barrierRecordingTool) Parameters() map[string]any { return nil }
func (t *barrierRecordingTool) IsReadOnly() bool           { return t.readOnly }
func (t *barrierRecordingTool) ConcurrencySafeReadOnly(json.RawMessage) bool {
	return t.readOnly
}
func (t *barrierRecordingTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	t.ran.Store(true)
	if t.startedMustBeSet != nil && !t.startedMustBeSet.Load() {
		return "", fmt.Errorf("tool body ran before the started journal write")
	}
	return "ok", nil
}

func TestToolActivityJournalRequiredFilter(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{})
	registry.Register(tools.WriteTool{})
	registry.Register(&tools.TodoWriteTool{})

	cases := []struct {
		name     string
		tool     string
		args     string
		required bool
	}{
		{name: "read", tool: tools.NameRead, args: `{"path":"a.txt"}`, required: false},
		{name: "write", tool: tools.NameWrite, args: `{"path":"a.txt","content":"x"}`, required: true},
		{name: "todo_write", tool: tools.NameTodoWrite, args: `{"todos":[]}`, required: true},
		{name: "unknown", tool: "not-a-real-tool", args: `{}`, required: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := message.ToolCall{Name: tc.tool, Args: json.RawMessage(tc.args)}
			if got := toolActivityJournalRequired(registry, call); got != tc.required {
				t.Fatalf("toolActivityJournalRequired(%s) = %v, want %v", tc.tool, got, tc.required)
			}
		})
	}
}

func TestPromotedTodoWritesStartedJournalBeforeCommit(t *testing.T) {
	rm := recovery.NewRecoveryManager(t.TempDir())
	t.Cleanup(rm.Close)
	committed := false
	a := &MainAgent{recovery: rm}
	call := message.ToolCall{ID: "todo-1", Name: tools.NameTodoWrite, Args: json.RawMessage(`{"todos":[]}`)}
	payload := &ToolResultPayload{
		TurnID: 7,
		speculativeHooks: &speculativeToolHooks{commit: func() error {
			committed = true
			return nil
		}},
	}
	if err := a.commitPromotedToolSideEffects(call, payload); err != nil {
		t.Fatalf("commitPromotedToolSideEffects: %v", err)
	}
	if !committed {
		t.Fatal("todo commit did not run")
	}
	started, err := rm.LoadToolActivity()
	if err != nil {
		t.Fatalf("LoadToolActivity: %v", err)
	}
	if _, ok := started[recovery.ToolActivityKey{AgentID: identity.MainAgentID, CallID: call.ID}]; !ok {
		t.Fatalf("started journal = %#v, want promoted todo record", started)
	}
}

func TestPromotedTodoJournalFailureBlocksCommit(t *testing.T) {
	rm := recovery.NewRecoveryManager(t.TempDir())
	rm.Close()
	committed := false
	a := &MainAgent{recovery: rm}
	call := message.ToolCall{ID: "todo-1", Name: tools.NameTodoWrite, Args: json.RawMessage(`{"todos":[]}`)}
	payload := &ToolResultPayload{
		TurnID: 7,
		speculativeHooks: &speculativeToolHooks{commit: func() error {
			committed = true
			return nil
		}},
	}
	if err := a.commitPromotedToolSideEffects(call, payload); err == nil {
		t.Fatal("expected journal failure")
	}
	if committed {
		t.Fatal("todo committed after journal failure")
	}
}

func TestToolExecutionPipelineWritesStartedJournalBeforeExecute(t *testing.T) {
	projectRoot := t.TempDir()
	registry := tools.NewRegistry()
	tool := &barrierRecordingTool{name: "mutating"}
	registry.Register(tool)

	var started atomic.Bool
	var appended []recovery.ToolActivityRecord
	pipeline := toolExecutionPipeline{
		agentID:     "agent-1",
		registry:    registry,
		fileTrack:   filelock.NewFileTracker(),
		projectRoot: projectRoot,
		appendToolActivity: func(rec recovery.ToolActivityRecord) error {
			appended = append(appended, rec)
			started.Store(true)
			return nil
		},
	}
	tool.startedMustBeSet = &started

	call := message.ToolCall{ID: "call-1", Name: "mutating", Args: json.RawMessage(`{}`)}
	if _, err := pipeline.execute(context.Background(), call, false); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !tool.ran.Load() {
		t.Fatal("tool body did not run")
	}
	if len(appended) != 1 {
		t.Fatalf("appended journal records = %d, want 1", len(appended))
	}
	if appended[0].CallID != "call-1" || appended[0].AgentID != "agent-1" || appended[0].State != recovery.ToolActivityStateStarted {
		t.Fatalf("journal record = %+v", appended[0])
	}
}

func TestToolExecutionPipelineSkipsStartedJournalForReadOnly(t *testing.T) {
	projectRoot := t.TempDir()
	registry := tools.NewRegistry()
	tool := &barrierRecordingTool{name: "lookup", readOnly: true}
	registry.Register(tool)

	var appended int
	pipeline := toolExecutionPipeline{
		agentID:     "agent-1",
		registry:    registry,
		fileTrack:   filelock.NewFileTracker(),
		projectRoot: projectRoot,
		appendToolActivity: func(recovery.ToolActivityRecord) error {
			appended++
			return nil
		},
	}

	call := message.ToolCall{ID: "call-1", Name: "lookup", Args: json.RawMessage(`{}`)}
	if _, err := pipeline.execute(context.Background(), call, false); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if appended != 0 {
		t.Fatalf("journal appends = %d, want 0 for read-only call", appended)
	}
}

func TestToolExecutionPipelineJournalFailureBlocksExecute(t *testing.T) {
	projectRoot := t.TempDir()
	registry := tools.NewRegistry()
	tool := &barrierRecordingTool{name: "mutating"}
	registry.Register(tool)

	journalErr := errors.New("journal write failed")
	pipeline := toolExecutionPipeline{
		agentID:     "agent-1",
		registry:    registry,
		fileTrack:   filelock.NewFileTracker(),
		projectRoot: projectRoot,
		appendToolActivity: func(recovery.ToolActivityRecord) error {
			return journalErr
		},
	}

	call := message.ToolCall{ID: "call-1", Name: "mutating", Args: json.RawMessage(`{}`)}
	if _, err := pipeline.execute(context.Background(), call, false); err == nil {
		t.Fatal("execute succeeded despite journal failure, want error")
	}
	if tool.ran.Load() {
		t.Fatal("tool body ran despite journal failure")
	}
}

func TestIntentBarrierPersistsAssistantBeforeToolDispatch(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.sessionDir = filepath.Join(t.TempDir(), "session")
	a.recovery = recovery.NewRecoveryManager(a.sessionDir)
	a.newTurn()

	payload := &LLMResponsePayload{
		ToolCalls:  []message.ToolCall{{ID: "call-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)}},
		StopReason: "tool_use",
	}
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	// The barrier is synchronous inside handleLLMResponse: by the time it
	// returns, the assistant tool-call message must already be on disk.
	msgs, err := a.recovery.LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assistant tool-call message not persisted before dispatch: %#v", msgs)
	}
	if a.persistenceDegraded() {
		t.Fatal("persistence should be healthy after a successful barrier")
	}
}

func TestIntentBarrierSuccessRecoversMainPersistenceHealth(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.persistenceHealth.markDegraded(errors.New("temporary persistence error"))
	a.newTurn()

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{
		ToolCalls:  []message.ToolCall{{ID: "call-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)}},
		StopReason: "tool_use",
	}})

	if state := a.persistenceHealth.snapshot().State; state != PersistenceHealthy {
		t.Fatalf("persistence state = %q, want healthy after successful intent barrier", state)
	}
}

func TestMainToolActivityJournalUsesStableMainScope(t *testing.T) {
	a := newReadyTestMainAgent(t)
	pipeline := a.toolExecutionPipeline()
	if pipeline.agentID == identity.MainAgentID {
		t.Fatalf("main runtime agent ID = %q, want a distinct instance ID", pipeline.agentID)
	}
	if pipeline.toolActivityAgentID() != identity.MainAgentID {
		t.Fatalf("journal agent ID = %q, want %q", pipeline.toolActivityAgentID(), identity.MainAgentID)
	}
}

// newBrokenPathRecoveryManager returns a manager whose session directory can
// never be created (its parent is a regular file), so every write fails with
// a real filesystem error rather than ErrClosed.
func newBrokenPathRecoveryManager(t *testing.T) *recovery.RecoveryManager {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	return recovery.NewRecoveryManager(filepath.Join(blocker, "session"))
}

func TestAssistantPersistenceFailureWithoutToolsDegradesMainAgent(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.recovery = newBrokenPathRecoveryManager(t)
	a.newTurn()

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: &LLMResponsePayload{
		Content:    "final answer",
		StopReason: "stop",
	}})
	a.flushPersist()

	if !a.persistenceDegraded() {
		t.Fatal("main persistence remained healthy after an answer-only write failure")
	}
}

func TestLoadTaskHistoryMessagesScopesRecoveryByInstance(t *testing.T) {
	rm := recovery.NewRecoveryManager(t.TempDir())
	defer rm.Close()

	// instance-a: orphan call-1 with a started journal record.
	if err := rm.PersistMessage("agent-a", message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "shell"}}}); err != nil {
		t.Fatalf("PersistMessage(agent-a): %v", err)
	}
	if err := rm.AppendToolActivity(recovery.ToolActivityRecord{CallID: "call-1", AgentID: "agent-a", Tool: "shell", State: recovery.ToolActivityStateStarted}); err != nil {
		t.Fatalf("AppendToolActivity: %v", err)
	}
	// instance-b: the same call-1 orphan, but never started.
	if err := rm.PersistMessage("agent-b", message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "shell"}}}); err != nil {
		t.Fatalf("PersistMessage(agent-b): %v", err)
	}

	started, err := rm.LoadToolActivity()
	if err != nil {
		t.Fatalf("LoadToolActivity: %v", err)
	}
	rec := &DurableTaskRecord{TaskID: "task-1", InstanceHistory: []string{"agent-a", "agent-b"}, TaskDesc: "task"}
	msgs, err := loadTaskHistoryMessages(rm, rec, started)
	if err != nil {
		t.Fatalf("loadTaskHistoryMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages = %#v, want 4 (2 instances x assistant + synthetic result)", msgs)
	}
	if msgs[1].ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("agent-a recovery state = %q, want outcome_unknown", msgs[1].ToolRecoveryState)
	}
	if msgs[3].ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("agent-b recovery state = %q, want not_started", msgs[3].ToolRecoveryState)
	}
}

func TestSubAgentPersistMessageBarrierDirectWriteFailClosed(t *testing.T) {
	sub := &SubAgent{
		instanceID: "worker-1",
		recovery:   newBrokenPathRecoveryManager(t),
	}

	barrier, enqueued := sub.persistMessageBarrier(message.Message{Role: "assistant"}, "assistant message")
	if !enqueued {
		t.Fatal("direct-write path should report enqueued=true (synchronous)")
	}
	if err := <-barrier; err == nil {
		t.Fatal("direct-write barrier returned nil after recovery Close, want error")
	}
	if state := sub.persistenceHealth.snapshot().State; state != PersistenceDegraded {
		t.Fatalf("persistence state = %q, want degraded", state)
	}
}

func TestNotePersistenceFailureIgnoresErrClosed(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.notePersistenceFailure(recovery.ErrClosed)
	if a.persistenceDegraded() {
		t.Fatal("ErrClosed after an intentional close must not degrade main persistence")
	}
	sub := &SubAgent{instanceID: "worker-1"}
	sub.notePersistenceFailure(recovery.ErrClosed)
	if sub.persistenceHealth.snapshot().State == PersistenceDegraded {
		t.Fatal("ErrClosed must not degrade SubAgent persistence")
	}
}

func TestWaitPersistBarrierFailClosed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if err := a.waitPersistBarrier(make(chan error), false); !errors.Is(err, errPersistenceQueueUnavailable) {
		t.Fatalf("enqueue-failed barrier error = %v, want errPersistenceQueueUnavailable", err)
	}
	a.signalStopping()
	if err := a.waitPersistBarrier(make(chan error), true); !errors.Is(err, errPersistenceStopping) {
		t.Fatalf("stop-first barrier error = %v, want errPersistenceStopping", err)
	}
}

func TestLoopPausedWhenPersistenceDegraded(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.loopState.Enabled = true
	a.persistenceHealth.markDegraded(errors.New("disk full"))

	assessment := a.nextLoopAssessmentFromAssistant(message.Message{Role: "assistant", Content: "done"})
	if assessment == nil {
		t.Fatal("expected an assessment while degraded")
	}
	if assessment.Action != LoopAssessmentActionBlocked {
		t.Fatalf("assessment.Action = %q, want %q", assessment.Action, LoopAssessmentActionBlocked)
	}
}

func TestTryRecoverPersistenceBeforeTurnRecoversAfterCheckpoint(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.sessionDir = filepath.Join(t.TempDir(), "session")
	a.recovery = recovery.NewRecoveryManager(a.sessionDir)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "hi"})
	a.persistenceHealth.markDegraded(errors.New("disk full"))

	a.tryRecoverPersistenceBeforeTurn()
	if a.persistenceDegraded() {
		t.Fatal("persistence should recover after a successful checkpoint")
	}
}

func TestCrashPointClassificationFromDiskState(t *testing.T) {
	dir := t.TempDir()
	rm := recovery.NewRecoveryManager(dir)
	assistant := message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "shell"}}}

	// A prior side-effect tool has already run, so the journal exists.
	if err := rm.AppendToolActivity(recovery.ToolActivityRecord{CallID: "call-0", AgentID: "main", Tool: "shell", State: recovery.ToolActivityStateStarted}); err != nil {
		t.Fatalf("AppendToolActivity: %v", err)
	}

	// Crash point 1: assistant persisted, no started record for call-1 → not_started.
	if err := rm.PersistMessage("main", assistant); err != nil {
		t.Fatalf("PersistMessage: %v", err)
	}
	rm.Close()

	rm2 := recovery.NewRecoveryManager(dir)
	msgs, err := rm2.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	msgs = normalizeRestoredMessages(msgs, loadToolActivityStarted(rm2), "main")
	if len(msgs) != 2 || msgs[1].ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("crash-point-1 classification = %#v, want not_started", msgs)
	}
	rm2.Close()

	// Crash point 2: assistant + started record, no result → outcome_unknown.
	rm3 := recovery.NewRecoveryManager(dir)
	if err := rm3.AppendToolActivity(recovery.ToolActivityRecord{CallID: "call-1", AgentID: "main", Tool: "shell", State: recovery.ToolActivityStateStarted}); err != nil {
		t.Fatalf("AppendToolActivity: %v", err)
	}
	rm3.Close()

	rm4 := recovery.NewRecoveryManager(dir)
	msgs, err = rm4.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	msgs = normalizeRestoredMessages(msgs, loadToolActivityStarted(rm4), "main")
	if len(msgs) != 2 || msgs[1].ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("crash-point-2 classification = %#v, want outcome_unknown", msgs)
	}
	rm4.Close()
}

func TestFailIntentBarrierSynthesizesNotStartedResults(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.recovery = newBrokenPathRecoveryManager(t) // force the intent barrier to fail closed
	a.newTurn()

	payload := &LLMResponsePayload{
		ToolCalls:  []message.ToolCall{{ID: "call-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"a.txt","content":"x"}`)}},
		StopReason: "tool_use",
	}
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	if !a.persistenceDegraded() {
		t.Fatal("expected persistence to be degraded after barrier failure")
	}
	if a.turn == nil {
		t.Fatal("turn became nil after barrier failure")
	}
	// The synthetic not_started result is queued as an EventToolResult; process
	// it the way the event loop would.
	var processed int
	select {
	case evt := <-a.eventCh:
		if evt.Type != EventToolResult {
			t.Fatalf("unexpected queued event type %q", evt.Type)
		}
		a.handleToolResult(evt)
		processed++
	default:
	}
	if processed != 1 {
		t.Fatalf("processed tool results = %d, want 1", processed)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v, want assistant + synthetic tool result", msgs)
	}
	synth := msgs[1]
	if synth.Role != "tool" || synth.ToolCallID != "call-1" {
		t.Fatalf("synthetic message = %#v", synth)
	}
	if synth.ToolRecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("recovery state = %q, want not_started", synth.ToolRecoveryState)
	}
	if synth.ToolStatus != string(ToolResultStatusError) {
		t.Fatalf("tool status = %q, want error", synth.ToolStatus)
	}
}

func TestRepeatedIntentBarrierFailuresAbortTurn(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.sessionDir = filepath.Join(t.TempDir(), "session")
	a.recovery = recovery.NewRecoveryManager(a.sessionDir)
	a.recovery.Close() // force every intent barrier to fail closed
	a.newTurn()
	// Simulate a prior LLM round in this turn whose dispatch already failed the
	// barrier; this round's failure is the second consecutive one.
	a.turn.BarrierFailureRounds = 1

	payload := &LLMResponsePayload{
		ToolCalls:  []message.ToolCall{{ID: "call-abort", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"a.txt","content":"x"}`)}},
		StopReason: "tool_use",
	}
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})
	if a.turn.BarrierFailureRounds != maxIntentBarrierFailureRounds {
		t.Fatalf("BarrierFailureRounds = %d, want %d", a.turn.BarrierFailureRounds, maxIntentBarrierFailureRounds)
	}

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventToolResult {
			t.Fatalf("unexpected queued event type %q", evt.Type)
		}
		a.handleToolResult(evt)
	default:
		t.Fatal("expected a queued synthetic tool result")
	}

	// Second consecutive barrier failure: the turn must end instead of firing
	// another LLM round against the broken write path.
	if a.turn != nil {
		t.Fatalf("turn = %v, want aborted (nil)", a.turn)
	}
	// The synthetic result is still in the transcript so no orphan remains.
	msgs := a.ctxMgr.Snapshot()
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "call-abort" {
		t.Fatalf("last message = %#v, want synthetic tool result", last)
	}
}

func TestIntentBarrierSuccessResetsBarrierFailureRounds(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.sessionDir = filepath.Join(t.TempDir(), "session")
	a.recovery = recovery.NewRecoveryManager(a.sessionDir)
	t.Cleanup(a.recovery.Close)
	a.startPersistLoop()
	t.Cleanup(a.closePersistLoop)
	a.newTurn()
	a.turn.BarrierFailureRounds = 1

	payload := &LLMResponsePayload{
		ToolCalls:  []message.ToolCall{{ID: "call-ok", Name: "nonexistent_tool", Args: json.RawMessage(`{}`)}},
		StopReason: "tool_use",
	}
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})
	if a.turn == nil {
		t.Fatal("turn ended unexpectedly")
	}
	if a.turn.BarrierFailureRounds != 0 {
		t.Fatalf("BarrierFailureRounds = %d, want 0 after a successful barrier", a.turn.BarrierFailureRounds)
	}
}
