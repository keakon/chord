package llm

import (
	"encoding/json"
	"net/http"

	"golang.org/x/net/http/httpguts"
)

// responseErrorHeaders validates JSON error headers before reusing HTTP retry
// and rate-limit parsers. Malformed headers must not discard the provider error.
func responseErrorHeaders(raw json.RawMessage) http.Header {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return nil
	}
	headers := make(http.Header, len(fields))
	for name, value := range fields {
		if !httpguts.ValidHeaderFieldName(name) {
			continue
		}
		var values []string
		switch value[0] {
		case '"':
			var text string
			if json.Unmarshal(value, &text) != nil {
				continue
			}
			values = []string{text}
		case '[':
			if json.Unmarshal(value, &values) != nil {
				continue
			}
		default:
			if value[0] != '-' && (value[0] < '0' || value[0] > '9') {
				continue
			}
			var number json.Number
			if json.Unmarshal(value, &number) != nil {
				continue
			}
			values = []string{number.String()}
		}
		for _, text := range values {
			if httpguts.ValidHeaderFieldValue(text) {
				headers.Add(name, text)
			}
		}
	}
	return headers
}
