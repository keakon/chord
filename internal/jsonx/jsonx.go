// Package jsonx is the repository's canonical JSON encoding entry point. It
// wraps encoding/json/v2 with one fixed option set so every wire-visible
// encoder in the repo produces the same bytes for the same value without each
// call site restating the options.
//
// The option set is chosen to match the v1 API's output byte for byte, with
// two deliberate deviations:
//
//   - HTML characters (<, >, &) are not escaped. Nothing consumes the output
//     as HTML; user-visible surfaces render markdown at most.
//   - RawMessage payloads carrying invalid UTF-8 have those bytes rewritten to
//     U+FFFD. The v1 writer passed them through verbatim; no v2 option
//     reproduces that, and the lenient readers accept the normalized bytes, so
//     the read/write closure holds.
//
// Everything else matches v1: map keys are sorted (Deterministic), nil slices
// and maps marshal as null, duplicate members inside RawMessage pass through
// verbatim, invalid UTF-8 in string fields becomes U+FFFD exactly like v1, and
// the trailing newline of an Encoder is preserved by Encode.
package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
	"io"
	"strings"

	jsonv2 "encoding/json/v2"
)

// canonical options shared by every encoder in this package. Duplicate-name
// and invalid-UTF-8 tolerance only affect RawMessage passthrough and string
// fields exactly as the v1 API behaved.
func canonical() []jsonv2.Options {
	return []jsonv2.Options{
		jsonv2.Deterministic(true),
		jsonv2.FormatNilSliceAsNull(true),
		jsonv2.FormatNilMapAsNull(true),
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
	}
}

// Marshal encodes v as compact JSON. It is the drop-in replacement for
// json.Marshal.
func Marshal(v any) ([]byte, error) {
	return jsonv2.Marshal(v, canonical()...)
}

// MarshalIndent encodes v indented, without a trailing newline — the same
// shape as json.MarshalIndent. The jsontext encoder appends one newline, so it
// is trimmed here.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf, jsontext.WithIndentPrefix(prefix), jsontext.WithIndent(indent))
	if err := jsonv2.MarshalEncode(enc, v, canonical()...); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// Encode writes v as one compact JSON value followed by a newline — the same
// shape as json.NewEncoder(w).Encode(v), with the same default of no HTML
// escaping every encoder in this package shares.
func Encode(w io.Writer, v any) error {
	return jsonv2.MarshalEncode(jsontext.NewEncoder(w), v, canonical()...)
}

// EncodeIndent writes v as one indented JSON value followed by a newline — the
// same shape as json.NewEncoder(w) with SetIndent, sharing the canonical
// option set.
func EncodeIndent(w io.Writer, v any, prefix, indent string) error {
	enc := jsontext.NewEncoder(w, jsontext.WithIndentPrefix(prefix), jsontext.WithIndent(indent))
	return jsonv2.MarshalEncode(enc, v, canonical()...)
}

// EscapeForPrompt escapes the three characters the v1 API escaped on top of
// the JSON requirements. Some sites embed canonically encoded text verbatim
// into prompt content (skill listings, memory reminders); there "<" in
// untrusted content must surface as \u003c so it cannot forge Markdown/XML
// tags in the surrounding text. The canonical encoder output contains these
// characters only as literal string content, so replacing them afterwards is
// exact.
func EscapeForPrompt(encoded []byte) []byte {
	return []byte(promptEscaper.Replace(string(encoded)))
}

var promptEscaper = strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`)
