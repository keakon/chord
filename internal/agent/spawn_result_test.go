package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHandleBackgroundObjectFinishedForMainAppendsContextAndStartsTurn(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	payload := &tools.SpawnFinishedPayload{
		BackgroundID: "job-1",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-1 completed]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}

	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload})

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		t.Fatal("expected context message appended for main background result")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("last role = %q, want user", last.Role)
	}
	if !strings.Contains(last.Content, "Run production build") {
		t.Fatalf("last content = %q, want build description", last.Content)
	}
	if a.turn == nil {
		t.Fatal("expected new turn to start after main background result")
	}
}

func TestHandleBackgroundObjectFinishedForMainQueuesWhileBusy(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{
			{ID: "grep-1", Name: "Grep", Args: []byte(`{"pattern":"TODO","path":"internal","glob":"**/*.go"}`)},
		},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "grep-1", Name: "Grep", ArgsJSON: `{"pattern":"TODO","path":"internal","glob":"**/*.go"}`})

	payload := &tools.SpawnFinishedPayload{
		BackgroundID: "job-1",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-1 completed]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}

	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload})
	a.flushPersist()

	if a.turn == nil || a.turn.ID != turnID {
		t.Fatalf("turn = %+v, want original active turn %d", a.turn, turnID)
	}
	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1", got)
	}
	if got := a.pendingUserMessages[0].Content; !strings.Contains(got, "Run production build") {
		t.Fatalf("pending background completion = %q, want build description", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("len(ctx snapshot) = %d, want 1 assistant tool-call message only", len(msgs))
	}
	if got := a.turn.PendingToolCalls.Load(); got != 1 {
		t.Fatalf("PendingToolCalls = %d, want 1", got)
	}
	if restored, err := a.recovery.LoadMessages("main"); err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	} else if len(restored) != 1 {
		t.Fatalf("len(restored) = %d, want 1 assistant tool-call message only", len(restored))
	}
}

func TestHandleBackgroundObjectFinishedForMainCoalescesContiguousBusyResults(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

	payload1 := &tools.SpawnFinishedPayload{
		BackgroundID: "job-1",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-1 completed]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}
	payload2 := &tools.SpawnFinishedPayload{
		BackgroundID: "job-2",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Upload release bundle",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-2 completed]\n\nDescription: Upload release bundle\nStatus: finished (exit 0)",
	}

	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload1})
	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload2})

	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1 coalesced entry", got)
	}
	got := a.pendingUserMessages[0]
	if !strings.Contains(got.Content, "Run production build") || !strings.Contains(got.Content, "Upload release bundle") {
		t.Fatalf("coalesced content = %q, want both background descriptions", got.Content)
	}
	if strings.Index(got.Content, "Run production build") > strings.Index(got.Content, "Upload release bundle") {
		t.Fatalf("coalesced content order = %q, want job-1 before job-2", got.Content)
	}
}

func TestHandleBackgroundObjectFinishedForMainDoesNotCoalesceAcrossUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

	payload1 := &tools.SpawnFinishedPayload{
		BackgroundID: "job-1",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-1 completed]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}
	payload2 := &tools.SpawnFinishedPayload{
		BackgroundID: "job-2",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Upload release bundle",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-2 completed]\n\nDescription: Upload release bundle\nStatus: finished (exit 0)",
	}

	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload1})
	a.handleUserMessage(Event{Payload: "queued user follow-up"})
	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload2})

	if got := len(a.pendingUserMessages); got != 3 {
		t.Fatalf("len(pendingUserMessages) = %d, want 3 distinct ordered entries", got)
	}
	if !strings.Contains(a.pendingUserMessages[0].Content, "Run production build") {
		t.Fatalf("first pending content = %q, want first background completion", a.pendingUserMessages[0].Content)
	}
	if a.pendingUserMessages[1].Content != "queued user follow-up" {
		t.Fatalf("second pending content = %q, want queued user follow-up", a.pendingUserMessages[1].Content)
	}
	if !strings.Contains(a.pendingUserMessages[2].Content, "Upload release bundle") {
		t.Fatalf("third pending content = %q, want second background completion", a.pendingUserMessages[2].Content)
	}
}

func TestHandleBackgroundObjectFinishedForMainMergesAfterToolBatch(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{
			{ID: "grep-1", Name: "Grep", Args: []byte(`{"pattern":"TODO","path":"internal","glob":"**/*.go"}`)},
		},
	}
	a.ctxMgr.Append(assistant)
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "grep-1", Name: "Grep", ArgsJSON: `{"pattern":"TODO","path":"internal","glob":"**/*.go"}`})

	payload := &tools.SpawnFinishedPayload{
		BackgroundID: "job-1",
		AgentID:      a.instanceID,
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-1 completed]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}
	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: a.instanceID, Payload: payload})

	a.handleToolResult(Event{Type: EventToolResult, TurnID: a.turn.ID, Payload: &ToolResultPayload{
		CallID:   "grep-1",
		Name:     "Grep",
		ArgsJSON: `{"pattern":"TODO","path":"internal","glob":"**/*.go"}`,
		Result:   "No matches found.",
		TurnID:   a.turn.ID,
	}})

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 3 {
		t.Fatalf("len(ctx snapshot) = %d, want 3", len(msgs))
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "grep-1" {
		t.Fatalf("tool result message = %#v, want grep-1 tool result", msgs[1])
	}
	if msgs[2].Role != "user" || !strings.Contains(msgs[2].Content, "Run production build") {
		t.Fatalf("merged background completion message = %#v, want user background completion", msgs[2])
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 after merge", got)
	}
}

func TestHandleBackgroundObjectFinishedRoutesToOwnerSubAgentOnly(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := &SubAgent{
		instanceID:  "builder-2",
		parent:      a,
		parentCtx:   ctx,
		cancel:      cancel,
		ctxAppendCh: make(chan message.Message, 1),
		continueCh:  make(chan continueMsg, 1),
	}
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()

	payload := &tools.SpawnFinishedPayload{
		BackgroundID: "job-7",
		AgentID:      sub.instanceID,
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Background object job-7 completed]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}

	a.handleSpawnFinished(Event{Type: EventSpawnFinished, SourceID: sub.instanceID, Payload: payload})

	select {
	case msg := <-sub.ctxAppendCh:
		if !strings.Contains(msg.Content, "Run production build") {
			t.Fatalf("subagent append msg = %q, want build description", msg.Content)
		}
	default:
		t.Fatal("expected subagent to receive background completion context append")
	}
	select {
	case <-sub.continueCh:
		// ok
	default:
		t.Fatal("expected subagent continue signal after background completion")
	}
	for _, msg := range a.ctxMgr.Snapshot() {
		if strings.Contains(msg.Content, "Run production build") {
			t.Fatalf("main context should not receive subagent background result: %q", msg.Content)
		}
	}
}
