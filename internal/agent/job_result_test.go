package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func backgroundResultPayload(agentID, backgroundID, description string) *tools.JobFinishedPayload {
	return &tools.JobFinishedPayload{
		BackgroundID: backgroundID,
		AgentID:      agentID,
		Status:       "completed (exit code 0)",
		Message:      "[Background job " + backgroundID + " finished]\n\nStatus: completed (exit code 0)\nPurpose: " + description,
	}
}

func mainInboxBackgroundResults(a *MainAgent) []SubAgentMailboxMessage {
	var out []SubAgentMailboxMessage
	for _, msg := range a.subAgentInbox.normal {
		if msg.Kind == SubAgentMailboxKindBackgroundResult {
			out = append(out, msg)
		}
	}
	return out
}

func TestHandleBackgroundObjectFinishedForMainIdleStartsTurnAndDelivers(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-1", "Run production build")})

	if a.turn == nil {
		t.Fatal("expected new turn to start after main background result")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0: a background result is queued durably, not in memory", got)
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("len(pendingSubAgentMailboxes) = %d, want the durable result staged for the boundary", got)
	}

	overlays := a.buildTurnOverlayMessages()
	if len(overlays) != 1 {
		t.Fatalf("overlay count = %d, want 1", len(overlays))
	}
	delivered := overlays[0]
	if delivered.Role != message.RoleUser || delivered.Kind != message.KindBackgroundResult {
		t.Fatalf("overlay = %#v, want a user KindBackgroundResult message", delivered)
	}
	if delivered.Mailbox == nil || delivered.Mailbox.Kind != string(SubAgentMailboxKindBackgroundResult) {
		t.Fatalf("overlay mailbox metadata = %#v", delivered.Mailbox)
	}
	if !strings.Contains(delivered.Content, "Run production build") {
		t.Fatalf("overlay content = %q, want build description", delivered.Content)
	}
	ctx := a.ctxMgr.Snapshot()
	if len(ctx) != 1 || ctx[0].Kind != message.KindBackgroundResult {
		t.Fatalf("context = %#v, want the durable background result", ctx)
	}
	// The durable row is recognized as delivered, so a restore does not replay
	// a result the model already saw.
	if _, ok := conversationMailboxIDs(ctx)[delivered.Mailbox.MessageID]; !ok {
		t.Fatalf("conversationMailboxIDs = %v, want the delivered background result", conversationMailboxIDs(ctx))
	}
	a.flushPersist()
	persisted, err := a.recoveryManager().LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(persisted) != 1 || persisted[0].Kind != message.KindBackgroundResult {
		t.Fatalf("persisted = %#v, want a durable background result", persisted)
	}
}

func TestHandleBackgroundObjectFinishedForMainQueuesDurablyWhileBusy(t *testing.T) {
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
			{ID: "grep-1", Name: "grep", Args: []byte(`{"pattern":"TODO","paths":["internal"],"includes":["**/*.go"]}`)},
		},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "grep-1", Name: "grep", ArgsJSON: `{"pattern":"TODO","paths":["internal"],"includes":["**/*.go"]}`})

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-1", "Run production build")})
	a.flushPersist()

	if a.turn == nil || a.turn.ID != turnID {
		t.Fatalf("turn = %+v, want original active turn %d", a.turn, turnID)
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
	}
	if got := len(a.pendingSubAgentMailboxes); got != 0 {
		t.Fatalf("len(pendingSubAgentMailboxes) = %d, want nothing staged mid-batch", got)
	}
	if got := len(mainInboxBackgroundResults(a)); got != 1 {
		t.Fatalf("main inbox background results = %d, want 1", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("len(ctx snapshot) = %d, want 1 assistant tool-call message only", len(msgs))
	}
	if got := a.turn.PendingToolCalls.Load(); got != 1 {
		t.Fatalf("PendingToolCalls = %d, want 1", got)
	}
	restored, err := a.recoveryManager().LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("len(restored) = %d, want 1 assistant tool-call message only", len(restored))
	}
	// The result is durable in the mailbox log before it is shown, so a restart
	// or a session switch replays it instead of dropping it.
	rows, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	backgroundRows := 0
	for _, row := range rows {
		if row.Kind == SubAgentMailboxKindBackgroundResult {
			backgroundRows++
			if !strings.Contains(row.Summary, "Run production build") {
				t.Fatalf("durable row summary = %q, want build description", row.Summary)
			}
		}
	}
	if backgroundRows != 1 {
		t.Fatalf("durable background_result rows = %d, want 1", backgroundRows)
	}
}

