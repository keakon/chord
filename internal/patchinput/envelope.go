// Package patchinput normalizes text envelopes around freeform patch input.
package patchinput

import (
	"encoding/json"
	"strings"
)

// MaxEnvelopeLayers bounds the nested string envelopes accepted around a patch.
const MaxEnvelopeLayers = 4

// UnwrapEnvelope returns the patch inside a complete JSON string envelope using
// patch or input fields. The patch field takes precedence when both exist.
// Invalid, unrelated, or excessively nested input is returned unchanged, so
// repeated normalization never gradually strips an unsupported envelope.
// Patch syntax and file operations still require validation by the caller.
func UnwrapEnvelope(text string) string {
	original := text
	for range MaxEnvelopeLayers {
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "{") {
			return original
		}
		// Unrelated fields may contain numbers outside float64's range.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return original
		}
		raw, ok := obj["patch"]
		if !ok {
			raw, ok = obj["input"]
		}
		if !ok {
			return original
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return original
		}
		if strings.HasPrefix(strings.TrimSpace(value), "***") {
			return value
		}
		text = value
	}
	return original
}
