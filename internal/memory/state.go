package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/privatefs"
)

// ExtractionCheckpoint tracks which session content an extraction pass covered.
// It is machine-owned state, not content: deleting it only allows re-extraction.
// It must never be advanced before content is committed (see commit.go).
// Coverage is tracked per session so multiple frozen sessions can each be
// marked covered without one checkpoint overwriting another.
type ExtractionCheckpoint struct {
	// Sessions records covered fingerprints per session ID.
	Sessions map[string]SessionCoverage `json:"sessions"`
}

// SessionCoverage is one session's extraction coverage.
type SessionCoverage struct {
	SourceFingerprint    string    `json:"source_fingerprint"`
	ProjectedMessages    int       `json:"projected_message_count,omitempty"`
	CompactionGeneration uint64    `json:"compaction_generation,omitempty"`
	ExtractedAt          time.Time `json:"extracted_at"`
}

// Covered reports whether sessionID's current fingerprint is already covered.
func (c *ExtractionCheckpoint) Covered(sessionID, fingerprint string) bool {
	if c == nil || c.Sessions == nil {
		return false
	}
	sc, ok := c.Sessions[sessionID]
	return ok && sc.SourceFingerprint == fingerprint
}

// SetCovered records that sessionID's fingerprint was covered.
func (c *ExtractionCheckpoint) SetCovered(sessionID, fingerprint string, projected int, generation uint64) {
	if c == nil {
		return
	}
	if c.Sessions == nil {
		c.Sessions = make(map[string]SessionCoverage)
	}
	c.Sessions[sessionID] = SessionCoverage{
		SourceFingerprint:    fingerprint,
		ProjectedMessages:    projected,
		CompactionGeneration: generation,
		ExtractedAt:          time.Now().UTC(),
	}
}

// FailureStatus records the last extraction attempt that failed without
// committing content, kept for diagnostics and logs. It is advisory only.
type FailureStatus struct {
	SessionID string    `json:"session_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	FailedAt  time.Time `json:"failed_at"`
}

// ErrCorruptCheckpoint marks a checkpoint file that exists but cannot be
// parsed. The checkpoint is rebuildable machine state, so writers recover by
// starting from an empty one instead of failing forever; the only cost is that
// previously covered sessions may be extracted again.
var ErrCorruptCheckpoint = errors.New("parse extraction checkpoint")

// LoadCheckpoint reads the project's extraction checkpoint; returns (nil, nil)
// when absent.
func LoadCheckpoint(l *Layout) (*ExtractionCheckpoint, error) {
	data, err := os.ReadFile(l.CheckpointPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read extraction checkpoint: %w", err)
	}
	var cp ExtractionCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorruptCheckpoint, err)
	}
	return &cp, nil
}

// SaveCheckpoint atomically writes the extraction checkpoint. It is the final
// step of a memory commit (see commit.go) and is never advanced when content
// commit failed. Coverage is merged per session so saving one session never
// erases another session's coverage entries. An unparsable existing file is
// replaced rather than propagated: it would otherwise fail every commit from
// here on, including the retries, with no way to recover.
func SaveCheckpoint(l *Layout, cp *ExtractionCheckpoint) error {
	existing, err := LoadCheckpoint(l)
	if err != nil && !errors.Is(err, ErrCorruptCheckpoint) {
		return err
	}
	if existing == nil {
		existing = &ExtractionCheckpoint{}
	}
	if existing.Sessions == nil {
		existing.Sessions = make(map[string]SessionCoverage)
	}
	for sid, sc := range cp.Sessions {
		existing.Sessions[sid] = sc
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal extraction checkpoint: %w", err)
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(filepath.Dir(l.CheckpointPath), "checkpoint.tmp")
	if err := privatefs.WriteFileSynced(l.StateDir, tmpPath, data); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write extraction checkpoint: %w", err)
	}
	if err := os.Rename(tmpPath, l.CheckpointPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install extraction checkpoint: %w", err)
	}
	return privatefs.SyncDir(filepath.Dir(l.CheckpointPath))
}

// LoadFailure reads the last recorded extraction failure; returns (nil, nil)
// when absent.
func LoadFailure(l *Layout) (*FailureStatus, error) {
	path := filepath.Join(l.StateDir, "last-failure.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory failure status: %w", err)
	}
	var f FailureStatus
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse memory failure status: %w", err)
	}
	return &f, nil
}

// SaveFailure records the most recent failed extraction attempt.
func SaveFailure(l *Layout, sessionID string, err error) {
	f := FailureStatus{SessionID: sessionID, FailedAt: time.Now()}
	if err != nil {
		f.Error = err.Error()
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	data = append(data, '\n')
	path := filepath.Join(l.StateDir, "last-failure.json")
	tmpPath := path + ".tmp"
	if werr := privatefs.WriteFileSynced(l.StateDir, tmpPath, data); werr != nil {
		_ = os.Remove(tmpPath)
		return
	}
	_ = os.Rename(tmpPath, path)
}
