package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
)

type compactionTransactionStatus string

const (
	compactionTransactionPrepared  compactionTransactionStatus = "prepared"
	compactionTransactionCommitted compactionTransactionStatus = "committed"
	compactionTransactionAborted   compactionTransactionStatus = "aborted"
)

type compactionTransactionManifest struct {
	Version           int                         `json:"version"`
	TransactionID     string                      `json:"transaction_id"`
	ProposalID        string                      `json:"proposal_id,omitempty"`
	SourceFingerprint string                      `json:"source_fingerprint,omitempty"`
	ArchivePath       string                      `json:"archive_path"`
	ArchiveMetaPath   string                      `json:"archive_meta_path"`
	TranscriptIndex   int                         `json:"transcript_index"`
	TargetFingerprint string                      `json:"target_fingerprint,omitempty"`
	Status            compactionTransactionStatus `json:"status"`
	UpdatedAt         time.Time                   `json:"updated_at"`
}

func compactionTranscriptFingerprint(messages []message.Message) string {
	h := sha256.New()
	for _, msg := range messages {
		data, _ := json.Marshal(msg)
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{'\n'})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func updateCompactionTransactionTarget(sessionDir, transactionID, targetFingerprint string) error {
	path := compactionTransactionManifestPath(sessionDir, transactionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read compaction transaction: %w", err)
	}
	var manifest compactionTransactionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode compaction transaction: %w", err)
	}
	manifest.TargetFingerprint = targetFingerprint
	return writeCompactionTransactionManifest(sessionDir, manifest)
}

func compactionTransactionManifestPath(sessionDir, transactionID string) string {
	return filepath.Join(sessionDir, "compaction-txn-"+transactionID+".json")
}

func writeCompactionTransactionManifest(sessionDir string, manifest compactionTransactionManifest) error {
	if strings.TrimSpace(manifest.TransactionID) == "" {
		return fmt.Errorf("empty compaction transaction ID")
	}
	manifest.Version = 1
	manifest.UpdatedAt = time.Now()
	path := compactionTransactionManifestPath(sessionDir, manifest.TransactionID)
	// persistJSONAtomically syncs the file contents and the parent directory,
	// so a rename is durable before a crash can land between the transcript
	// replace and the manifest's status update.
	if err := persistJSONAtomically(sessionDir, path, ".compaction-txn", manifest); err != nil {
		return fmt.Errorf("write compaction transaction manifest: %w", err)
	}
	return nil
}

func updateCompactionTransactionStatus(sessionDir, transactionID string, status compactionTransactionStatus) error {
	path := compactionTransactionManifestPath(sessionDir, transactionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read compaction transaction: %w", err)
	}
	var manifest compactionTransactionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode compaction transaction: %w", err)
	}
	manifest.Status = status
	return writeCompactionTransactionManifest(sessionDir, manifest)
}

func reconcileCompactionTransactions(sessionDir string, messages []message.Message) error {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return err
	}
	fingerprint := compactionTranscriptFingerprint(messages)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "compaction-txn-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(sessionDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var manifest compactionTransactionManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("decode %s: %w", entry.Name(), err)
		}
		if manifest.Status != compactionTransactionPrepared {
			continue
		}
		status := compactionTransactionAborted
		if manifest.TargetFingerprint != "" && manifest.TargetFingerprint == fingerprint {
			status = compactionTransactionCommitted
		}
		if err := updateCompactionTransactionStatus(sessionDir, manifest.TransactionID, status); err != nil {
			return err
		}
	}
	return nil
}
