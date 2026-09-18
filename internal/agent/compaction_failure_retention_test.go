package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
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

// A rejected compact_context request that a later call of the same tool was
// accepted for is not a failure the checkpoint carries forward: the retry
// decided the outcome, and the retained records are re-attached directly under
// the new checkpoint, where the superseded rejection reads as a checkpoint
// that ran and failed right after one applied.
func TestCheckpointRetainedFailureRecordsDropsSupersededCompactContextRejection(t *testing.T) {
	rejection := toolBatchMessages("rejected-call", tools.NameCompactContext, message.ToolStatusError, `Context checkpoint rejected: compact_context evidence_refs contains unknown evidence ID "derived"`)
	head := appendAll([]message.Message{{Role: message.RoleUser, Content: "request"}},
		rejection,
		toolBatchMessages("accepted-call", tools.NameCompactContext, message.ToolStatusSuccess, "Context checkpoint request accepted"),
	)
	if got := checkpointRetainedFailureRecords(head); len(got) != 0 {
		t.Fatalf("retained %d records, want the superseded rejection dropped: %+v", len(got), got)
	}

	// A later rejection does not supersede the earlier one: without an
	// accepted call, the failure is still what the head settled on.
	unresolved := appendAll([]message.Message{{Role: message.RoleUser, Content: "request"}},
		rejection,
		toolBatchMessages("rejected-again", tools.NameCompactContext, message.ToolStatusError, "Context checkpoint rejected: unknown evidence ID"),
	)
	if got := checkpointRetainedFailureRecords(unresolved); len(got) != 4 {
		t.Fatalf("retained %d records, want both rejections kept: %+v", len(got), got)
	}
}

// At barrier time the accepted retry's result does not exist yet — it is
// deferred to the barrier — so the head holds the retry's declaration alone.
// That in-flight declaration is still the call that replaced the rejection:
// the rejection must not be re-attached under the checkpoint it would read as
// failing right after.
func TestCheckpointRetainedFailureRecordsDropsRejectionSupersededByInFlightRetry(t *testing.T) {
	head := appendAll([]message.Message{{Role: message.RoleUser, Content: "request"}},
		toolBatchMessages("rejected-call", tools.NameCompactContext, message.ToolStatusError, `Context checkpoint rejected: compact_context evidence_refs contains unknown evidence ID "derived"`),
		[]message.Message{{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "accepted-call", Name: tools.NameCompactContext, Args: json.RawMessage(`{}`)}}}},
	)
	if got := checkpointRetainedFailureRecords(head); len(got) != 0 {
		t.Fatalf("retained %d records, want the rejection dropped while the accepted retry is in flight: %+v", len(got), got)
	}
}

// A batch that carries any call besides compact_context keeps its failure: a
// sibling failure is not the checkpoint's own, so nothing about it is
// superseded.
func TestCheckpointRetainedFailureRecordsKeepsBatchWithSiblingFailure(t *testing.T) {
	head := appendAll([]message.Message{{Role: message.RoleUser, Content: "request"}},
		[]message.Message{
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
				{ID: "cc-call", Name: tools.NameCompactContext, Args: json.RawMessage(`{}`)},
				{ID: "read-call", Name: tools.NameRead, Args: json.RawMessage(`{}`)},
			}},
			{Role: message.RoleTool, ToolCallID: "cc-call", ToolStatus: message.ToolStatusError, Content: "Context checkpoint rejected"},
			{Role: message.RoleTool, ToolCallID: "read-call", ToolStatus: message.ToolStatusError, Content: "read failed"},
		},
		toolBatchMessages("accepted-call", tools.NameCompactContext, message.ToolStatusSuccess, "Context checkpoint request accepted"),
	)
	if got := checkpointRetainedFailureRecords(head); len(got) != 3 {
		t.Fatalf("retained %d records, want the batch with the sibling failure kept: %+v", len(got), got)
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
	// The successful sibling keeps its place in the pair but not its body: the
	// archive carries the output, and the retained copy only has to explain
	// where it went. The failure keeps every byte.
	if want := message.FormatToolResultElided(13); got[1].Content != want {
		t.Fatalf("successful sibling content = %q, want %q", got[1].Content, want)
	}
	if got[2].Content != "exit status 1" {
		t.Fatalf("failure content = %q, want the original error text", got[2].Content)
	}
}

