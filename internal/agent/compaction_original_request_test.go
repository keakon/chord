package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestCaptureOriginalFirstUserHintPrefersRecoverableMessageOverPollutedSummary(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	if a.usageLedger == nil {
		t.Fatal("newTestMainAgent should initialize usageLedger")
	}
	if err := a.usageLedger.RewriteFirstUserMessageWithOriginalForCompaction("[Context Summary]\n## Goal\n- compacted", ""); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginalForCompaction polluted summary: %v", err)
	}
	original := "Original user request from disk"
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "user", Content: original}); err != nil {
		t.Fatalf("PersistMessage original user: %v", err)
	}
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "assistant", Content: "ack"}); err != nil {
		t.Fatalf("PersistMessage assistant: %v", err)
	}

	if got := a.captureOriginalFirstUserHint(); got != original {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want %q", got, original)
	}
}

// A transcript head that is a real user prompt outranks a cached preview that
// names a prompt the user replaced. The ee tail edit on the session's first
// message rewrites the head in place; trusting the cache would write the
// deleted prompt into the checkpoint's "Original request:" anchor.
func TestCaptureOriginalFirstUserHintPrefersTranscriptHeadOverStalePreview(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	deleted := []message.Message{{Role: "user", Content: "DELETED prompt"}}
	a.ctxMgr.RestoreMessages(deleted)
	if err := a.recoveryManager().RewriteLog("main", deleted); err != nil {
		t.Fatalf("RewriteLog(deleted): %v", err)
	}
	if err := a.usageLedger.SetFirstUserMessage("DELETED prompt"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	if got := a.usageLedger.OriginalFirstUserMessage(); got != "DELETED prompt" {
		t.Fatalf("precondition: ledger OriginalFirstUserMessage = %q, want the deleted prompt", got)
	}

	// The tail edit rewrites the transcript to the corrected prompt.
	corrected := []message.Message{{Role: "user", Content: "CORRECTED prompt"}}
	a.ctxMgr.RestoreMessages(corrected)
	if err := a.recoveryManager().RewriteLog("main", corrected); err != nil {
		t.Fatalf("RewriteLog(corrected): %v", err)
	}

	if got := a.captureOriginalFirstUserHint(); got != "CORRECTED prompt" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want the corrected prompt", got)
	}
}

// Once the history starts with a checkpoint the head carries no user prompt, so
// re-deriving the original from the transcript would name a mid-session prompt
// instead. This fixture's checkpoint predates the anchors block, so the cached
// original is the only survivor and must win; a checkpoint that does carry
// anchors outranks the cache — see
// TestCaptureOriginalFirstUserHintFallsBackToCheckpointAnchor.
func TestCaptureOriginalFirstUserHintKeepsCachedValueAfterCompaction(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	original := []message.Message{{Role: "user", Content: "ORIGINAL request"}}
	a.ctxMgr.RestoreMessages(original)
	if err := a.recoveryManager().RewriteLog("main", original); err != nil {
		t.Fatalf("RewriteLog(original): %v", err)
	}
	if err := a.usageLedger.SetFirstUserMessage("ORIGINAL request"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}

	compacted := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- carry on", IsCompactionSummary: true},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}

	if got := a.captureOriginalFirstUserHint(); got != "ORIGINAL request" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want the cached original request", got)
	}
}

// checkpointWithAnchor builds the checkpoint message a compaction writes for
// the given original request: the verbatim anchors block plus a summary body.
func checkpointWithAnchor(originalRequest string) message.Message {
	summary := withCompactionAnchors(
		"[Context Summary]\n## Goal\n- carry on",
		compactionAnchors{OriginalRequest: originalRequest},
	)
	return message.Message{Role: "user", Content: summary, IsCompactionSummary: true}
}

// A compacted history starts with a checkpoint, so neither transcript scan can
// name the original request any more: FirstUserMessageFromFile and the snapshot
// scan both skip the checkpoint and return the first prompt *after* it. The
// checkpoint's own anchors block is the durable record — written once, while
// the real head was still observable, and copied forward verbatim ever after —
// so it has to be consulted before those two steps are reached.
func TestCaptureOriginalFirstUserHintFallsBackToCheckpointAnchor(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	compacted := []message.Message{
		checkpointWithAnchor("REAL original request"),
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}
	if got := a.usageLedger.OriginalFirstUserMessage(); got != "" {
		t.Fatalf("precondition: ledger original = %q, want empty", got)
	}

	if got := a.captureOriginalFirstUserHint(); got != "REAL original request" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want the checkpoint anchor", got)
	}
}