func TestHandleBackgroundObjectFinishedForMainKeepsDistinctDurableRows(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-1", "Run production build")})
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-2", "Upload release bundle")})

	// One durable row per finished job: a JOB RESULT card without its own
	// backing transcript slot would make the live view disagree with a restored
	// session about how many results exist.
	rows := mainInboxBackgroundResults(a)
	if len(rows) != 2 {
		t.Fatalf("main inbox background results = %d, want 2 distinct entries", len(rows))
	}
	if !strings.Contains(rows[0].Summary, "Run production build") {
		t.Fatalf("first row = %q, want job-1", rows[0].Summary)
	}
	if !strings.Contains(rows[1].Summary, "Upload release bundle") {
		t.Fatalf("second row = %q, want job-2", rows[1].Summary)
	}
	if rows[0].MessageID == "" || rows[0].MessageID == rows[1].MessageID {
		t.Fatalf("message ids = (%q, %q), want two distinct durable ids", rows[0].MessageID, rows[1].MessageID)
	}
}

func TestHandleBackgroundObjectFinishedForMainDoesNotCoalesceAcrossUserInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-1", "Run production build")})
	a.handleUserMessage(Event{Payload: "queued user follow-up"})
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-2", "Upload release bundle")})

	if got := len(a.pendingUserMessages); got != 1 || a.pendingUserMessages[0].Content != "queued user follow-up" {
		t.Fatalf("pendingUserMessages = %#v, want only the queued user follow-up", a.pendingUserMessages)
	}
	rows := mainInboxBackgroundResults(a)
	if len(rows) != 2 {
		t.Fatalf("main inbox background results = %d, want 2 distinct entries", len(rows))
	}
	if !strings.Contains(rows[0].Summary, "Run production build") || !strings.Contains(rows[1].Summary, "Upload release bundle") {
		t.Fatalf("rows = (%q, %q), want job-1 then job-2", rows[0].Summary, rows[1].Summary)
	}
}

func TestHandleBackgroundObjectFinishedForMainDeliversAfterToolBatch(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{
			{ID: "grep-1", Name: "grep", Args: []byte(`{"pattern":"TODO","paths":["internal"],"includes":["**/*.go"]}`)},
		},
	}
	a.ctxMgr.Append(assistant)
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "grep-1", Name: "grep", ArgsJSON: `{"pattern":"TODO","paths":["internal"],"includes":["**/*.go"]}`})

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-1", "Run production build")})

	// A result that arrives while a tool batch is open must never land between
	// the assistant tool_calls message and the tool result that closes it.
	if msgs := a.ctxMgr.Snapshot(); len(msgs) != 1 {
		t.Fatalf("len(ctx snapshot) = %d, want only the assistant tool-call message", len(msgs))
	}

	a.handleToolResult(Event{Type: EventToolResult, TurnID: a.turn.ID, Payload: &ToolResultPayload{
		CallID:   "grep-1",
		Name:     "grep",
		ArgsJSON: `{"pattern":"TODO","paths":["internal"],"includes":["**/*.go"]}`,
		Result:   "No matches found.",
		TurnID:   a.turn.ID,
	}})
	a.prepareSubAgentMailboxBatchForTurnContinuation()
	a.buildTurnOverlayMessages()

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 3 {
		t.Fatalf("len(ctx snapshot) = %d, want 3", len(msgs))
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "grep-1" {
		t.Fatalf("tool result message = %#v, want grep-1 tool result", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Kind != message.KindBackgroundResult {
		t.Fatalf("delivered background completion = %#v, want user KindBackgroundResult", msgs[2])
	}
	if !strings.Contains(msgs[2].Content, "Run production build") {
		t.Fatalf("delivered content = %q, want build description", msgs[2].Content)
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
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
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()

	payload := backgroundResultPayload(sub.instanceID, "job-7", "Run production build")

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: sub.instanceID, Payload: payload})

	select {
	case msg := <-sub.ctxAppendCh:
		if !strings.Contains(msg.Content, "Run production build") {
			t.Fatalf("subagent append msg = %q, want build description", msg.Content)
		}
		if msg.Kind != message.KindBackgroundResult {
			t.Fatalf("subagent append kind = %q, want %q", msg.Kind, message.KindBackgroundResult)
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

func TestHandleBackgroundObjectFinishedOrphanOwnerFallsBackToMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()

	payload := backgroundResultPayload("builder-gone", "job-9", "Run production build")
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: payload.AgentID, Payload: payload})

	// A terminated owner must not leave a card with no backing transcript slot:
	// the result falls back to the main transcript like a main-owned result.
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0", got)
	}
	rows := mainInboxBackgroundResults(a)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "Run production build") {
		t.Fatalf("main inbox background results = %#v, want one main fallback row", rows)
	}
}

