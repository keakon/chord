package agent

import (
	"os"
	"testing"

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