// The checkpoint anchor outranks the cached preview. A summary rebuild can lose
// the cached original (older versions did), and adopting a rebuilt preview can
// even overwrite it with a mid-session prompt, while the anchor is the verbatim
// copy the first compaction made — so the anchor is what keeps the checkpoint's
// "Original request:" correct when the two disagree.
func TestCaptureOriginalFirstUserHintPrefersCheckpointAnchorOverCachedPreview(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	compacted := []message.Message{
		checkpointWithAnchor("REAL original request"),
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}
	// The polluted cache a pre-fix summary rebuild leaves behind: the original
	// preview was re-derived from the transcript and named a mid-session prompt.
	// Record it the way that rebuild did — as both the preview and the original.
	if err := a.usageLedger.RewriteFirstUserMessageWithOriginalForCompaction("mid-session prompt", "mid-session prompt"); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginalForCompaction: %v", err)
	}
	if got := a.usageLedger.OriginalFirstUserMessage(); got != "mid-session prompt" {
		t.Fatalf("precondition: ledger original = %q, want the mid-session prompt", got)
	}

	if got := a.captureOriginalFirstUserHint(); got != "REAL original request" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want the checkpoint anchor", got)
	}
}

// A checkpoint that lost its anchors block leaves no record of the original
// request anywhere, and the transcript behind it cannot supply one: its first
// user-authored message is a mid-session prompt. Returning that would freeze it
// into the next checkpoint's "Original request:" anchor, which every later
// checkpoint then copies forward verbatim — so no answer is better than a guess.
func TestCaptureOriginalFirstUserHintIgnoresTranscriptBehindCheckpoint(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	compacted := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- carry on", IsCompactionSummary: true},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}
	if got := a.usageLedger.OriginalFirstUserMessage(); got != "" {
		t.Fatalf("precondition: ledger original = %q, want empty", got)
	}

	if got := a.captureOriginalFirstUserHint(); got != "" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want empty: the transcript behind a checkpoint holds no original request", got)
	}
}

// A summary written before OriginalFirstUserMessage existed is still a record:
// its preview was captured while the head was observable, so it stays the last
// resort for a history that has since been compacted with a checkpoint that
// carries no anchors. Only a preview the ledger derived by scanning is refused,
// and a rebuild never promotes a preview to the original on its own.
func TestCaptureOriginalFirstUserHintKeepsRecordedPreviewWhenCheckpointLostAnchors(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	compacted := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- carry on", IsCompactionSummary: true},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}
	// The pre-OFUM summary shape, seeded stale so the next open rebuilds it: a
	// rebuild carries the preview over but never promotes it to the original.
	sessionID := filepath.Base(a.sessionDir)
	summary := fmt.Sprintf(`{"session_id":%q,"last_event_id":"stale-event","first_user_message":"the recorded first request","status":"active"}`+"\n", sessionID)
	if err := os.WriteFile(filepath.Join(a.sessionDir, "usage-summary.json"), []byte(summary), 0o600); err != nil {
		t.Fatalf("WriteFile(usage-summary.json): %v", err)
	}
	event := fmt.Sprintf(`{"event_id":"event-1","session_id":%q,"agent_id":"main","purpose":"chat","running_model_ref":"provider-a/model-1"}`+"\n", sessionID)
	if err := os.WriteFile(filepath.Join(a.sessionDir, "usage.jsonl"), []byte(event), 0o600); err != nil {
		t.Fatalf("WriteFile(usage.jsonl): %v", err)
	}

	if got := a.captureOriginalFirstUserHint(); got != "the recorded first request" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want the recorded preview", got)
	}
}

