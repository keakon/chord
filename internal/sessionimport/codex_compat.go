package sessionimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

var errEmptyInput = fmt.Errorf("codex import: empty input")

// normalizeCodexLine extracts the inner item/payload object from a top-level
// rollout JSONL line. This is used by source_lookup.go for session-id
// detection during import-file resolution.
func normalizeCodexLine(lineObj map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if itemRaw := lineObj["item"]; len(bytes.TrimSpace(itemRaw)) != 0 {
		return readJSONAsMap(itemRaw)
	}
	if payloadRaw := lineObj["payload"]; len(bytes.TrimSpace(payloadRaw)) != 0 {
		payloadObj, err := readJSONAsMap(payloadRaw)
		if err != nil {
			return nil, err
		}
		if rawType, ok := lineObj["type"]; ok {
			payloadObj["__event_type"] = rawType
		}
		if _, ok := payloadObj["type"]; !ok {
			if rawType, ok := lineObj["type"]; ok {
				payloadObj["type"] = rawType
			}
		}
		return payloadObj, nil
	}
	return lineObj, nil
}

// extractCodexSessionID extracts a session identifier from a normalized
// Codex item object. Used by source_lookup.go.
func extractCodexSessionID(itemObj map[string]json.RawMessage) (string, bool) {
	if sid, ok := pickFirstStringRaw(itemObj, "session_id", "sessionId", "sessionID", "id"); ok {
		return sid, true
	}
	if payloadRaw, ok := itemObj["payload"]; ok {
		if payloadObj, err := readJSONAsMap(payloadRaw); err == nil {
			if sid, ok := pickFirstStringRaw(payloadObj, "session_id", "sessionId", "sessionID", "id"); ok {
				return sid, true
			}
		}
	}
	return "", false
}

// pickFirstStringRaw returns the first non-empty string value for any of the
// given keys from a map of raw JSON values.
func pickFirstStringRaw(m map[string]json.RawMessage, keys ...string) (string, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s != "" {
				return s, true
			}
		}
	}
	return "", false
}
