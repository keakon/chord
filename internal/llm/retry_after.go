package llm

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

// parseRetryAfter accepts HTTP delay-seconds or an HTTP date. A valid zero or
// past date is distinct from invalid advice when choosing a fallback hint.
func parseRetryAfter(value string) (time.Duration, bool) {
	if !httpguts.ValidHeaderFieldValue(value) {
		return 0, false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	digits := true
	for _, c := range value {
		if c < '0' || c > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, false
		}
		return durationFromPositiveSecondsClamped(seconds, 0), true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}

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
