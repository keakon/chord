package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
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

	// The manifest was consumed by the settled apply: a terminal transaction
	// record has no further crash-reconciliation role once the apply
	// settlement is durable, so it is removed instead of accumulating. The
	// apply itself is proven by the live transcript, which now leads with the
	// applied checkpoint.
	if _, err := os.Stat(compactionTransactionManifestPath(a.sessionDir, txnID)); !os.IsNotExist(err) {
		t.Fatalf("committed transaction manifest must be removed after a settled apply, stat err = %v", err)
	}
	if got := a.ctxMgr.Snapshot()[0].Content; got != "checkpoint content" {
		t.Fatalf("context head = %q, want the applied checkpoint", got)
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

	// The commit point landed before the injected fault and the settled apply
	// then removed the terminal manifest. The removal itself is plain file
	// cleanup on the same best-effort post-commit tail and must not fail the
	// apply either.
	if _, err := os.Stat(compactionTransactionManifestPath(a.sessionDir, txnID)); !os.IsNotExist(err) {
		t.Fatalf("committed transaction manifest must be removed after the settled apply, stat err = %v", err)
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

	// The failed apply records the abort and removes the terminal manifest
	// (reconcile only ever flips prepared manifests, so an aborted record has
	// no further role); what matters is that the transaction never reads
	// committed.
	if _, err := os.Stat(compactionTransactionManifestPath(a.sessionDir, txnID)); !os.IsNotExist(err) {
		t.Fatalf("aborted transaction manifest must be removed, stat err = %v", err)
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

// TestModelDrivenCommittedApplyBatchReconcilesCrashWindows drives the two
// crash shapes of a model-driven apply: (A) the transcript rewrite recorded
// its target fingerprint inside the replace critical section but the
// committed status never made it to disk — the restore reconcile flips the
// prepared manifest to committed and the fix must then settle the proposal —
// and (B) the committed status was written but the applied transition and its
// recovery snapshot never ran. Both must reconcile to the request batch the
// checkpoint stamped, while unprovable states (wrong proposal, mismatched
// fingerprint, non-model-driven head) must never override the persisted
// record.
func TestModelDrivenCommittedApplyBatchReconcilesCrashWindows(t *testing.T) {
	dir := t.TempDir()
	head := message.Message{Role: message.RoleUser, Content: "checkpoint", IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven, RequestBatch: 7}
	tail := message.Message{Role: message.RoleUser, Content: "tail request"}
	messages := []message.Message{head, tail}
	fingerprint := compactionTranscriptFingerprint(messages)

	// Crash shape A: prepared manifest with a matching target fingerprint.
	if err := writeCompactionTransactionManifest(dir, compactionTransactionManifest{
		TransactionID: "1-1", ProposalID: "call-9", TargetFingerprint: fingerprint, Status: compactionTransactionPrepared,
	}); err != nil {
		t.Fatalf("write prepared manifest: %v", err)
	}
	if err := reconcileCompactionTransactions(dir, messages); err != nil {
		t.Fatalf("reconcile prepared manifest: %v", err)
	}
	if batch, ok := modelDrivenCommittedApplyBatch(dir, messages, "call-9"); !ok || batch != 7 {
		t.Fatalf("crash shape A = batch %d ok %v, want 7 true", batch, ok)
	}

	// Crash shape B: already committed with a matching fingerprint.
	if err := writeCompactionTransactionManifest(dir, compactionTransactionManifest{
		TransactionID: "2-1", ProposalID: "call-9", TargetFingerprint: fingerprint, Status: compactionTransactionCommitted,
	}); err != nil {
		t.Fatalf("write committed manifest: %v", err)
	}
	if batch, ok := modelDrivenCommittedApplyBatch(dir, messages, "call-9"); !ok || batch != 7 {
		t.Fatalf("crash shape B = batch %d ok %v, want 7 true", batch, ok)
	}

	// Non-conclusive states stay untouched.
	if _, ok := modelDrivenCommittedApplyBatch(dir, messages, "call-other"); ok {
		t.Fatal("mismatched proposal id must not reconcile")
	}
	mismatchDir := t.TempDir()
	if err := writeCompactionTransactionManifest(mismatchDir, compactionTransactionManifest{
		TransactionID: "3-1", ProposalID: "call-9", TargetFingerprint: "other-transcript", Status: compactionTransactionCommitted,
	}); err != nil {
		t.Fatalf("write mismatched manifest: %v", err)
	}
	if _, ok := modelDrivenCommittedApplyBatch(mismatchDir, messages, "call-9"); ok {
		t.Fatal("fingerprint mismatch must not reconcile")
	}
	usageHead := []message.Message{{Role: message.RoleUser, Content: "usage summary", IsCompactionSummary: true, CompactionSummaryMode: message.CompactionSummaryModeModelSummary}, tail}
	if _, ok := modelDrivenCommittedApplyBatch(mismatchDir, usageHead, "call-9"); ok {
		t.Fatal("non-model-driven transcript head must not reconcile")
	}
}

// TestReconcileLoadedModelDrivenCrashWindow pins the restore-side wiring of
// the crash-window fix: a pre-apply proposal record whose committed
// transaction matches the loaded transcript is upgraded to applied with the
// checkpoint-stamped request batch, while unprovable or already-terminal
// records are left exactly as loaded.
func TestReconcileLoadedModelDrivenCrashWindow(t *testing.T) {
	dir := t.TempDir()
	head := message.Message{Role: message.RoleUser, Content: "checkpoint", IsCompactionSummary: true, CompactionSummaryMode: compactionSummaryModeModelDriven, RequestBatch: 9}
	tail := message.Message{Role: message.RoleUser, Content: "tail"}
	messages := []message.Message{head, tail}
	if err := writeCompactionTransactionManifest(dir, compactionTransactionManifest{
		TransactionID: "1-1", ProposalID: "call-1", TargetFingerprint: compactionTranscriptFingerprint(messages), Status: compactionTransactionCommitted,
	}); err != nil {
		t.Fatalf("write committed manifest: %v", err)
	}

	loaded := &loadedSessionState{
		SessionPath: dir,
		Messages:    messages,
		ModelDrivenProposal: &recovery.ModelDrivenProposalSnapshot{
			RequestID: "call-1",
			Status:    modelDrivenProposalPreparing,
			Reason:    "preparing durable checkpoint",
			ArgsJSON:  `{"active_objective":"x"}`,
		},
	}
	reconcileLoadedModelDrivenCrashWindow(loaded, dir)
	proposal := loaded.ModelDrivenProposal
	if proposal.Status != modelDrivenProposalApplied {
		t.Fatalf("reconciled proposal status = %q, want applied", proposal.Status)
	}
	if proposal.ArgsJSON != "" {
		t.Fatalf("reconciled proposal must clear the audit args copy, got %q", proposal.ArgsJSON)
	}
	if proposal.Reason == "" {
		t.Fatal("reconciled proposal must record the reconciliation reason")
	}
	if loaded.LastModelDrivenApplyBatch != 9 {
		t.Fatalf("restored apply batch = %d, want the checkpoint-stamped batch 9", loaded.LastModelDrivenApplyBatch)
	}

	// An unprovable proposal (unknown request id) stays untouched.
	other := &loadedSessionState{
		SessionPath: dir,
		Messages:    messages,
		ModelDrivenProposal: &recovery.ModelDrivenProposalSnapshot{
			RequestID: "call-unknown",
			Status:    modelDrivenProposalAccepted,
			ArgsJSON:  `{"active_objective":"y"}`,
		},
	}
	reconcileLoadedModelDrivenCrashWindow(other, dir)
	if other.ModelDrivenProposal.Status != modelDrivenProposalAccepted || other.ModelDrivenProposal.ArgsJSON == "" {
		t.Fatalf("unprovable proposal must stay untouched: %+v", other.ModelDrivenProposal)
	}

	// An already-terminal record is never re-opened or downgraded.
	terminal := &loadedSessionState{
		SessionPath:               dir,
		Messages:                  messages,
		LastModelDrivenApplyBatch: 9,
		ModelDrivenProposal: &recovery.ModelDrivenProposalSnapshot{
			RequestID: "call-1",
			Status:    modelDrivenProposalApplied,
			Reason:    "durable checkpoint applied",
		},
	}
	reconcileLoadedModelDrivenCrashWindow(terminal, dir)
	if terminal.ModelDrivenProposal.Status != modelDrivenProposalApplied {
		t.Fatalf("terminal proposal must stay applied: %q", terminal.ModelDrivenProposal.Status)
	}
}

// TestCleanupStalePendingCompactionsSweepsTransactionManifests pins the
// manifest side of the stale sweep: old terminal manifests and old prepared
// manifests whose archive is gone are removed, while a prepared manifest that
// still has its archive (a possibly in-flight worker) survives.
func TestCleanupStalePendingCompactionsSweepsTransactionManifests(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-10 * time.Minute)
	fresh := time.Now()
	writeTxn := func(transactionID string, status compactionTransactionStatus, updatedAt time.Time, historyPath string) string {
		t.Helper()
		path := compactionTransactionManifestPath(dir, transactionID)
		data, err := json.Marshal(compactionTransactionManifest{
			TransactionID: transactionID, Status: status, UpdatedAt: updatedAt, ArchivePath: historyPath,
		})
		if err != nil {
			t.Fatalf("marshal txn: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write txn: %v", err)
		}
		return path
	}
	staleCommitted := writeTxn("1-1", compactionTransactionCommitted, old, "")
	staleAborted := writeTxn("2-1", compactionTransactionAborted, old, "")
	stalePreparedNoArchive := writeTxn("3-1", compactionTransactionPrepared, old, "")
	livePrepared := writeTxn("4-1", compactionTransactionPrepared, fresh, "")
	archivePath := filepath.Join(dir, "history-5.md")
	if err := os.WriteFile(archivePath, []byte("archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	oldPreparedWithArchive := writeTxn("5-1", compactionTransactionPrepared, old, archivePath)
	committedWithArchive := writeTxn("6-1", compactionTransactionCommitted, old, archivePath)

	cleanupStalePendingCompactions(dir, 5*time.Minute)

	for _, path := range []string{staleCommitted, staleAborted, stalePreparedNoArchive, committedWithArchive} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale transaction manifest must be swept: %s err=%v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{livePrepared, oldPreparedWithArchive} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("prepared manifest must survive while it may be in flight: %s err=%v", filepath.Base(path), err)
		}
	}
}
