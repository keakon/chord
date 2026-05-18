package sessionimport

import (
	"bytes"

	"github.com/keakon/chord/internal/message"
)

// convertCodexRollout converts a Codex rollout JSONL file (typically under
// ~/.codex/sessions/**/rollout-*.jsonl) into a Chord main transcript.
//
// The conversion pipeline:
//  1. Parse JSONL into typed rollout entries
//  2. Build intermediate representation (IR) with turn reconstruction
//     and source-precedence decisions
//  3. Linearize IR into Chord messages with structured tool imports where
//     safe, and readable fallback blocks where not
func convertCodexRollout(data []byte, toolMode string, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errEmptyInput
	}

	// Stage 1: Parse JSONL.
	entries, sessionID, err := parseCodexJSONL(data)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		report.SourceSessionID = sessionID
	}

	// Stage 2: Build IR with turn reconstruction.
	turns, err := buildCodexIR(entries, reasoningMode, report)
	if err != nil {
		return nil, err
	}

	// Stage 3: Linearize IR into Chord messages.
	msgs := linearizeCodexTurns(turns, toolMode, report)
	if err := validateImportedCodexMessages(msgs, report); err != nil {
		return nil, err
	}

	report.ImportedMessages = len(msgs)
	return msgs, nil
}
