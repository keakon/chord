package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// TestAppendDeferredModelDrivenToolResultCarriesFullMessageShape pins that the
// deferred emission path writes the same durable tool-message fields as the
// normal batch path: it used to hand-assemble a thin message and silently drop
// the payload/notes split, diff counters, audit, LSP reviews and provenance.
func TestAppendDeferredModelDrivenToolResultCarriesFullMessageShape(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	a.ctxMgr.Append(message.Message{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{testToolCall("cc-1", tools.NameCompactContext)},
		Provenance: &message.MessageProvenance{ModelRef: "test/provider"},
	})
	payload := &ToolResultPayload{
		CallID:      "cc-1",
		Name:        tools.NameCompactContext,
		ArgsJSON:    "{}",
		Result:      "accepted",
		Payload:     "tool output",
		Notes:       []string{"a note"},
		Diff:        "--- a\n+++ b",
		DiffAdded:   3,
		DiffRemoved: 1,
		Duration:    1500 * time.Millisecond,
		Audit:       &message.ToolArgsAudit{EditSummary: "user tightened the args"},
		LSPReviews:  []message.LSPReview{{Path: "internal/agent/main.go"}},
		FileState:   &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "internal/agent/main.go"}}},
	}
	a.appendDeferredModelDrivenToolResult(payload, "accepted", nil, false)

	snapshot := a.ctxMgr.Snapshot()
	last := snapshot[len(snapshot)-1]
	if last.Role != message.RoleTool || last.ToolCallID != "cc-1" {
		t.Fatalf("deferred append must write the tool message, got %+v", last)
	}
	if last.ToolPayload != "tool output" || len(last.ToolNotes) != 1 {
		t.Fatalf("payload/notes split lost: payload=%q notes=%v", last.ToolPayload, last.ToolNotes)
	}
	if last.ToolDiffAdded != 3 || last.ToolDiffRemoved != 1 {
		t.Fatalf("diff counters lost: +%d -%d", last.ToolDiffAdded, last.ToolDiffRemoved)
	}
	if last.Audit == nil || last.Audit.EditSummary != "user tightened the args" {
		t.Fatalf("tool args audit lost: %+v", last.Audit)
	}
	if len(last.LSPReviews) != 1 {
		t.Fatalf("LSP reviews lost: %v", last.LSPReviews)
	}
	if last.Provenance == nil || last.Provenance.ModelRef != "test/provider" {
		t.Fatalf("provenance lost: %+v", last.Provenance)
	}
	if last.ToolDurationMs != 1500 || last.FileState == nil || len(last.FileState.Reads) != 1 {
		t.Fatalf("duration/file state lost: %d %+v", last.ToolDurationMs, last.FileState)
	}
}

// TestModelDrivenResumeMergesQueuedUserMessageIntoContinuation pins the
// boundary between a model-driven checkpoint and real user messages that
// arrived while the checkpoint was pending: the queued messages are the
// current request, so the continuation runs on the compacted context with
// them merged into its single first request in arrival order (context summary
// first, then each fresh user message). The model acts on the messages
// immediately instead of finishing the checkpoint's archived objective first,
// and no "continue the current task" instruction is appended on top of the
// fresh input.
func TestModelDrivenResumeMergesQueuedUserMessageIntoContinuation(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	// The apply landed: the slot is free and the compacted context is live,
	// with the checkpoint summary as its first message.
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})

	// Two real user messages arrived while the checkpoint was pending, in
	// arrival order.
	a.pendingUserMessages = []pendingUserMessage{
		{Content: "commit the pending changes", FromUser: true},
		{Content: "then update the changelog draft", FromUser: true},
	}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("the continuation must keep running in the checkpoint's own turn")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("queued user messages must all merge into the continuation request, got %d queued", len(a.pendingUserMessages))
	}
	if notice := a.pendingModelDrivenNotice; notice != "" {
		t.Fatalf("merged user input leads the continuation, so no 'continue the current task' notice may be appended, got %q", notice)
	}
	if resume := a.snapshotPendingCompactionResume(); resume != nil {
		t.Fatalf("merged user input leads the continuation, so no durable resume may be armed, got %#v", resume)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("continuation context must read [summary, first, second], got %d messages", len(snapshot))
	}
	if !snapshot[0].IsCompactionSummary {
		t.Fatalf("compaction summary must stay the first message of the continuation context, got %+v", snapshot[0])
	}
	if snapshot[1].Role != message.RoleUser || !strings.Contains(snapshot[1].Content, "commit the pending changes") {
		t.Fatalf("first queued user message must directly follow the summary, got %+v", snapshot[1])
	}
	if snapshot[2].Role != message.RoleUser || !strings.Contains(snapshot[2].Content, "then update the changelog draft") {
		t.Fatalf("second queued user message must follow the first in arrival order, got %+v", snapshot[2])
	}
}

