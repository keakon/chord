package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestMainAgentAcquireExecutionSlotCancelledBatchEmitsToolResult(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turn := a.turn
	if turn == nil || turn.streamingToolExec == nil {
		t.Fatal("expected active turn with streamingToolExec")
	}

	// Fill the shared semaphore so AcquireExecutionSlot must wait.
	releases := make([]func(), 0, streamingToolSpeculativeLimit)
	for range streamingToolSpeculativeLimit {
		release := turn.streamingToolExec.AcquireExecutionSlot(context.Background())
		if release == nil {
			t.Fatal("expected AcquireExecutionSlot to succeed")
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	call := message.ToolCall{ID: "tool-1", Name: "Read", Args: []byte(`{"path":"README.md"}`)}
	turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})

	turn.toolExecutionBatches = []toolExecutionBatch{{Calls: []message.ToolCall{call}, AbortSiblingsOnError: true}}
	turn.nextToolBatch = 0

	a.startNextToolBatch(turn)
	if turn.activeToolBatchCancel == nil {
		t.Fatal("expected activeToolBatchCancel to be set")
	}
	turn.activeToolBatchCancel()

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventToolResult {
			t.Fatalf("event type=%v, want %v", evt.Type, EventToolResult)
		}
		payload, ok := evt.Payload.(*ToolResultPayload)
		if !ok || payload == nil {
			t.Fatalf("payload=%T, want *ToolResultPayload", evt.Payload)
		}
		if payload.CallID != call.ID {
			t.Fatalf("CallID=%q, want %q", payload.CallID, call.ID)
		}
		if payload.Error == nil {
			t.Fatal("expected cancelled tool result error")
		}

		a.handleToolResult(evt)
		if got := turn.PendingToolCalls.Load(); got != 0 {
			t.Fatalf("PendingToolCalls=%d, want 0", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for tool result event")
	}
}

func TestMainAgentAcquireExecutionSlotTurnCancelDoesNotEmitToolResult(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turn := a.turn
	if turn == nil || turn.streamingToolExec == nil {
		t.Fatal("expected active turn with streamingToolExec")
	}

	// Fill the shared semaphore so AcquireExecutionSlot must wait.
	releases := make([]func(), 0, streamingToolSpeculativeLimit)
	for range streamingToolSpeculativeLimit {
		release := turn.streamingToolExec.AcquireExecutionSlot(context.Background())
		if release == nil {
			t.Fatal("expected AcquireExecutionSlot to succeed")
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	call := message.ToolCall{ID: "tool-turn-cancel", Name: "Read", Args: []byte(`{"path":"README.md"}`)}
	a.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{call}})
	turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})
	turn.toolExecutionBatches = []toolExecutionBatch{{Calls: []message.ToolCall{call}, AbortSiblingsOnError: true}}
	turn.nextToolBatch = 0

	a.startNextToolBatch(turn)
	if turn.activeToolBatchCancel == nil {
		t.Fatal("expected activeToolBatchCancel to be set")
	}
	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	var cancelEvt Event
	select {
	case cancelEvt = <-a.eventCh:
		if cancelEvt.Type != EventTurnCancelled {
			t.Fatalf("event type=%v, want %v", cancelEvt.Type, EventTurnCancelled)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for turn-cancelled event")
	}

	select {
	case evt := <-a.eventCh:
		t.Fatalf("unexpected extra event after turn cancellation: %v", evt.Type)
	case <-time.After(50 * time.Millisecond):
	}

	a.handleTurnCancelled(cancelEvt)
	msgs := a.GetMessages()
	toolMessages := 0
	for _, msg := range msgs {
		if msg.Role == "tool" && msg.ToolCallID == call.ID {
			toolMessages++
		}
	}
	if toolMessages != 1 {
		t.Fatalf("tool messages for %q = %d, want 1", call.ID, toolMessages)
	}
}

func TestSubAgentAcquireExecutionSlotCancelledBatchEmitsToolResult(t *testing.T) {
	projectRoot := t.TempDir()
	parent := newTestMainAgent(t, projectRoot)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	turn := &Turn{ID: 1, Ctx: ctx, Cancel: cancel, PendingToolMeta: make(map[string]PendingToolCall)}
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, ctx, parent.emitToTUI, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
		return ToolExecutionResult{}, nil
	})

	releases := make([]func(), 0, streamingToolSpeculativeLimit)
	for range streamingToolSpeculativeLimit {
		release := turn.streamingToolExec.AcquireExecutionSlot(context.Background())
		if release == nil {
			t.Fatal("expected AcquireExecutionSlot to succeed")
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()

	s := &SubAgent{
		instanceID:   "sub-test",
		parent:       parent,
		parentCtx:    context.Background(),
		turn:         turn,
		toolCh:       make(chan *toolResult, 8),
		sessionDir:   parent.sessionDir,
		modelName:    "test-model",
		workDir:      projectRoot,
		venvPath:     "",
		agentsMD:     "",
		loadedSkills: nil,
	}

	call := message.ToolCall{ID: "tool-1", Name: "Read", Args: []byte(`{"path":"README.md"}`)}
	turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args), AgentID: s.instanceID})
	turn.toolExecutionBatches = []toolExecutionBatch{{Calls: []message.ToolCall{call}, AbortSiblingsOnError: true}}
	turn.nextToolBatch = 0

	s.startNextToolBatch(turn)
	if turn.activeToolBatchCancel == nil {
		t.Fatal("expected activeToolBatchCancel to be set")
	}
	turn.activeToolBatchCancel()

	select {
	case tr := <-s.toolCh:
		if tr.CallID != call.ID {
			t.Fatalf("CallID=%q, want %q", tr.CallID, call.ID)
		}
		if tr.Error == nil {
			t.Fatal("expected cancelled tool result error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for sub-agent tool result")
	}
}
