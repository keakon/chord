package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/message"
)

func TestCompactionTransactionManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	manifest := compactionTransactionManifest{
		TransactionID:     "7-3",
		ProposalID:        "call-1",
		SourceFingerprint: "source-1",
		ArchivePath:       "history-3.md",
		ArchiveMetaPath:   "history-3.md.status.json",
		TranscriptIndex:   3,
		Status:            compactionTransactionPrepared,
	}
	if err := writeCompactionTransactionManifest(dir, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := updateCompactionTransactionStatus(dir, manifest.TransactionID, compactionTransactionCommitted); err != nil {
		t.Fatalf("update manifest: %v", err)
	}
	data, err := os.ReadFile(compactionTransactionManifestPath(dir, manifest.TransactionID))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(data) == "" || !contains(string(data), `"status": "committed"`) {
		t.Fatalf("manifest does not contain committed status: %s", data)
	}
}

func contains(text, fragment string) bool {
	for i := 0; i+len(fragment) <= len(text); i++ {
		if text[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

// TestApplyCompactionDraftCommitsTransactionOnReplaceSuccess drives a real
// durable apply with a prepared transaction and pins the C3 commit-point
// contract: once ReplacePrefixAtomic succeeds, the transaction manifest is
// recorded committed with the target fingerprint of the live transcript, so a
// later crash reconciles the apply as committed and the archive stays
// referenced.
func TestApplyCompactionDraftCommitsTransactionOnReplaceSuccess(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "first reply"})

	const txnID = "1-1"
	manifest := compactionTransactionManifest{
		TransactionID:   txnID,
		ProposalID:      "call-1",
		ArchivePath:     filepath.Join(a.sessionDir, "history-1.md"),
		ArchiveMetaPath: filepath.Join(a.sessionDir, "history-1.md.status.json"),
		TranscriptIndex: 1,
		Status:          compactionTransactionPrepared,
	}
	if err := writeCompactionTransactionManifest(a.sessionDir, manifest); err != nil {
		t.Fatalf("write prepared manifest: %v", err)
	}

	draft := &compactionDraft{
		NewMessages:           []message.Message{{Role: message.RoleUser, Content: "checkpoint content", IsCompactionSummary: true}},
		HeadSplit:             1,
		Index:                 1,
		AbsHistoryPath:        filepath.Join(a.sessionDir, "history-1.md"),
		AbsHistoryMetaPath:    filepath.Join(a.sessionDir, "history-1.md.status.json"),
		SummaryMode:           compactionSummaryModeModelDriven,
		PlanID:                1,
		Target:                compactionTarget{sessionEpoch: a.sessionEpoch},
		TransactionID:         txnID,
		TransactionSessionDir: a.sessionDir,
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("apply draft: %v", err)
	}

	data, err := os.ReadFile(compactionTransactionManifestPath(a.sessionDir, txnID))
	if err != nil {
		t.Fatalf("read committed manifest: %v", err)
	}
	var recorded compactionTransactionManifest
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatalf("decode committed manifest: %v", err)
	}
	if recorded.Status != compactionTransactionCommitted {
		t.Fatalf("transaction status = %q, want committed", recorded.Status)
	}
	wantFingerprint := compactionTranscriptFingerprint(a.ctxMgr.Snapshot())
	if recorded.TargetFingerprint != wantFingerprint {
		t.Fatalf("target fingerprint = %q, want live transcript %q", recorded.TargetFingerprint, wantFingerprint)
	}
}

// TestApplyCompactionDraftLogsOnlyWhenPostCommitAuditWriteFails pins the
// best-effort tail of the C3 commit point through an independently injectable
// sibling write: once ReplacePrefixAtomic succeeded and the transaction is
// recorded committed, a failing post-commit audit write must only log — the
// apply still returns nil, the runtime settlement still runs, and the
// still-referenced archive is never deleted. The committed manifest update
// itself cannot be fault-injected directly: it shares its manifest file (and
// its atomic rewrite) with the target-fingerprint record written inside the
// ReplacePrefixAtomic callback, so any durable fault hits that earlier write
// first and aborts the whole replace. The history-meta audit write right
// after the commit is the first best-effort write on this path with an
// independent file and pins the same log-only contract.
func TestApplyCompactionDraftLogsOnlyWhenPostCommitAuditWriteFails(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "first reply"})

	// The archive exists and must stay referenced after the apply. Its status
	// path is taken by a directory so the post-commit audit write cannot land.
	historyPath := filepath.Join(a.sessionDir, "history-1.md")
	if err := os.WriteFile(historyPath, []byte("archived head"), 0o600); err != nil {
		t.Fatalf("WriteFile(history): %v", err)
	}
	metaPath := filepath.Join(a.sessionDir, "history-1.md.status.json")
	if err := os.MkdirAll(metaPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(metaPath): %v", err)
	}

	const txnID = "3-1"
	if err := writeCompactionTransactionManifest(a.sessionDir, compactionTransactionManifest{
		TransactionID: txnID, TranscriptIndex: 1, Status: compactionTransactionPrepared,
	}); err != nil {
		t.Fatalf("write prepared manifest: %v", err)
	}

	var buf bytes.Buffer
	logger := logtest.NewLogger(&buf, golog.DebugLevel)
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	draft := &compactionDraft{
		NewMessages:           []message.Message{{Role: message.RoleUser, Content: "checkpoint content", IsCompactionSummary: true}},
		HeadSplit:             1,
		Index:                 1,
		AbsHistoryPath:        historyPath,
		AbsHistoryMetaPath:    metaPath,
		SummaryMode:           compactionSummaryModeModelDriven,
		PlanID:                3,
		Target:                compactionTarget{sessionEpoch: a.sessionEpoch},
		TransactionID:         txnID,
		TransactionSessionDir: a.sessionDir,
	}
	windowBefore := a.compactionWindowGeneration
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("apply must not fail on a post-commit audit write error: %v", err)
	}

	// The committed transaction record was written before the injected fault,
	// so it must read as committed with the live transcript fingerprint.
	data, readErr := os.ReadFile(compactionTransactionManifestPath(a.sessionDir, txnID))
	if readErr != nil {
		t.Fatalf("read committed manifest: %v", readErr)
	}
	var recorded compactionTransactionManifest
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatalf("decode committed manifest: %v", err)
	}
	if recorded.Status != compactionTransactionCommitted {
		t.Fatalf("transaction status = %q, want committed", recorded.Status)
	}
	wantFingerprint := compactionTranscriptFingerprint(a.ctxMgr.Snapshot())
	if recorded.TargetFingerprint != wantFingerprint {
		t.Fatalf("target fingerprint = %q, want live transcript %q", recorded.TargetFingerprint, wantFingerprint)
	}
	// The replace landed and the post-commit settlement still ran: the
	// checkpoint is live, the compaction window advanced, and the apply was
	// reported successful in the logs.
	if got := a.ctxMgr.Snapshot()[0].Content; got != "checkpoint content" {
		t.Fatalf("context head = %q, want the applied checkpoint", got)
	}
	if a.compactionWindowGeneration != windowBefore+1 {
		t.Fatalf("compaction window generation = %d, want %d (settlement ran)", a.compactionWindowGeneration, windowBefore+1)
	}
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("archive must stay referenced after a log-only audit failure: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "failed to update compaction history meta") {
		t.Fatalf("audit write failure must be logged, got:\n%s", logs)
	}
	if !strings.Contains(logs, "context compacted (async)") {
		t.Fatalf("apply must settle to the successful-compaction log, got:\n%s", logs)
	}
}