// TestModelDrivenResumeWithEmptyQueueAppendsContinueInstruction pins the
// apply-success resume with no queued input: the compacted transcript ends in
// the summary message itself, so the continuation must append the "continue
// the current task" instruction — otherwise the model could read the summary
// as a finished reply and stop without continuing the task.
func TestModelDrivenResumeWithEmptyQueueAppendsContinueInstruction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "continue the current task") {
		t.Fatalf("an empty queue must not suppress the 'continue the current task' instruction, got %q", notice)
	}
	// The instruction above dies with the process, so the same intent must be
	// recoverable from the session snapshot until this continuation's response
	// lands.
	resume := a.snapshotPendingCompactionResume()
	if resume == nil || resume.Kind != string(compactionResumeAutoContinue) {
		t.Fatalf("an empty-queue continuation must arm a durable auto-continue resume, got %#v", resume)
	}
	if resume.Mode != compactionResumeModeSyntheticContinue || resume.AwaitUserInput {
		t.Fatalf("resume = %#v, want an unconditional synthetic continue", resume)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 1 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("an empty-queue resume must leave the compacted context unchanged, got %d messages", len(snapshot))
	}
}

func TestModelDrivenTerminalCandidateContinuesAfterApply(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{
		kind:      compactionResumeModelDriven,
		turnID:    turnID,
		turnEpoch: a.turn.Epoch,
	})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("checkpoint must preserve its continuation")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("terminal checkpoint apply must own the idle barrier")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("checkpoint must keep the turn alive for remaining work or the final response")
	}
	if notice := a.pendingModelDrivenNotice; !strings.Contains(notice, "continue the current task") {
		t.Fatalf("checkpoint must schedule a continuation notice, got %q", notice)
	}
}

func TestModelDrivenTerminalCandidateContinuesForFreshUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{
		kind:      compactionResumeModelDriven,
		turnID:    turnID,
		turnEpoch: a.turn.Epoch,
	})
	pending := a.currentCompactionPendingCall()
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{{Content: "new request", FromUser: true}}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("terminal checkpoint with fresh input must resume the turn")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("fresh user input must keep the checkpoint turn alive")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("fresh user input must merge into the continuation, got %d queued", len(a.pendingUserMessages))
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 2 || !strings.Contains(snapshot[1].Content, "new request") {
		t.Fatalf("fresh user input must be visible in continuation context, got %+v", snapshot)
	}
}

// TestModelDrivenResumeBackgroundCompletionDoesNotSuppressContinueInstruction
// pins the mergedUserInput criterion on its least obvious false positive: a
// queued background-task completion is FromUser=false and drains into the
// continuation as context, but it is not a fresh user request, so it must not
// suppress the "continue the current task" instruction (the queue-length
// heuristic this guards against used to treat any drained entry as merged
// user input).
func TestModelDrivenResumeBackgroundCompletionDoesNotSuppressContinueInstruction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{
		{Content: "Background task bg-1 finished (success).", Kind: message.KindBackgroundResult},
	}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("the background completion must drain into the continuation, got %d queued", len(a.pendingUserMessages))
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "continue the current task") {
		t.Fatalf("a system-generated entry must not suppress the 'continue the current task' instruction, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 2 || !strings.Contains(snapshot[1].Content, "Background task bg-1") {
		t.Fatalf("background completion must append as context after the summary, got %d messages", len(snapshot))
	}
}

