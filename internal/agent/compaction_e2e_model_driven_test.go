package agent

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Session-level end-to-end tests for the model-driven checkpoint chain: real
// checkpoint renders (buildModelDrivenCheckpointSummary + the checkpoint
// envelope) applied through applyCompactionDraft, with new user requests and
// tool results landing between generations. The unit tests already cover the
// merge/render pieces in isolation; these pin the semantics across actual
// durable applies.

// e2eCheckpointRequest builds a compact_context submission carrying the
// machine-carryable typed state.
func e2eCheckpointRequest(active string, decisions, openIssues, evidenceRefs []string, stageID, stageStatus, kind string) *modelDrivenCheckpointRequest {
	return &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: active,
		NextStep:        "continue",
		Decisions:       decisions,
		OpenIssues:      openIssues,
		EvidenceRefs:    evidenceRefs,
		StageID:         stageID,
		StageStatus:     stageStatus,
		CheckpointKind:  kind,
	}}
}

// e2eApplyModelDrivenCheckpoint renders a checkpoint from the live ctxmgr
// snapshot (archiving the first headSplit messages) and durably applies it.
func e2eApplyModelDrivenCheckpoint(t *testing.T, a *MainAgent, index, headSplit int, req *modelDrivenCheckpointRequest) {
	t.Helper()
	snapshot := a.ctxMgr.Snapshot()
	if headSplit <= 0 || headSplit > len(snapshot) {
		t.Fatalf("headSplit %d out of range for %d messages", headSplit, len(snapshot))
	}
	summary := a.buildModelDrivenCheckpointSummary(modelDrivenBarrierSnapshot{snapshot: snapshot}, snapshot, headSplit, req)
	content := buildCompactionCheckpointMessage(summary, nil, compactionSummaryModeModelDriven, nil)
	draft := &compactionDraft{
		NewMessages:    []message.Message{{Role: message.RoleUser, Content: content, IsCompactionSummary: true}},
		HeadSplit:      headSplit,
		Index:          index,
		AbsHistoryPath: filepath.Join(a.sessionDir, fmt.Sprintf("history-%d.md", index)),
		SummaryMode:    compactionSummaryModeModelDriven,
		PlanID:         uint64(index),
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("apply model-driven checkpoint %d: %v", index, err)
	}
}

// e2eTypedStateOf parses the typed carry block out of a checkpoint message.
func e2eTypedStateOf(t *testing.T, checkpoint message.Message) checkpointTypedState {
	t.Helper()
	state, ok := parseCheckpointTypedState(compactionSummaryBody(checkpoint.Content))
	if !ok {
		t.Fatalf("typed state missing from applied checkpoint:\n%s", checkpoint.Content)
	}
	return state
}

// e2eCheckpointAt returns the checkpoint message at the head of the live
// transcript, or fails if the applied checkpoint is not the first message.
func e2eCheckpointAt(t *testing.T, a *MainAgent) message.Message {
	t.Helper()
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) == 0 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("applied checkpoint must be the transcript head, got %d messages", len(snapshot))
	}
	return snapshot[0]
}

