package llm

import (
	"bytes"
	"encoding/json"
)

type sseEventTypeEnvelope struct {
	Type string `json:"type"`
}

// sseDataLineTerminatesEvent reports whether a single data line is enough to
// flush a terminal SSE event without waiting for the blank-line event delimiter
// or HTTP EOF. It supports both combined JSON event lines that carry a top-level
// type and standard SSE lines whose event type came from a preceding event: line.
func sseDataLineTerminatesEvent(data []byte, eventTypeHint string, terminalEventTypes map[string]struct{}) bool {
	if bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	if len(terminalEventTypes) == 0 {
		return false
	}
	if eventTypeHint != "" {
		if _, ok := terminalEventTypes[eventTypeHint]; !ok {
			return false
		}
		var payload json.RawMessage
		return json.Unmarshal(data, &payload) == nil
	}
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	var raw sseEventTypeEnvelope
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := terminalEventTypes[raw.Type]
	return ok
}
