package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	Status            compactionTransactionStatus `json:"status"`
	UpdatedAt         time.Time                   `json:"updated_at"`
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
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal compaction transaction: %w", err)
	}
	tmp, err := os.CreateTemp(sessionDir, ".compaction-txn-*.tmp")
	if err != nil {
		return fmt.Errorf("create compaction transaction temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod compaction transaction: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write compaction transaction: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync compaction transaction: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close compaction transaction: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install compaction transaction: %w", err)
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