// TestE2EModelDrivenThreeRoundChainKeepsTypedState drives three full
// checkpoint build+apply rounds with new user requests and tool results
// landing between generations, and pins that the machine-carryable state
// (decisions, open issues, evidence references) accumulates newest-first
// across real durable applies without a natural-language carry.
func TestE2EModelDrivenThreeRoundChainKeepsTypedState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	appendConversation := func(userText, assistantText, toolText string) {
		a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: userText})
		a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: assistantText})
		if toolText != "" {
			callID := fmt.Sprintf("call-%d", len(a.ctxMgr.Snapshot()))
			a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "", ToolCalls: []message.ToolCall{{ID: callID, Name: tools.NameRead, Args: mustJSONRaw(t, map[string]any{"path": "internal/agent/main.go"})}}})
			a.ctxMgr.Append(message.Message{Role: message.RoleTool, ToolCallID: callID, Content: toolText})
		}
	}

	// Round 1: plan the work.
	appendConversation("implement the context-management optimization", "round 1: scoping the plan", "")
	// Archive the request, keep the assistant reply as tail.
	e2eApplyModelDrivenCheckpoint(t, a, 1, 1, e2eCheckpointRequest(
		"implement the context-management optimization",
		[]string{"d1: unify context reduction and compaction state"}, []string{"o1: verify the carry format"}, []string{"ev-1"},
		"plan", "candidate", "provisional",
	))
	first := e2eTypedStateOf(t, e2eCheckpointAt(t, a))
	if slices.Equal(first.Decisions, []string{"d1: unify context reduction and compaction state"}) == false {
		t.Fatalf("round-1 decisions = %v", first.Decisions)
	}

	// Round 2: a new user request plus a tool result land before the next
	// checkpoint; both get archived into the round-2 head.
	appendConversation("review the typed carry merge", "round 2: reading the merge code", "file read: mergeTypedStateList keeps newest first")
	e2eApplyModelDrivenCheckpoint(t, a, 2, 3, e2eCheckpointRequest(
		"review the typed carry merge",
		[]string{"d2: fresh submission must win the merge"}, []string{"o2: cap disclosure text"}, []string{"ev-2"},
		"plan", "candidate", "provisional",
	))
	second := e2eTypedStateOf(t, e2eCheckpointAt(t, a))
	// The round-1 decision survived the durable apply and re-merged behind the
	// fresh round-2 submission.
	if slices.Equal(second.Decisions, []string{"d2: fresh submission must win the merge", "d1: unify context reduction and compaction state"}) == false {
		t.Fatalf("round-2 decisions = %v", second.Decisions)
	}
	if slices.Equal(second.OpenIssues, []string{"o2: cap disclosure text", "o1: verify the carry format"}) == false {
		t.Fatalf("round-2 open issues = %v", second.OpenIssues)
	}
	if slices.Equal(second.EvidenceRefs, []string{"ev-2", "ev-1"}) == false {
		t.Fatalf("round-2 evidence refs = %v", second.EvidenceRefs)
	}

	// Round 3: same again; the final transcript head must carry every round's
	// state with the newest submission first and no natural-language prior
	// checkpoint section.
	appendConversation("add the e2e regression tests", "round 3: writing session-level tests", "tool result: tests added")
	e2eApplyModelDrivenCheckpoint(t, a, 3, 4, e2eCheckpointRequest(
		"add the e2e regression tests",
		[]string{"d3: session-level tests pin the chain"}, []string{"o3: benchmark guard still missing"}, []string{"ev-3"},
		"impl", "candidate", "provisional",
	))
	final := e2eCheckpointAt(t, a)
	finalState := e2eTypedStateOf(t, final)
	if slices.Equal(finalState.Decisions, []string{"d3: session-level tests pin the chain", "d2: fresh submission must win the merge", "d1: unify context reduction and compaction state"}) == false {
		t.Fatalf("round-3 decisions = %v", finalState.Decisions)
	}
	if slices.Equal(finalState.OpenIssues, []string{"o3: benchmark guard still missing", "o2: cap disclosure text", "o1: verify the carry format"}) == false {
		t.Fatalf("round-3 open issues = %v", finalState.OpenIssues)
	}
	if slices.Equal(finalState.EvidenceRefs, []string{"ev-3", "ev-2", "ev-1"}) == false {
		t.Fatalf("round-3 evidence refs = %v", finalState.EvidenceRefs)
	}
	if finalState.StageID != "impl" || finalState.StageStatus != "candidate" || finalState.Kind != "provisional" {
		t.Fatalf("round-3 stage must be the fresh submission's: %+v", finalState)
	}
	body := compactionSummaryBody(final.Content)
	if strings.Contains(body, priorCheckpointSectionHeading) {
		t.Fatal("no checkpoint generation may carry the prior checkpoint as natural-language Markdown")
	}

	// The tail preserved by the last apply is still live after the checkpoint.
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) < 3 {
		t.Fatalf("live transcript after 3 rounds = %d messages, want the checkpoint plus preserved tail", len(snapshot))
	}
}

// TestE2EModelDrivenLatestRequestOverridesCheckpointedClaim pins the
// authority rule across an apply: after a checkpoint lands on claim B, a user
// correction (switch to C) must become the anchor of the next checkpoint and
// the fresh submission must lead the carried decisions — the old claim is
// history, not current direction.
func TestE2EModelDrivenLatestRequestOverridesCheckpointedClaim(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "compare API A against API B"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "exploration notes for A and B"})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "user chooses plan B after comparing A and B"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "ok, adopting plan B"})
	// Checkpoint on claim B. headSplit 3 archives the exploration including
	// the B decision request; the assistant reply stays as tail.
	e2eApplyModelDrivenCheckpoint(t, a, 1, 3, e2eCheckpointRequest(
		"adopt plan B",
		[]string{"adopt plan B after A/B comparison"}, nil, []string{"ev-b"},
		"explore", "candidate", "provisional",
	))
	firstBody := compactionSummaryBody(e2eCheckpointAt(t, a).Content)
	if !strings.Contains(firstBody, "user chooses plan B after comparing A and B") {
		t.Fatalf("round-1 checkpoint must anchor the B request:\n%s", firstBody)
	}

	// The B claim was checkpointed; then the user hits a wall and switches to
	// C. The next checkpoint must anchor the correction, not the carried B
	// claim, and the fresh C decision must lead the carried list.
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "plan B hit a wall; switch to plan C"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "starting plan C"})
	e2eApplyModelDrivenCheckpoint(t, a, 2, 2, e2eCheckpointRequest(
		"switch to plan C",
		[]string{"abandon plan B; adopt plan C"}, nil, []string{"ev-c"},
		"explore", "candidate", "provisional",
	))
	final := e2eCheckpointAt(t, a)
	body := compactionSummaryBody(final.Content)
	if !strings.Contains(body, "plan B hit a wall; switch to plan C") {
		t.Fatalf("round-2 checkpoint must anchor the live correction:\n%s", body)
	}
	if strings.Contains(body, "user chooses plan B after comparing A and B") {
		t.Fatal("round-2 checkpoint must not re-anchor the superseded B request")
	}
	state := e2eTypedStateOf(t, final)
	if slices.Equal(state.Decisions, []string{"abandon plan B; adopt plan C", "adopt plan B after A/B comparison"}) == false {
		t.Fatalf("decisions must lead with the fresh C submission: %v", state.Decisions)
	}
	if slices.Equal(state.EvidenceRefs, []string{"ev-c", "ev-b"}) == false {
		t.Fatalf("evidence refs must lead with the fresh submission: %v", state.EvidenceRefs)
	}
}