// TestModelDrivenResumeKeepsIdleOnlySlashQueuedForIdleDrain pins that an
// idle-only slash command (/new, /resume*, /loop*, /mcp*) queued while the
// checkpoint was pending is not merged into the continuation: the drain
// re-queues it for the next idle drain (it is only valid while idle), nothing
// is appended, and the "continue the current task" instruction stays so the
// continuation request is not left to end on the bare summary.
func TestModelDrivenResumeKeepsIdleOnlySlashQueuedForIdleDrain(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{{Content: "/new", FromUser: true}}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("a successful model-driven apply must handle its own resume barrier")
	}
	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != "/new" {
		t.Fatalf("an idle-only slash command must stay queued for the idle drain, got %v", a.pendingUserMessages)
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "continue the current task") {
		t.Fatalf("an idle-only slash command must not suppress the 'continue the current task' instruction, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 1 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("an idle-only slash command must not be appended to the continuation context, got %d messages", len(snapshot))
	}
}

// TestModelDrivenResumeAfterFailedApplyMergesQueuedUserInput pins the
// recheckGate=false resume (model-driven skip/failure): the checkpoint did not
// apply, but the same turn still continues automatically on the old context,
// so queued user messages merge into its first request exactly as on the
// applied path — they are the current request and must not trail the
// continuation's reply. The notice explaining why the checkpoint was not
// applied is still appended on top.
func TestModelDrivenResumeAfterFailedApplyMergesQueuedUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "original request"})
	target := compactionTarget{turnID: turnID, turnEpoch: a.turn.Epoch, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(11, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending model-driven call")
	}
	a.resetCompactionState()
	a.pendingUserMessages = []pendingUserMessage{
		{Content: "commit the pending changes", FromUser: true},
		{Content: "then update the changelog draft", FromUser: true},
	}

	if !a.resumePendingMainLLMAfterCompaction(pending, false) {
		t.Fatal("a failed model-driven apply must still resume its own turn")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("queued user messages must merge into the failed-apply continuation, got %d queued", len(a.pendingUserMessages))
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "Context checkpoint not applied") {
		t.Fatalf("the failed-apply continuation must carry the non-apply notice, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("failed-apply continuation context must read [request, first, second], got %d messages", len(snapshot))
	}
	if snapshot[1].Role != message.RoleUser || !strings.Contains(snapshot[1].Content, "commit the pending changes") {
		t.Fatalf("first queued user message must follow the request, got %+v", snapshot[1])
	}
	if snapshot[2].Role != message.RoleUser || !strings.Contains(snapshot[2].Content, "then update the changelog draft") {
		t.Fatalf("second queued user message must follow the first in arrival order, got %+v", snapshot[2])
	}
}

// TestModelDrivenResumeAfterDiscardPreservesQueuedUserInput locks the
// discard/cancel resume asymmetry on purpose: a discarded checkpoint is a user
// abort, so the restarted request must NOT merge queued user messages — they
// wait for the next explicit round instead of auto-running behind a
// continuation the user just stopped. This is the mirror image of the
// failed-apply resume, which merges because nothing user-initiated was
// aborted there.
func TestModelDrivenResumeAfterDiscardPreservesQueuedUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	turnID := a.turn.ID
	a.ctxMgr.Append(message.Message{
		Role:                  message.RoleUser,
		Content:               "checkpoint summary",
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	})
	a.pendingUserMessages = []pendingUserMessage{{Content: "user follow-up after cancel", FromUser: true}}
	plan := continuationPlan{kind: compactionResumeModelDriven, turnID: turnID, turnEpoch: a.turn.Epoch, agentErrSourceID: "main"}

	if !a.resumeModelDrivenTurnAfterDiscard(plan) {
		t.Fatal("a discarded model-driven checkpoint must resume the deferred turn")
	}
	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != "user follow-up after cancel" {
		t.Fatalf("queued user input must stay queued after a discard, got %v", a.pendingUserMessages)
	}
	if notice := a.pendingModelDrivenNotice; notice == "" || !strings.Contains(notice, "Context checkpoint not applied") {
		t.Fatalf("the discard resume must still carry the non-apply notice, got %q", notice)
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 1 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("the discard resume must not append queued user input to the context, got %d messages", len(snapshot))
	}
}
