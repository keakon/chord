package sessionimport

import (
	"encoding/json"
	"errors"
	"fmt"

	sonicjson "github.com/bytedance/sonic"
)

var ErrUnsupportedSchema = errors.New("unsupported import schema")

// Session import favors sonic's default fast decoder. Imported files are
// best-effort compatibility inputs rather than strict protocol boundaries; if a
// future import path needs stdlib-identical validation, give it its own config.

// readJSONAsMap decodes raw JSON into a map and returns a helpful error when
// the payload is not an object.
func readJSONAsMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := sonicjson.ConfigDefault.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse JSON object: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("parse JSON object: %w", ErrUnsupportedSchema)
	}
	return m, nil
}

func importJSONUnmarshal(data []byte, v any) error {
	return sonicjson.ConfigDefault.Unmarshal(data, v)
}

func importJSONUnmarshalString(data string, v any) error {
	return sonicjson.ConfigDefault.UnmarshalFromString(data, v)
}
