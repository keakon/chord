package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestModelDrivenProgressSinceCheckpoint(t *testing.T) {
	checkpoint := message.Message{Role: message.RoleUser, IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven, RequestBatch: 5}
	retry := []message.Message{
		{Role: message.RoleAssistant, RequestBatch: 6, ToolCalls: []message.ToolCall{testToolCall("checkpoint-1", tools.NameCompactContext)}},
		{Role: message.RoleTool, ToolCallID: "checkpoint-1", Content: "skipped", ToolStatus: message.ToolStatusError},
		{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "continue"},
		{Role: message.RoleUser, Kind: message.KindContextNotice, Content: "context pressure"},
	}
	tests := []struct {
		name string
		tail []message.Message
		want bool
	}{
		{name: "empty"},
		{name: "checkpoint retry", tail: retry},
		{name: "user input", tail: []message.Message{{Role: message.RoleUser, Content: "new request"}}, want: true},
		{name: "attachment", tail: []message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartImage}}}}, want: true},
		{name: "background result", tail: []message.Message{{Role: message.RoleUser, Kind: message.KindBackgroundResult, Content: "job failed"}}, want: true},
		{name: "mailbox", tail: []message.Message{{Role: message.RoleUser, Kind: message.KindSubAgentMailbox, Content: "task result"}}, want: true},
		{name: "hook feedback", tail: []message.Message{{Role: message.RoleUser, Kind: message.KindHookFeedback, Content: "verification required"}}, want: true},
		{name: "assistant analysis", tail: []message.Message{{Role: message.RoleAssistant, Content: "new finding", RequestBatch: 6}}, want: true},
		{name: "new tool", tail: []message.Message{{Role: message.RoleAssistant, RequestBatch: 6, ToolCalls: []message.ToolCall{testToolCall("read-1", tools.NameRead)}}}, want: true},
		{name: "unknown result", tail: []message.Message{{Role: message.RoleTool, ToolCallID: "unknown", Content: "result"}}, want: true},
		{name: "retained failure", tail: []message.Message{
			{Role: message.RoleAssistant, RequestBatch: 4, ToolCalls: []message.ToolCall{testToolCall("read-1", tools.NameRead)}},
			{Role: message.RoleTool, ToolCallID: "read-1", Content: "failed", ToolStatus: message.ToolStatusError},
		}},
		{name: "unclassified historical call", tail: []message.Message{{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{testToolCall("read-1", tools.NameRead)}}}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := append([]message.Message{checkpoint}, test.tail...)
			if got := modelDrivenHasProgressSinceCheckpoint(snapshot); got != test.want {
				t.Fatalf("progress = %v, want %v", got, test.want)
			}
		})
	}
	if !modelDrivenHasProgressSinceCheckpoint(nil) {
		t.Fatal("missing checkpoint must not suppress a request")
	}
	if !modelDrivenHasProgressSinceCheckpoint([]message.Message{checkpoint, {IsCompactionSummary: true, CompactionSummaryMode: message.CompactionSummaryModeModelSummary}}) {
		t.Fatal("another compaction mode must invalidate the duplicate boundary")
	}
}

func TestModelDrivenDuplicateAllowsNewWorkWithUnchangedArgs(t *testing.T) {
	args := tools.CompactContextArgs{ActiveObjective: "verify parser", NextStep: "run tests"}
	request := &modelDrivenCheckpointRequest{Args: args, ArgsFingerprint: modelDrivenArgsFingerprint(args)}
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                             []message.Message{{IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}},
		runtimeStateFingerprint:              "runtime-state",
		lastModelDrivenCheckpointFingerprint: modelDrivenCheckpointFingerprint(request.ArgsFingerprint, "runtime-state"),
	}
	if !modelDrivenCheckpointDuplicate(bundle, request) {
		t.Fatal("identical state without new work must be skipped")
	}
	changed := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{ActiveObjective: "verify parser", NextStep: "inspect results"}}
	if modelDrivenCheckpointDuplicate(bundle, changed) {
		t.Fatal("changed continuation state must make a checkpoint eligible")
	}
	bundle.snapshot = append(bundle.snapshot, message.Message{Role: message.RoleUser, Content: "also verify invalid input"})
	if modelDrivenCheckpointDuplicate(bundle, request) {
		t.Fatal("already-consumed user input must make a checkpoint eligible")
	}
	bundle.snapshot = bundle.snapshot[:1]
	bundle.queuedUserMessages = []message.Message{{Role: message.RoleUser, Content: "new request"}}
	if modelDrivenCheckpointDuplicate(bundle, request) {
		t.Fatal("queued input must make a checkpoint eligible")
	}
}

