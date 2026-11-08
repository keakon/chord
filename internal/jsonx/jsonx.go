// Package jsonx is the repository's canonical JSON encoding entry point. It
// wraps encoding/json/v2 with one fixed option set so every wire-visible
// encoder in the repo produces the same bytes for the same value without each
// call site restating the options.
//
// Maps are sorted, nil containers encode as null, and invalid UTF-8 in Go
// strings becomes U+FFFD. Raw JSON retains member order, duplicate members,
// number literals, string escapes, and invalid UTF-8 bytes. U+2028 and U+2029
// are escaped in both Go strings and raw JSON.
//
// HTML characters (<, >, &) are not escaped by default. Encoded values embedded
// in prompt text require EscapeForPrompt so untrusted strings cannot introduce
// tags into the surrounding text.
package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
	"io"
	"strings"

	jsonv2 "encoding/json/v2"
)

// Raw JSON may carry LLM arguments or provider replay data. Preserve its string
// spelling and invalid bytes instead of silently rewriting transcript evidence.
func canonical() []jsonv2.Options {
	return []jsonv2.Options{
		jsonv2.Deterministic(true),
		jsonv2.FormatNilSliceAsNull(true),
		jsonv2.FormatNilMapAsNull(true),
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
		jsontext.PreserveRawStrings(true),
		jsontext.EscapeForJS(true),
	}
}

// Marshal encodes v as compact JSON with the canonical option set.
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