func TestHandleBackgroundObjectFinishedSubOwnerQueueFullSpoolsDurably(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := &SubAgent{
		instanceID:        "builder-2",
		parent:            a,
		parentCtx:         ctx,
		cancel:            cancel,
		ctxAppendCh:       make(chan message.Message, 1),
		continueCh:        make(chan continueMsg, 1),
		queueMessageLimit: 1,
	}
	// Fill the one-message queue so routing the result to the owner is rejected.
	if !sub.TryEnqueueContextAppend(message.Message{Role: message.RoleUser, Content: "already queued", Kind: message.KindBackgroundResult}) {
		t.Fatal("precondition enqueue rejected")
	}
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: sub.instanceID, Payload: backgroundResultPayload(sub.instanceID, "job-8", "Run production build")})
	a.flushPersist()

	// A full owner queue must not drop the result: it stays a durable mailbox
	// row and is spooled under the owner for a later drain.
	rows, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	durable := 0
	for _, row := range rows {
		if row.Kind == SubAgentMailboxKindBackgroundResult && strings.Contains(row.Summary, "Run production build") {
			durable++
		}
	}
	if durable != 1 {
		t.Fatalf("durable background_result rows = %d, want 1", durable)
	}
	a.subAgentMailboxIDsMu.Lock()
	owned := len(a.ownedSubAgentMailboxes[sub.instanceID]) + len(a.ownedMailboxSpool[sub.instanceID])
	a.subAgentMailboxIDsMu.Unlock()
	if owned != 1 {
		t.Fatalf("owned queue depth = %d, want the rejected result queued durably under the owner", owned)
	}
	if got := len(sub.ctxAppendCh) + len(sub.ctxAppendOverflow); got != 1 {
		t.Fatalf("owner context append depth = %d, want only the precondition message (no silent drop, no double delivery)", got)
	}
}

func TestBackgroundCompletionToastLevelFollowsTerminalStatus(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "completed (exit code 0)", want: "info"},
		{status: "failed (exit code 7)", want: "error"},
		{status: "killed (timed out after 120s)", want: "error"},
		{status: "killed (cancelled by job_kill)", want: "warn"},
	}
	for _, tt := range tests {
		if got := backgroundCompletionToastLevel(tt.status); got != tt.want {
			t.Errorf("backgroundCompletionToastLevel(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// TestJobFinishedEventNormalizesMainOwnerAgentID pins that the lightweight
// job-finished notification names the main agent with the shared identity
// instead of its internal instance id, so the TUI's "main" -> "" normalization
// applies uniformly; every other owner keeps its instance id.
func TestJobFinishedEventNormalizesMainOwnerAgentID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: identity.MainAgentID, Payload: backgroundResultPayload(a.instanceID, "job-main", "run tests")})
	if got := lastJobFinishedEvent(t, a); got.AgentID != identity.MainAgentID {
		t.Fatalf("JobFinishedEvent.AgentID = %q, want %q (main owner normalized)", got.AgentID, identity.MainAgentID)
	}

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: identity.MainAgentID, Payload: backgroundResultPayload("worker-owner-1", "job-sub", "run tests")})
	if got := lastJobFinishedEvent(t, a); got.AgentID != "worker-owner-1" {
		t.Fatalf("JobFinishedEvent.AgentID = %q, want the sub-agent owner %q", got.AgentID, "worker-owner-1")
	}
}

func lastJobFinishedEvent(t *testing.T, a *MainAgent) JobFinishedEvent {
	t.Helper()
	var found JobFinishedEvent
	ok := false
	for _, evt := range drainAgentEvents(a.outputCh) {
		if e, isJobFinished := evt.(JobFinishedEvent); isJobFinished {
			found = e
			ok = true
		}
	}
	if !ok {
		t.Fatal("handleJobFinished emitted no JobFinishedEvent")
	}
	return found
}

// TestHandleJobFinishedDropsCrossSessionCompletion pins the session-identity
// guard: a job that finished after a session switch carries its origin session,
// and its result must not be written into the new session's transcript.
func TestHandleJobFinishedDropsCrossSessionCompletion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := backgroundResultPayload(a.instanceID, "job-cross-session-drop", "late build")
	payload.SessionDir = a.SessionDir() + "-previous"
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: payload})

	if a.turn != nil {
		t.Fatal("a completion from another session must not start a turn")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 0 {
		t.Fatalf("len(pendingSubAgentMailboxes) = %d, want 0 for a dropped completion", got)
	}
	// The drop happens before the reported claim, so the result stays claimable
	// rather than being silently marked delivered.
	if !tools.ClaimJobReported(payload.EffectiveID()) {
		t.Fatal("a dropped cross-session completion must remain claimable")
	}
}

func TestHandleJobFinishedDeliversMatchingSessionCompletion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	payload := backgroundResultPayload(a.instanceID, "job-cross-session-match", "build")
	payload.SessionDir = a.SessionDir()
	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: payload})

	if a.turn == nil {
		t.Fatal("a completion from the active session must deliver")
	}
	if got := len(a.pendingSubAgentMailboxes); got != 1 {
		t.Fatalf("len(pendingSubAgentMailboxes) = %d, want 1 for a matching-session completion", got)
	}
}