func TestModelDrivenSummaryPrioritizesRequestAndNextAction(t *testing.T) {
	agent := newTestMainAgent(t, t.TempDir())
	request := &modelDrivenCheckpointRequest{Args: tools.CompactContextArgs{
		ActiveObjective: "verify parser", Completed: []string{"parser implemented"},
		OpenIssues: []string{"invalid input unverified"}, NextStep: "run parser tests",
	}}
	bundle := modelDrivenBarrierSnapshot{snapshot: []message.Message{{Role: message.RoleUser, Content: "verify parser"}}}
	summary := agent.buildModelDrivenCheckpointSummary(bundle, bundle.snapshot, len(bundle.snapshot), request)
	previous := -1
	for _, heading := range []string{"## Current User Request", "## User Constraints", "## Active Objective", "## Next Step", "## Open Problems", "## Progress"} {
		position := strings.Index(summary, heading)
		if position <= previous || strings.Count(summary, heading) != 1 {
			t.Fatalf("section %q must appear once in continuation priority order", heading)
		}
		previous = position
	}
	if !strings.Contains(summary, "model-declared Active Objective are subordinate") {
		t.Fatal("model state must not outrank the current user request")
	}
}

func TestModelDrivenApplyContinuesAfterStageCandidate(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "remaining work"
		if background {
			name = "background result"
		}
		t.Run(name, func(t *testing.T) {
			agent := newTestMainAgent(t, t.TempDir())
			agent.newTurn()
			agent.requestBatches.reserve(agent.sessionEpoch, 0)
			agent.stageCompletionCandidatePending = true
			agent.stageCompletionCandidateTurnID = agent.turn.ID
			if err := agent.UpdateTodos([]tools.TodoItem{{ID: "task-1", Content: "verify parser", Status: "in_progress"}}); err != nil {
				t.Fatal(err)
			}
			agent.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "verify parser"})
			callID := "checkpoint-1"
			agent.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{testToolCall(callID, tools.NameCompactContext)}})
			if _, err := agent.tryArmModelDrivenCheckpoint(callID, `{"active_objective":"verify parser","next_step":"run tests"}`); err != nil {
				t.Fatal(err)
			}
			target := compactionTarget{turnID: agent.turn.ID, turnEpoch: agent.turn.Epoch, sessionEpoch: agent.sessionEpoch}
			agent.startCompactionState(1, target, compactionTriggerModelDriven, continuationPlan{kind: compactionResumeModelDriven, turnID: agent.turn.ID, turnEpoch: agent.turn.Epoch})
			pending := agent.currentCompactionPendingCall()
			bundle := agent.captureModelDrivenBarrierSnapshot(agent.ctxMgr.Snapshot())
			if background {
				agent.pendingUserMessages = []pendingUserMessage{{Content: "Background job failed verification", Kind: message.KindBackgroundResult}}
			}
			draft := &compactionDraft{
				SummaryMode: compactionSummaryModeModelDriven, RuntimeGeneration: bundle.currentRequestBatch,
				RuntimeStateFingerprint: bundle.runtimeStateFingerprint, ModelDrivenArgsFingerprint: agent.pendingModelDriven.ArgsFingerprint,
				HeadSplit: len(bundle.snapshot), Index: 1, PlanID: 1, Target: target,
				AbsHistoryPath: filepath.Join(agent.sessionDir, "history-1.md"),
				NewMessages:    []message.Message{{Role: message.RoleUser, Content: "checkpoint summary", IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven}},
			}
			if err := agent.applyCompactionDraft(draft); err != nil {
				t.Fatal(err)
			}
			agent.resetCompactionState()
			if !agent.resumePendingMainLLMAfterCompaction(pending, true) || agent.turn == nil {
				t.Fatal("applied checkpoint must continue the turn")
			}
			if !strings.Contains(agent.pendingModelDrivenNotice, "continue the current task") {
				t.Fatal("continuation must remain scheduled")
			}
			if background {
				snapshot := agent.ctxMgr.Snapshot()
				if len(agent.pendingUserMessages) != 0 || snapshot[len(snapshot)-1].Kind != message.KindBackgroundResult {
					t.Fatal("background result must reach the continuing model request")
				}
			}
		})
	}
}
