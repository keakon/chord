package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

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

// compactionTransactionFile is one decoded compaction-txn-*.json manifest in a
// session directory. Err carries a per-file read/decode failure so callers
// with different error policies (abort the whole scan vs skip the entry) can
// keep their own handling.
type compactionTransactionFile struct {
	TransactionID string
	Path          string
	Manifest      compactionTransactionManifest
	Err           error
}

// isCompactionTransactionManifestFile reports whether a directory entry name
// is a compaction transaction manifest.
func isCompactionTransactionManifestFile(name string) bool {
	return strings.HasPrefix(name, "compaction-txn-") && strings.HasSuffix(name, ".json")
}

// listCompactionTransactionManifests scans sessionDir for every
// compaction-txn-*.json manifest. ReadDir failures abort the scan; a per-file
// read or decode failure is recorded on the entry's Err field instead of
// aborting, so the three consumers (reconcile, crash-window proof, stale
// sweep) keep their distinct error handling.
func listCompactionTransactionManifests(sessionDir string) ([]compactionTransactionFile, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	var out []compactionTransactionFile
	for _, entry := range entries {
		if entry.IsDir() || !isCompactionTransactionManifestFile(entry.Name()) {
			continue
		}
		path := filepath.Join(sessionDir, entry.Name())
		file := compactionTransactionFile{
			TransactionID: strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "compaction-txn-"), ".json"),
			Path:          path,
		}
		data, err := os.ReadFile(path)
		if err != nil {
			file.Err = err
			out = append(out, file)
			continue
		}
		if err := json.Unmarshal(data, &file.Manifest); err != nil {
			file.Err = fmt.Errorf("decode %s: %w", entry.Name(), err)
			out = append(out, file)
			continue
		}
		out = append(out, file)
	}
	return out, nil
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
	files, err := listCompactionTransactionManifests(sessionDir)
	if err != nil {
		return err
	}
	fingerprint := compactionTranscriptFingerprint(messages)
	for _, file := range files {
		if file.Err != nil {
			return file.Err
		}
		if file.Manifest.Status != compactionTransactionPrepared {
			continue
		}
		status := compactionTransactionAborted
		if file.Manifest.TargetFingerprint != "" && file.Manifest.TargetFingerprint == fingerprint {
			status = compactionTransactionCommitted
		}
		if err := updateCompactionTransactionStatus(sessionDir, file.Manifest.TransactionID, status); err != nil {
			return err
		}
	}
	return nil
}

// modelDrivenCommittedApplyBatch reconciles the model-driven apply crash
// window at restore time. A crash that lands after ReplacePrefixAtomic
// rewrote the transcript — before or after the manifest's committed status
// was recorded — but before the applied transition and its recovery snapshot
// leaves the persisted proposal reading accepted/preparing although the
// checkpoint is the live transcript head: a restore would then misreport the
// applied reset as "requested but not applied" and lose the
// lastModelDrivenApplyBatch anchor. The reconciliation proves the apply from
// durable state alone: a transaction manifest whose target fingerprint
// matches the current transcript, whose proposal ID matches the persisted
// request, and whose transcript head is the model-driven checkpoint this
// transaction wrote. Checkpoints stamp the apply's request batch on the
// message itself, so the anchor is readable without any in-memory state. It
// returns that batch when the evidence is conclusive, and ok=false otherwise:
// a non-model-driven apply, an unmatched fingerprint, or an unknown proposal
// never overrides the persisted record.
func modelDrivenCommittedApplyBatch(sessionDir string, messages []message.Message, proposalID string) (uint64, bool) {
	proposalID = strings.TrimSpace(proposalID)
	if proposalID == "" || len(messages) == 0 {
		return 0, false
	}
	head := messages[0]
	if !head.IsCompactionSummary || head.CompactionSummaryMode != compactionSummaryModeModelDriven || head.RequestBatch == 0 {
		return 0, false
	}
	fingerprint := compactionTranscriptFingerprint(messages)
	files, err := listCompactionTransactionManifests(sessionDir)
	if err != nil {
		return 0, false
	}
	for _, file := range files {
		if file.Err != nil {
			continue
		}
		manifest := file.Manifest
		if manifest.Status != compactionTransactionCommitted || strings.TrimSpace(manifest.ProposalID) != proposalID {
			continue
		}
		if manifest.TargetFingerprint != "" && manifest.TargetFingerprint == fingerprint {
			return head.RequestBatch, true
		}
	}
	return 0, false
}

// removeCompactionTransactionManifest deletes a transaction manifest file,
// logging (never failing) when the removal itself fails. Terminal manifests
// have no further role once the apply settlement is durable: committed
// applies are proven by the target-fingerprint reconciliation at restore, the
// model-driven crash-window fix reads the manifest only while the apply
// settlement is not durable (the case where this removal never ran), and
// reconcileCompactionTransactions only ever flips prepared manifests. Leaving
// the files in place would grow the session directory with every compaction.
func removeCompactionTransactionManifest(sessionDir, transactionID string) {
	if strings.TrimSpace(transactionID) == "" {
		return
	}
	if err := os.Remove(compactionTransactionManifestPath(sessionDir, transactionID)); err != nil && !os.IsNotExist(err) {
		log.Warnf("failed to remove compaction transaction manifest transaction_id=%v error=%v", transactionID, err)
	}
}
