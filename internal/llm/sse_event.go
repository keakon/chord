package llm

import (
	"bytes"

	jsonv2 "encoding/json/v2"
)

const sseMaxTokenSize = 1024 * 1024

type sseEventTypeEnvelope struct {
	Type string `json:"type"`
}

// sseDataLineTerminatesEvent reports whether a single data line is enough to
// flush a terminal SSE event without waiting for the blank-line event delimiter
// or HTTP EOF. It supports both combined JSON event lines that carry a top-level
// type and standard SSE lines whose event type came from a preceding event: line.
func sseDataLineTerminatesEvent(data []byte, eventTypeHint string, terminalEventTypes map[string]struct{}) bool {
	data = bytes.TrimLeft(data, " \t\r\n")
	if bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	if len(terminalEventTypes) == 0 {
		return false
	}
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	var raw sseEventTypeEnvelope
	// Strict v2 decode of a single {"type":...} field. A failure (or a
	// case-mismatched field name, which v2 leaves empty where v1 matched it
	// case-insensitively) only means the early-flush hint is not taken: the
	// blank-line delimiter and HTTP EOF still finalize the event.
	if err := jsonv2.Unmarshal(data, &raw); err != nil {
		return false
	}
	if raw.Type == "" {
		raw.Type = eventTypeHint
	}
	_, ok := terminalEventTypes[raw.Type]
	return ok
}
