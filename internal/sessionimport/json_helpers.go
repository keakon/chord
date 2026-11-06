package sessionimport

import (
	"encoding/json"
	"errors"
)

var ErrUnsupportedSchema = errors.New("unsupported import schema")

func importJSONUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func importJSONUnmarshalString(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}
