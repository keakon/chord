package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func toolBatchMessages(callID, name, status, content string) []message.Message {
	return []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: callID, Name: name, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: callID, ToolStatus: status, Content: content},
	}
}

func appendAll(dst []message.Message, batches ...[]message.Message) []message.Message {
	for _, batch := range batches {
		dst = append(dst, batch...)
	}
	return dst
}

func TestCheckpointRetainedFailureRecordsKeepsOnlyCurrentTurnFailures(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleUser, Content: "first request"},
		{Role: message.RoleAssistant, Content: "older turn analysis"},
	}
	head = appendAll(head, toolBatchMessages("old-call", tools.NameRead, message.ToolStatusError, "read failed"))
	head = appendAll(head,
		[]message.Message{{Role: message.RoleUser, Content: "second request"}},
		toolBatchMessages("ok-call", tools.NameRead, message.ToolStatusSuccess, "file contents"),
		toolBatchMessages("kept-call", tools.NameCompactContext, message.ToolStatusError, "Context checkpoint rejected: claim_kinds \"x\" is observed but has no claim_evidence"),
	)

	got := checkpointRetainedFailureRecords(head)
	if len(got) != 2 {
		t.Fatalf("retained %d records, want the failed batch pair: %+v", len(got), got)
	}
	if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].ID != "kept-call" {
		t.Fatalf("retained assistant record = %+v, want the kept-call tool call", got[0])
	}
	if got[1].Role != message.RoleTool || got[1].ToolCallID != "kept-call" || got[1].ToolStatus != message.ToolStatusError {
		t.Fatalf("retained tool record = %+v, want the kept-call error result", got[1])
	}
}

func TestCheckpointRetainedFailureRecordsCapsToNewestBatches(t *testing.T) {
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	for _, callID := range []string{"call-1", "call-2", "call-3"} {
		head = appendAll(head, toolBatchMessages(callID, tools.NameShell, message.ToolStatusError, "exit status 1"))
	}

	got := checkpointRetainedFailureRecords(head)
	if want := 2 * maxCheckpointRetainedFailureBatches; len(got) != want {
		t.Fatalf("retained %d records, want %d (newest %d batches)", len(got), want, maxCheckpointRetainedFailureBatches)
	}
	if got[0].ToolCalls[0].ID != "call-2" || got[2].ToolCalls[0].ID != "call-3" {
		t.Fatalf("retained batches = %q/%q, want the newest two in order", got[0].ToolCalls[0].ID, got[2].ToolCalls[0].ID)
	}
}

func TestCheckpointRetainedFailureRecordsKeepsWholeParallelBatch(t *testing.T) {
	head := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "ok-call", Name: tools.NameRead, Args: json.RawMessage(`{}`)},
			{ID: "bad-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)},
		}},
		{Role: message.RoleTool, ToolCallID: "ok-call", ToolStatus: message.ToolStatusSuccess, Content: "file contents"},
		{Role: message.RoleTool, ToolCallID: "bad-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
	}

	got := checkpointRetainedFailureRecords(head)
	if len(got) != 3 {
		t.Fatalf("retained %d records, want the whole parallel batch so the replay keeps every tool response: %+v", len(got), got)
	}
	if len(got[0].ToolCalls) != 2 || got[1].ToolCallID != "ok-call" || got[2].ToolCallID != "bad-call" {
		t.Fatalf("retained batch = %+v, want both responses behind the assistant record", got)
	}
}

func TestCheckpointRetainedFailureRecordsDropsIncompleteBatches(t *testing.T) {
	pending := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "pending-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
	}
	if got := checkpointRetainedFailureRecords(pending); got != nil {
		t.Fatalf("an assistant tool call without its response must stay archived, got %+v", got)
	}

	partial := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "bad-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)},
			{ID: "missing-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)},
		}},
		{Role: message.RoleTool, ToolCallID: "bad-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
	}
	if got := checkpointRetainedFailureRecords(partial); got != nil {
		t.Fatalf("a batch truncated by the archive boundary must stay archived, got %+v", got)
	}
}

