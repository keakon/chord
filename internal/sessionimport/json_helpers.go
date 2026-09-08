package sessionimport

import (
	"errors"

	sonicjson "github.com/bytedance/sonic"
)

var ErrUnsupportedSchema = errors.New("unsupported import schema")

// Session import favors sonic's default fast decoder. Imported files are
// best-effort compatibility inputs rather than strict protocol boundaries; if a
// future import path needs stdlib-identical validation, give it its own config.

func importJSONUnmarshal(data []byte, v any) error {
	return sonicjson.ConfigDefault.Unmarshal(data, v)
}

func importJSONUnmarshalString(data string, v any) error {
	return sonicjson.ConfigDefault.UnmarshalFromString(data, v)
}