func TestRewriteSessionAfterCompactionPreservesOriginalFirstUserMessage(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	if err := recovery.SaveSessionMeta(a.sessionDir, recovery.SessionMeta{Title: "Custom feature title"}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	// Set up initial first user message via usage ledger
	if a.usageLedger == nil {
		t.Fatal("newTestMainAgent should initialize usageLedger")
	}
	if err := a.usageLedger.SetFirstUserMessage("Original user request for feature X"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}

	// Simulate compaction creating a summary message
	summaryContent := "[Context Summary]\n## Goal\n- continue\n\n[Context compressed]"
	compactionMsg := message.Message{
		Role:                "user",
		Content:             summaryContent,
		IsCompactionSummary: true,
	}

	// Rewrite session with compaction summary as first message
	_, err := a.rewriteSessionAfterCompaction(1, []message.Message{compactionMsg}, "", nil)
	if err != nil {
		t.Fatalf("rewriteSessionAfterCompaction: %v", err)
	}

	// Verify OriginalFirstUserMessage is preserved
	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != "Original user request for feature X" {
		t.Errorf("OriginalFirstUserMessage = %q, want %q", summary.OriginalFirstUserMessage, "Original user request for feature X")
	}
	// FirstUserMessage should be updated to reflect compaction summary
	if summary.FirstUserMessage == "" {
		t.Error("FirstUserMessage should not be empty after compaction")
	}
	if !summary.FirstUserMessageIsCompactionSummary {
		t.Error("FirstUserMessageIsCompactionSummary = false, want true after compaction")
	}
	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || meta.Title != "Custom feature title" {
		t.Fatalf("session title after compaction = %+v, want unchanged custom title", meta)
	}
}

// A compaction apply must never adopt a tail prompt as the session's original
// request. The hint the caller captured before the rewrite is the only source
// that can still name it: once the session has been compacted the pre-rename
// transcript starts with a checkpoint, and the messages being written start
// with this compaction's own checkpoint, so a "first user-authored message"
// scan on either one names the first prompt *after* the checkpoint. An
// original request is sticky, so such a value would outlive every later
// compaction.
func TestRewriteSessionAfterCompactionNeverAdoptsTailPromptAsOriginal(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	// The live transcript was already compacted, and nothing carries the
	// session's original request: no ledger preview, no checkpoint anchors.
	compacted := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- earlier round", IsCompactionSummary: true},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}

	// A second compaction applies over it with an empty hint, mirroring the
	// draft shape the apply path builds: this compaction's checkpoint first,
	// the preserved tail after it.
	nextCheckpoint := message.Message{
		Role:                "user",
		Content:             "[Context Summary]\n## Goal\n- second round",
		IsCompactionSummary: true,
	}
	applied := []message.Message{
		nextCheckpoint,
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	if _, err := a.rewriteSessionAfterCompaction(1, applied, "", nil); err != nil {
		t.Fatalf("rewriteSessionAfterCompaction: %v", err)
	}

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != "" {
		t.Fatalf("OriginalFirstUserMessage = %q, want empty: a tail prompt is not the original request", summary.OriginalFirstUserMessage)
	}
	if !summary.FirstUserMessageIsCompactionSummary {
		t.Fatalf("FirstUserMessageIsCompactionSummary = false, want true (FirstUserMessage = %q)", summary.FirstUserMessage)
	}
}

// After a compaction that could not name the original request, a later user
// prompt still must not fill it. The ledger already refuses that promotion;
// the in-memory summary is what tail-edit hints and the terminal title read.
func TestRecordCommittedUserMessageDoesNotFillUnknownOriginal(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	compacted := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- earlier round", IsCompactionSummary: true},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	a.ctxMgr.RestoreMessages(compacted)
	if err := a.recoveryManager().RewriteLog("main", compacted); err != nil {
		t.Fatalf("RewriteLog(compacted): %v", err)
	}
	nextCheckpoint := message.Message{
		Role:                "user",
		Content:             "[Context Summary]\n## Goal\n- second round",
		IsCompactionSummary: true,
	}
	applied := []message.Message{
		nextCheckpoint,
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "mid-session prompt"},
	}
	if _, err := a.rewriteSessionAfterCompaction(1, applied, "", nil); err != nil {
		t.Fatalf("rewriteSessionAfterCompaction: %v", err)
	}

	a.recordCommittedUserMessage(message.Message{Role: "user", Content: "later prompt"})

	got := a.GetSessionSummary()
	if got == nil {
		t.Fatal("GetSessionSummary() = nil")
	}
	if got.OriginalFirstUserMessage != "" {
		t.Fatalf("in-memory original = %q, want empty: a later prompt is not the original request", got.OriginalFirstUserMessage)
	}
	if got := a.usageLedger.OriginalFirstUserMessage(); got != "" {
		t.Fatalf("ledger original = %q, want empty", got)
	}
	if got := a.captureOriginalFirstUserHint(); got != "" {
		t.Fatalf("captureOriginalFirstUserHint() = %q, want empty", got)
	}
}