// TestFailedApplyLeavesTransactionAborted drives the failure side of the
// commit point: when the transcript rewrite itself fails (the backup path is
// taken by a directory), the apply reports an error and the transaction must
// not read as committed — the abort defer records it aborted so a restore
// reconcile and the caller both agree the compaction never landed.
func TestFailedApplyLeavesTransactionAborted(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "first reply"})
	// The live transcript must exist on disk for rewriteSessionAfterCompaction
	// to attempt the rename that this test blocks.
	if err := a.recoveryManager().PersistMessage(identity.MainAgentID, a.ctxMgr.Snapshot()[0]); err != nil {
		t.Fatalf("PersistMessage before compaction: %v", err)
	}

	// A prepared manifest exists, then the rewrite cannot proceed because the
	// backup rename target is blocked by a directory.
	const txnID = "2-1"
	if err := writeCompactionTransactionManifest(a.sessionDir, compactionTransactionManifest{
		TransactionID: txnID, TranscriptIndex: 1, Status: compactionTransactionPrepared,
	}); err != nil {
		t.Fatalf("write prepared manifest: %v", err)
	}
	backupPath := filepath.Join(a.sessionDir, "main.pre-compress-1.jsonl")
	if err := os.MkdirAll(backupPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(backupPath): %v", err)
	}

	draft := &compactionDraft{
		NewMessages:           []message.Message{{Role: message.RoleUser, Content: "checkpoint content", IsCompactionSummary: true}},
		HeadSplit:             1,
		Index:                 1,
		AbsHistoryPath:        filepath.Join(a.sessionDir, "history-1.md"),
		SummaryMode:           compactionSummaryModeModelDriven,
		PlanID:                2,
		Target:                compactionTarget{sessionEpoch: a.sessionEpoch},
		TransactionID:         txnID,
		TransactionSessionDir: a.sessionDir,
	}
	err := a.applyCompactionDraft(draft)
	if err == nil {
		t.Fatal("apply must fail when the transcript rewrite cannot run")
	}

	data, readErr := os.ReadFile(compactionTransactionManifestPath(a.sessionDir, txnID))
	if readErr != nil {
		t.Fatalf("read manifest after failed apply: %v", readErr)
	}
	var recorded compactionTransactionManifest
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if recorded.Status == compactionTransactionCommitted {
		t.Fatal("failed apply must not commit the transaction")
	}
}

func TestReconcilePreparedCompactionTransaction(t *testing.T) {
	dir := t.TempDir()
	messages := []message.Message{{Role: message.RoleUser, Content: "checkpoint"}}
	manifest := compactionTransactionManifest{TransactionID: "tx", TargetFingerprint: compactionTranscriptFingerprint(messages), Status: compactionTransactionPrepared}
	if err := writeCompactionTransactionManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCompactionTransactions(dir, messages); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(compactionTransactionManifestPath(dir, "tx"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"status": "committed"`) {
		t.Fatalf("expected committed: %s", data)
	}
}

func TestReconcileMismatchedCompactionTransactionAborts(t *testing.T) {
	dir := t.TempDir()
	manifest := compactionTransactionManifest{TransactionID: "tx", TargetFingerprint: "other", Status: compactionTransactionPrepared}
	if err := writeCompactionTransactionManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCompactionTransactions(dir, []message.Message{{Role: message.RoleUser, Content: "current"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(compactionTransactionManifestPath(dir, "tx"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), `"status": "aborted"`) {
		t.Fatalf("expected aborted: %s", data)
	}
}