func TestCheckpointRetainedFailureRecordsCapsRetainedBytes(t *testing.T) {
	big := strings.Repeat("x", maxCheckpointRetainedFailureBytes+1)
	head := []message.Message{{Role: message.RoleUser, Content: "request"}}
	head = appendAll(head, toolBatchMessages("old-call", tools.NameRead, message.ToolStatusError, big))
	head = appendAll(head, toolBatchMessages("mid-call", tools.NameShell, message.ToolStatusError, big))
	head = appendAll(head, toolBatchMessages("new-call", tools.NameShell, message.ToolStatusError, "exit status 1"))

	got := checkpointRetainedFailureRecords(head)
	if len(got) != 2 || got[0].ToolCalls[0].ID != "new-call" {
		t.Fatalf("retained %d records starting with %q, want the byte cap to drop the older batch and keep the newest", len(got), got[0].ToolCalls[0].ID)
	}

	// The byte cap counts the assistant call's arguments too (matching the
	// preflight surface): a batch whose failure body is small but whose args
	// are huge still costs the cap.
	hugeArgs := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "args-call", Name: tools.NameShell, Args: json.RawMessage(`{"command":"` + strings.Repeat("y", maxCheckpointRetainedFailureBytes) + `"}`)}}},
		{Role: message.RoleTool, ToolCallID: "args-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "fresh-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "fresh-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
	}
	if got := checkpointRetainedFailureRecords(hugeArgs); len(got) != 2 || got[0].ToolCalls[0].ID != "fresh-call" {
		t.Fatalf("a batch dominated by call args must yield to the newest batch, got %+v", got)
	}

	// The newest batch is what the checkpoint is being written for, so it stays
	// live even when it alone exceeds the cap.
	only := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "huge-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "huge-call", ToolStatus: message.ToolStatusError, Content: strings.Repeat("x", 2*maxCheckpointRetainedFailureBytes)},
	}
	if got := checkpointRetainedFailureRecords(only); len(got) != 2 || got[0].ToolCalls[0].ID != "huge-call" {
		t.Fatalf("the newest batch must stay live even over the byte cap, got %+v", got)
	}
}

// TestCheckpointRetainedFailureRecordsCapsImagePayloadBytes pins the byte cap
// against the largest payload class. A failed result keeps its body — that is
// the whole reason the batch stays live — so an image-bearing failure is what
// the cap has to weigh, and it carries its weight in a part rather than in
// Content. A cap that only measured Content would keep both batches and hand
// the projected surface the whole blob.
func TestCheckpointRetainedFailureRecordsCapsImagePayloadBytes(t *testing.T) {
	payload := make([]byte, maxCheckpointRetainedFailureBytes+1)
	for i := range payload {
		payload[i] = byte(i)
	}
	head := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "img-call", Name: tools.NameViewImage, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "img-call", ToolStatus: message.ToolStatusError, Content: "stale", Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "image render failed"},
			{Type: message.ContentPartImage, MimeType: "image/png", FileName: "stale.png", ImagePath: "stale.png", Data: payload, DataBytes: int64(len(payload))},
		}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "fresh-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "fresh-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
	}

	got := checkpointRetainedFailureRecords(head)
	if len(got) != 2 || got[0].ToolCalls[0].ID != "fresh-call" {
		t.Fatalf("retained %d records starting with %+v, want the image-heavy older batch dropped by the byte cap: %+v", len(got), got[0].ToolCalls, got)
	}
}

// TestCheckpointRetainedFailureRecordsElidesSuccessPayloadInKeptBatch pins the
// division of labour between elision and the cap: a successful result sharing
// the batch with a failure keeps its call/response pairing but contributes no
// payload bytes, so a large attachment cannot push the batch over the cap or
// into the projected surface.
func TestCheckpointRetainedFailureRecordsElidesSuccessPayloadInKeptBatch(t *testing.T) {
	payload := make([]byte, maxCheckpointRetainedFailureBytes+1)
	head := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "img-call", Name: tools.NameViewImage, Args: json.RawMessage(`{}`)},
			{ID: "bad-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)},
		}},
		{Role: message.RoleTool, ToolCallID: "img-call", ToolStatus: message.ToolStatusSuccess, Parts: []message.ContentPart{
			{Type: message.ContentPartImage, MimeType: "image/png", FileName: "shot.png", ImagePath: "shot.png", Data: payload, DataBytes: int64(len(payload))},
		}},
		{Role: message.RoleTool, ToolCallID: "bad-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
	}

	got := checkpointRetainedFailureRecords(head)
	if len(got) != 3 {
		t.Fatalf("retained %d records, want the whole batch kept: %+v", len(got), got)
	}
	if bytes := ctxmgr.MessagePayloadBytes(got); bytes > maxCheckpointRetainedFailureBytes {
		t.Fatalf("retained payload bytes = %d, want the elided attachment to keep the batch under the cap (%d)", bytes, maxCheckpointRetainedFailureBytes)
	}
	for _, msg := range got {
		if msg.ToolCallID == "img-call" && len(msg.Parts) != 0 {
			t.Fatalf("the elided success result still carries parts: %+v", msg.Parts)
		}
	}
}

