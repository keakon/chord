package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// canonicalToolCallArgs preserves number literals while normalizing object
// order and whitespace, including arguments beyond float64 integer precision.
func canonicalToolCallArgs(input json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode tool arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("tool arguments contain multiple JSON values")
		}
		return nil, fmt.Errorf("decode trailing tool arguments: %w", err)
	}
	return json.Marshal(value)
}
