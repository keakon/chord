package sessionimport

import (
	"encoding/json"
	"fmt"

	"github.com/keakon/chord/internal/convformat"
)

func renderImportedToolMarker(kind string, payload json.RawMessage) string {
	if len(payload) == 0 {
		return fmt.Sprintf("[Imported %s]", kind)
	}
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return convformat.BlockString("[Imported "+kind+"]", string(payload))
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return convformat.BlockString("[Imported "+kind+"]", string(b))
}