// TestElideRetainedResultDropsBinaryPayloadEntirely pins that no elided
// successful result can still reach the provider with its blob: a part left
// with only ImagePath would be resolved back from disk by binaryPartPayload at
// request time, so elision must drop the parts outright and the message must
// report zero payload bytes to the estimator afterwards.
func TestElideRetainedResultDropsBinaryPayloadEntirely(t *testing.T) {
	payload := []byte{1, 2, 3}
	msg := message.Message{
		Role:       message.RoleTool,
		ToolCallID: "img-call",
		ToolStatus: message.ToolStatusSuccess,
		Content:    "image result",
		Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "here is the image"},
			{Type: message.ContentPartImage, MimeType: "image/png", FileName: "shot.png", ImagePath: "shot.png", Data: payload, DataBytes: int64(len(payload))},
		},
	}

	got := elideRetainedResult(msg)
	if len(got.Parts) != 0 {
		t.Fatalf("elided parts = %+v, want every part dropped so no payload can be resolved from disk", got.Parts)
	}
	// The marker must account for what was removed, including the binary bytes
	// the estimator charged through PayloadBytes.
	wantSize := len("here is the image") + len(payload)
	if want := message.FormatToolResultElided(wantSize); got.Content != want {
		t.Fatalf("elided content = %q, want %q", got.Content, want)
	}
	if bytes := ctxmgr.MessagePayloadBytes([]message.Message{got}); bytes != len(got.Content) {
		t.Fatalf("elided payload bytes = %d, want only the marker text (%d)", bytes, len(got.Content))
	}
	if got.ToolStatus != message.ToolStatusSuccess || got.ToolCallID != "img-call" {
		t.Fatalf("elided record lost its batch identity: %+v", got)
	}
}

// TestElidedToolResultContentReportsRemovedBytesOnce pins the marker's byte
// count against double counting. message.Message defines Content as the
// model-visible combination of the raw payload and the runtime's notes, so a
// structured tool result carries its ToolPayload inside Content; adding both
// would overstate what the elision removed. ToolDiff is a separate field and
// must still be counted.
func TestElidedToolResultContentReportsRemovedBytesOnce(t *testing.T) {
	payload := "structured output"
	diff := "@@ -1 +1 @@"
	msg := message.Message{
		Role:        message.RoleTool,
		ToolCallID:  "edit-call",
		ToolStatus:  message.ToolStatusSuccess,
		Content:     payload + "\n\nretry hint",
		ToolPayload: payload,
		ToolDiff:    diff,
		ToolNotes:   []string{"retry hint"},
	}

	want := len(msg.Content) + len(diff)
	got := elideRetainedResult(msg)
	if expected := message.FormatToolResultElided(want); got.Content != expected {
		t.Fatalf("elided content = %q, want %q (Content already contains ToolPayload)", got.Content, expected)
	}
	if got.ToolPayload != "" || got.ToolDiff != "" {
		t.Fatalf("elided record kept a body copy: payload=%q diff=%q", got.ToolPayload, got.ToolDiff)
	}
	// The notes are the runtime's own diagnosis, not a copy of the output, so
	// they survive the elision the marker describes.
	if len(got.ToolNotes) != 1 {
		t.Fatalf("elided record dropped its runtime notes: %+v", got.ToolNotes)
	}
}

func TestExcludeRetainedFailureEvidenceKeepsArchivedFailuresOnly(t *testing.T) {
	records := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "live-call", Name: tools.NameShell, Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "live-call", ToolStatus: message.ToolStatusError, Content: "exit status 1"},
		{Role: message.RoleTool, ToolCallID: "ok-call", ToolStatus: message.ToolStatusSuccess, Content: "fine"},
	}
	retained := retainedFailureCallIDs(records)
	if len(retained) != 1 {
		t.Fatalf("retainedFailureCallIDs = %v, want only the failed live call", retained)
	}
	if _, ok := retained["live-call"]; !ok {
		t.Fatalf("retainedFailureCallIDs = %v, want the failed call id", retained)
	}

	items := []evidenceItem{
		{Kind: evidenceToolError, SourceID: "live-call"},
		{Kind: evidenceToolError, SourceID: "archived-call"},
	}
	kept := excludeRetainedFailureEvidence(items, retained)
	if len(kept) != 1 || kept[0].SourceID != "archived-call" {
		t.Fatalf("excludeRetainedFailureEvidence = %+v, want the archived failure only", kept)
	}
	if got := excludeRetainedFailureEvidence(items, nil); len(got) != 2 {
		t.Fatalf("without retained records the pack must stay untouched, got %+v", got)
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

// A record an earlier checkpoint already elided must survive a later retention
// pass unchanged: re-eliding would measure the marker itself and shrink the
// reported size to the marker's own length.
func TestElideRetainedResultDoesNotReElideMarker(t *testing.T) {
	msg := message.Message{
		Role:       message.RoleTool,
		ToolCallID: "call-1",
		ToolStatus: message.ToolStatusSuccess,
		Content:    message.FormatToolResultElided(4096),
	}
	got := elideRetainedResult(msg)
	if got.Content != msg.Content {
		t.Fatalf("double elision rewrote %q to %q", msg.Content, got.Content)
	}
}