// TestE2EModelDrivenApplyWithQueuedUserInputKeepsItForNextTurn pins the
// full-flow boundary behind TestModelDrivenResumeKeepsQueuedUserMessageForNextTurn:
// a real user message that arrives while the checkpoint is pending must not be
// absorbed by the durable apply — the checkpoint rewrites only the archived
// head, the message stays queued, and nothing in the live transcript carries
// it into the checkpoint turn's continuation.
func TestE2EModelDrivenApplyWithQueuedUserInputKeepsItForNextTurn(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "implement the parser contract"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "parser implementation in progress"})

	// The user types a real follow-up while the checkpoint draft is pending.
	const queued = "commit the pending changes"
	a.pendingUserMessages = []pendingUserMessage{{Content: queued, FromUser: true}}

	// Apply the checkpoint (headSplit 1 archives the original request; the
	// in-flight assistant reply stays live).
	e2eApplyModelDrivenCheckpoint(t, a, 1, 1, e2eCheckpointRequest(
		"implement the parser contract",
		[]string{"keep the parser contract"}, nil, nil,
		"impl", "candidate", "provisional",
	))

	if len(a.pendingUserMessages) != 1 || a.pendingUserMessages[0].Content != queued {
		t.Fatalf("queued user message must stay queued after the apply, got %d", len(a.pendingUserMessages))
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) == 0 || !snapshot[0].IsCompactionSummary {
		t.Fatalf("checkpoint must be the transcript head after the apply, got %d messages", len(snapshot))
	}
	for _, m := range snapshot {
		if m.Role == message.RoleUser && strings.Contains(m.Content, queued) {
			t.Fatal("queued user message must not be appended into the checkpoint turn's live context")
		}
	}
	// The queued request is also not inside the checkpoint body itself: the
	// summary was built from the transcript that predated it.
	if strings.Contains(compactionSummaryBody(snapshot[0].Content), queued) {
		t.Fatal("checkpoint body must not embed a user message that arrived after the summary was built")
	}
}

// TestE2EModelDrivenStageSwitchAcrossUnrelatedTasks pins the stage semantics
// across real applies: a checkpoint that completes one stage must not pin the
// session into that stage — the next, unrelated task's checkpoint replaces
// the stage metadata with its own while the earlier decisions stay carried.
func TestE2EModelDrivenStageSwitchAcrossUnrelatedTasks(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "finish task one"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "task one done"})
	e2eApplyModelDrivenCheckpoint(t, a, 1, 1, e2eCheckpointRequest(
		"finish task one",
		[]string{"task one completed cleanly"}, nil, []string{"ev-1"},
		"task-one", "completed", "committed",
	))
	first := e2eTypedStateOf(t, e2eCheckpointAt(t, a))
	if first.StageID != "task-one" || first.StageStatus != "completed" || first.Kind != "committed" {
		t.Fatalf("round-1 stage = %+v", first)
	}

	// The user moves to an unrelated task. Its checkpoint must carry the
	// completed stage's decision as history but present its own stage as
	// current — a regression would carry task-one/completed forward and read
	// the new task as already finished.
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "start task two, unrelated to task one"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "task two underway"})
	e2eApplyModelDrivenCheckpoint(t, a, 2, 2, e2eCheckpointRequest(
		"start task two",
		[]string{"task two: rewrite the loader"}, nil, []string{"ev-2"},
		"task-two", "candidate", "provisional",
	))
	final := e2eCheckpointAt(t, a)
	state := e2eTypedStateOf(t, final)
	if state.StageID != "task-two" || state.StageStatus != "candidate" || state.Kind != "provisional" {
		t.Fatalf("round-2 stage must come from the fresh submission, got %+v", state)
	}
	// The completed stage's decision survives as history but is no longer the
	// current direction marker.
	if slices.Equal(state.Decisions, []string{"task two: rewrite the loader", "task one completed cleanly"}) == false {
		t.Fatalf("completed-stage decision must still be carried: %v", state.Decisions)
	}
}
