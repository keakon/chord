// Package patchinput normalizes text envelopes around freeform patch input.
package patchinput

import (
	"strings"

	sonicjson "github.com/bytedance/sonic"
)

// MaxEnvelopeLayers bounds the nested string envelopes accepted around a patch.
const MaxEnvelopeLayers = 4

// Patches enter history and TUI caches; copy strings so an extracted value
// cannot retain its whole envelope, and validate external string input.
var envelopeJSON = sonicjson.Config{
	CopyString:     true,
	ValidateString: true,
	UseNumber:      true,
}.Froze()

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
		var obj map[string]any
		if err := envelopeJSON.UnmarshalFromString(trimmed, &obj); err != nil {
			return original
		}
		raw, ok := obj["patch"]
		if !ok {
			raw, ok = obj["input"]
		}
		if !ok {
			return original
		}
		value, ok := raw.(string)
		if !ok {
			return original
		}
		if strings.HasPrefix(strings.TrimSpace(value), "***") {
			return value
		}
		text = value
	}
	return original
}
