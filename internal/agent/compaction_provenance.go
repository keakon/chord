package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// checkpointSourceRef identifies a message in the immutable source generation
// used to produce a compaction checkpoint. The ordinal is generation-scoped;
// the hash makes the locator verifiable after restore.
type checkpointSourceRef struct {
	SessionID            string `json:"session_id"`
	TranscriptGeneration string `json:"transcript_generation"`
	SegmentKind          string `json:"segment_kind"`
	SegmentID            string `json:"segment_id"`
	Ordinal              int    `json:"ordinal"`
	CanonicalPayloadHash string `json:"canonical_payload_hash"`
	Role                 string `json:"role"`
	ToolCallID           string `json:"tool_call_id,omitempty"`
}

func canonicalMessageHash(msg message.Message) (string, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal message for provenance: %w", err)
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

func buildCheckpointSourceRefs(sessionID, generation, segmentID string, messages []message.Message) ([]checkpointSourceRef, error) {
	refs := make([]checkpointSourceRef, 0, len(messages))
	for ordinal, msg := range messages {
		hash, err := canonicalMessageHash(msg)
		if err != nil {
			return nil, err
		}
		refs = append(refs, checkpointSourceRef{
			SessionID:            sessionID,
			TranscriptGeneration: generation,
			SegmentKind:          "archived_prefix",
			SegmentID:            segmentID,
			Ordinal:              ordinal,
			CanonicalPayloadHash: hash,
			Role:                 string(msg.Role),
			ToolCallID:           msg.ToolCallID,
		})
	}
	return refs, nil
}

func checkpointSourceFingerprint(refs []checkpointSourceRef) string {
	payload, err := json.Marshal(refs)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func validateCheckpointSourceRefs(refs []checkpointSourceRef, messages []message.Message) error {
	if len(refs) != len(messages) {
		return fmt.Errorf("source reference count %d does not cover generation of %d messages", len(refs), len(messages))
	}
	seen := make(map[int]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Ordinal < 0 || ref.Ordinal >= len(messages) {
			return fmt.Errorf("source ordinal %d is outside generation", ref.Ordinal)
		}
		if _, duplicate := seen[ref.Ordinal]; duplicate {
			return fmt.Errorf("source ordinal %d is duplicated", ref.Ordinal)
		}
		seen[ref.Ordinal] = struct{}{}
		msg := messages[ref.Ordinal]
		if string(msg.Role) != ref.Role || msg.ToolCallID != ref.ToolCallID {
			return fmt.Errorf("source ordinal %d identity changed", ref.Ordinal)
		}
		hash, err := canonicalMessageHash(msg)
		if err != nil {
			return err
		}
		if hash != ref.CanonicalPayloadHash {
			return fmt.Errorf("source ordinal %d fingerprint changed", ref.Ordinal)
		}
	}
	return nil
}
