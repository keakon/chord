// Package schemaargs checks Chord argument and result schema subsets.
package schemaargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// DecodeArguments decodes one argument document, preserving number literals and
// recording shadowed duplicate values when ignored is non-nil. It rejects
// trailing input and deeply nested values before schema validation.
func DecodeArguments(args json.RawMessage, ignored *[]message.IgnoredToolArg) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	value, err := decodeSchemaJSONValue(dec, "args", ignored, 0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("arguments must be valid JSON")
		}
		return nil, fmt.Errorf("decode arguments: %w", err)
	}
	// The token stream accepts a prefix; reject trailing garbage so a document
	// like `{"a":1} extra` is invalid instead of silently dropping the tail.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("arguments must be valid JSON")
	}

	return value, nil
}

// maxSchemaJSONDepth bounds recursive descent through nested JSON values.
// encoding/json rejects documents nested deeper than its own limit; the token
// stream decoder has no built-in bound, so a maliciously deep document would
// otherwise overflow the stack here.
const maxSchemaJSONDepth = 1000

func decodeSchemaJSONValue(dec *json.Decoder, path string, ignored *[]message.IgnoredToolArg, depth int) (any, error) {
	if depth > maxSchemaJSONDepth {
		return nil, fmt.Errorf("arguments nested deeper than %d levels", maxSchemaJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}

	switch delim {
	case '{':
		obj := make(map[string]any)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key must be a string")
			}
			childPath := path + "." + key
			value, err := decodeSchemaJSONValue(dec, childPath, ignored, depth+1)
			if err != nil {
				return nil, err
			}
			if previous, exists := obj[key]; exists {
				if err := appendIgnoredToolArg(ignored, childPath, previous, message.IgnoredToolArgReasonShadowed); err != nil {
					return nil, err
				}
			}
			obj[key] = value
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		var items []any
		for index := 0; dec.More(); index++ {
			item, err := decodeSchemaJSONValue(dec, fmt.Sprintf("%s[%d]", path, index), ignored, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func appendIgnoredToolArg(ignored *[]message.IgnoredToolArg, path string, value any, reason message.IgnoredToolArgReason) error {
	// A nil sink means the caller only wants the pass/fail verdict, so skip the
	// encode: ValidateToolArgs runs once per streaming delta and would throw the
	// record away. Encoding cannot fail for a value that came out of the JSON
	// decoder, so no verdict differs between a nil and a non-nil sink.
	if ignored == nil {
		return nil
	}
	encoded, err := Encode(value)
	if err != nil {
		return err
	}
	*ignored = append(*ignored, message.IgnoredToolArg{
		Path:      path,
		ValueJSON: string(encoded),
		Reason:    reason,
	})
	return nil
}

// dropIgnoredArgsUnder removes shadowed/ignored records nested under parent
// (directly or through arrays). Once an unrecognized field is dropped whole,
// its children never execute, so earlier duplicate occurrences inside it are
// noise: surfacing both the parent and its children would double-report.
func dropIgnoredArgsUnder(ignored *[]message.IgnoredToolArg, parent string) {
	if ignored == nil {
		return
	}
	out := (*ignored)[:0]
	for _, item := range *ignored {
		if item.Path == parent || strings.HasPrefix(item.Path, parent+".") || strings.HasPrefix(item.Path, parent+"[") {
			continue
		}
		out = append(out, item)
	}
	*ignored = out
}

// Encode re-encodes stripped arguments with HTML escaping off.
// The result is replayed to the model as the call's effective arguments, so
// escaping `<`, `>` and `&` would both inflate every JSX, HTML or shell payload
// sixfold per character and hide from the reader what actually ran.
func Encode(value any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, fmt.Errorf("re-encode arguments: %w", err)
	}
	// Encode terminates the value with a newline; tool arguments are compared
	// and displayed as bare JSON.
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
