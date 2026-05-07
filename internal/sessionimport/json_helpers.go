package sessionimport

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrUnsupportedSchema = errors.New("unsupported import schema")

// readJSONAsMap decodes raw JSON into a map and returns a helpful error when
// the payload is not an object.
func readJSONAsMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse JSON object: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("parse JSON object: %w", ErrUnsupportedSchema)
	}
	return m, nil
}