func TestCheckpointRetainedFailureRecordsStatusVocabulary(t *testing.T) {
	legacyError := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "legacy-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "legacy-call", Content: "Error: command not found"},
	}
	if got := checkpointRetainedFailureRecords(legacyError); len(got) != 2 {
		t.Fatalf("a legacy result whose content reads as an error must be retained, got %+v", got)
	}

	successWithErrorText := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "lucky-call", Name: tools.NameRead, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "lucky-call", ToolStatus: message.ToolStatusSuccess, Content: "func TestX(t *testing.T) { t.Error(\"Error: sample\") }"},
	}
	if got := checkpointRetainedFailureRecords(successWithErrorText); got != nil {
		t.Fatalf("an explicit success must not be retained for error-looking output, got %+v", got)
	}

	cancelled := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "cancelled-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "cancelled-call", ToolStatus: message.ToolStatusCancelled, Content: "cancelled"},
	}
	if got := checkpointRetainedFailureRecords(cancelled); len(got) != 2 {
		t.Fatalf("a cancelled result stays visible like a failure, got %+v", got)
	}
}

// TestModelDrivenCheckpointKeepsFailedToolRecordsLive pins the requirement end
// to end: when the archival checkpoint archives the rejected call, the failed
// batch must remain a real record pair in the live transcript and in
// main.jsonl, directly behind the checkpoint card.
func TestModelDrivenCheckpointKeepsFailedToolRecordsLive(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)

	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "implement the retention"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: strings.Repeat("analysis ", 4000)})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-keep", Name: tools.NameCompactContext, Args: json.RawMessage(`{}`)}}})
	a.ctxMgr.Append(message.Message{
		Role:       message.RoleTool,
		ToolCallID: "call-keep",
		ToolStatus: message.ToolStatusError,
		Content:    "Context checkpoint rejected: claim_kinds \"state\" is observed but has no claim_evidence",
	})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: strings.Repeat("retry ", 4000)})

	snapshot := a.ctxMgr.Snapshot()
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          1000,
		postResetFixedRequestTokens: 4000,
		archiveMeta:                 a.captureCompactionArchiveMeta(),
	}
	req := &modelDrivenCheckpointRequest{
		ToolCallID: "cc-1",
		Args:       tools.CompactContextArgs{ActiveObjective: "keep going", NextStep: "continue"},
	}
	draft, err := a.produceModelDrivenDraftAsync(t.Context(), bundle, 1, compactionTarget{}, len(snapshot), req)
	if err != nil {
		t.Fatalf("produceModelDrivenDraftAsync: %v", err)
	}
	if draft.Skip {
		t.Fatalf("checkpoint unexpectedly skipped: %s", draft.InfoMessage)
	}
	if len(draft.NewMessages) != 3 {
		t.Fatalf("draft carries %d new messages, want the card plus the failed batch: %+v", len(draft.NewMessages), draft.NewMessages)
	}
	if !draft.NewMessages[0].IsCompactionSummary {
		t.Fatal("the checkpoint card must stay the head of the rewritten transcript")
	}
	if len(draft.NewMessages[1].ToolCalls) != 1 || draft.NewMessages[1].ToolCalls[0].ID != "call-keep" {
		t.Fatalf("draft must re-attach the failed assistant record, got %+v", draft.NewMessages[1])
	}
	if draft.NewMessages[2].ToolCallID != "call-keep" || draft.NewMessages[2].ToolStatus != message.ToolStatusError {
		t.Fatalf("draft must re-attach the failed tool result, got %+v", draft.NewMessages[2])
	}

	draft.PlanID = 1
	draft.Target = compactionTarget{sessionEpoch: a.sessionEpoch}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	live := a.ctxMgr.Snapshot()
	if len(live) != 3 {
		t.Fatalf("live transcript has %d messages, want the card plus the failed batch", len(live))
	}
	if !live[0].IsCompactionSummary || live[1].ToolCalls[0].ID != "call-keep" || live[2].ToolStatus != message.ToolStatusError {
		t.Fatalf("live transcript lost the failed batch: %+v", live)
	}
	raw, err := os.ReadFile(filepath.Join(a.sessionDir, identity.MainSessionLogFilename))
	if err != nil {
		t.Fatalf("read rewritten session: %v", err)
	}
	persisted := string(raw)
	for _, want := range []string{`"tool_call_id":"call-keep"`, `"tool_status":"error"`} {
		if !strings.Contains(persisted, want) {
			t.Fatalf("rewritten main.jsonl missing %s:\n%s", want, persisted)
		}
	}
}
