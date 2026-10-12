package tui

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/patchinput"
)

func editPatchFromArgs(argsJSON string) string {
	var parsed struct {
		Patch string `json:"patch"`
	}
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil {
		return ""
	}
	// A freeform call the model wrapped in a JSON envelope reaches here as
	// {"patch":"{\"input\":\"*** Begin Patch...\"}"}; unwrap so the preview
	// shows the patch, never the envelope (see patchinput.UnwrapEnvelope).
	return strings.TrimSpace(patchinput.UnwrapEnvelope(parsed.Patch))
}

// applyPatchStreamingPreview best-effort extracts the patch text from
// still-streaming apply_patch args. Two shapes arrive here:
//
//   - JSON function shape: an incrementally received `{"patch":"..."}`
//     document that a full JSON parse cannot read until the stream closes, so
//     this locates the patch key (the document may be pretty-printed or carry
//     other keys first), decodes the escapes that have arrived so far, and
//     stops at the first unescaped closing quote.
//   - Freeform custom-tool shape: the args accumulate as bare patch text
//     (custom_tool_call_input.delta), which is shown as-is.
//
// It returns "" until recognizable text has streamed in, letting callers fall
// back to the path-only display. A decoded value that is itself a JSON
// envelope is decoded incrementally, including while it is incomplete.
func applyPatchStreamingPreview(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if strings.HasPrefix(trimmed, "{") {
		seg, ok := streamingPatchValueSegment(trimmed, "patch")
		if !ok {
			return ""
		}
		decoded := decodeStreamingPatchString(seg)
		if strings.HasPrefix(strings.TrimSpace(decoded), "{") {
			return freeformApplyPatchStreamingPreview(decoded)
		}
		return decoded
	}
	// Freeform custom-tool shape: bare patch text, no JSON envelope. Any
	// other payload (e.g. a gateway-lowered {"input":...} object) does not
	// start like a patch and stays hidden until args complete.
	if strings.HasPrefix(trimmed, "*** ") {
		if patch := sanitizeToolDisplayText(trimmed); patch != "" {
			return patch
		}
	}
	return ""
}

// freeformApplyPatchStreamingPreview extracts patch text from bare input or
// nested patch/input string envelopes without requiring their closing quotes
// and braces. This is display-only; execution uses complete JSON normalization.
func freeformApplyPatchStreamingPreview(text string) string {
	for layer := 0; layer <= patchinput.MaxEnvelopeLayers; layer++ {
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "*** ") {
			return sanitizeToolDisplayText(text)
		}
		if layer == patchinput.MaxEnvelopeLayers || !strings.HasPrefix(trimmed, "{") {
			return ""
		}
		seg, ok := streamingPatchValueSegment(trimmed, "patch")
		if !ok && !strings.Contains(trimmed, `"patch"`) {
			seg, ok = streamingPatchValueSegment(trimmed, "input")
		}
		if !ok {
			return ""
		}
		text = decodeStreamingPatchString(seg)
	}
	return ""
}

// streamingPatchValueSegment locates the value of a field in a
// still-streaming JSON object and returns the interior of its string value
// (after the opening quote). ok is false while the value has not started
// streaming yet or is not a string.
func streamingPatchValueSegment(argsJSON, field string) (string, bool) {
	quotedField := `"` + field + `"`
	for from := 0; ; {
		key := strings.Index(argsJSON[from:], quotedField)
		if key < 0 {
			return "", false
		}
		from += key + len(quotedField)
		seg := strings.TrimLeft(argsJSON[from:], " \t\r\n")
		if seg == "" {
			return "", false
		}
		if seg[0] != ':' {
			// The "patch" text appeared inside another string value; keep
			// scanning for the actual key.
			continue
		}
		seg = strings.TrimLeft(seg[1:], " \t\r\n")
		if seg == "" || seg[0] != '"' {
			// Not a string value (e.g. {"patch":123}); keep the caller's fallback.
			return "", false
		}
		return seg[1:], true
	}
}

// decodeStreamingPatchString decodes a still-streaming JSON string interior
// into patch text.
func decodeStreamingPatchString(seg string) string {
	var b strings.Builder
	b.Grow(len(seg))
	// JSON string escaping: \n / \t / \r / \" / \\ / \/ / \b / \f decode to
	// their literals and \uXXXX to the rune (surrogate pairs combine). A
	// trailing lone backslash is an incomplete escape.
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c == '"':
			// An unescaped quote closes the patch value (or an unterminated
			// tail); everything after it is not part of the patch.
			i = len(seg)
		case c == '\\':
			if i+1 >= len(seg) {
				i = len(seg)
				continue
			}
			i++
			switch seg[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'u':
				if n, ok := decodeJSONUnicodeEscape(seg, i); ok {
					if n >= 0xD800 && n <= 0xDBFF && i+11 <= len(seg) && seg[i+5] == '\\' && seg[i+6] == 'u' {
						if lo, loOK := decodeJSONUnicodeEscape(seg, i+6); loOK && lo >= 0xDC00 && lo <= 0xDFFF {
							b.WriteRune(rune(0x10000 + (n-0xD800)<<10 + (lo - 0xDC00)))
							i += 10
							continue
						}
					}
					if n < 0xD800 || n > 0xDFFF {
						b.WriteRune(rune(n))
						i += 4
						continue
					}
				}
				b.WriteString(`\u`)
			default:
				b.WriteByte('\\')
				b.WriteByte(seg[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	patch := strings.TrimSpace(b.String())
	if patch == "" {
		return ""
	}
	return sanitizeToolDisplayText(patch)
}

// decodeJSONUnicodeEscape decodes the 4 hex digits of the `\uXXXX` escape
// whose backslash sits at seg[i]. ok is false when fewer than 4 characters
// have streamed in or they are not hex.
func decodeJSONUnicodeEscape(seg string, i int) (rune, bool) {
	if i+5 > len(seg) {
		return 0, false
	}
	n, err := strconv.ParseUint(seg[i+1:i+5], 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(n), true
}
