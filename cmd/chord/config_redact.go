package main

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/keakon/chord/internal/config"
)

func sensitiveConfigKey(key string) bool {
	k := strings.ToLower(key)
	if strings.HasSuffix(k, "_url") || strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_path") {
		return false
	}
	// Separator styles vary between snake_case config keys and URL query
	// parameters; fold them so "api-key" and "api_key" match alike.
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(k)
	switch {
	case strings.Contains(normalized, "secret"),
		strings.Contains(normalized, "password"),
		strings.Contains(normalized, "authorization"),
		strings.Contains(normalized, "api_key"),
		strings.Contains(normalized, "apikey"),
		strings.Contains(normalized, "bearer"):
		return true
	case normalized == "token", normalized == "refresh", normalized == "access", normalized == "key":
		return true
	case strings.HasSuffix(normalized, "_token"):
		return true
	}
	return false
}

// redactConfigURL masks URL credentials while keeping the endpoint readable.
func redactConfigURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[redacted URL]"
	}
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	changed := false
	if queryErr != nil {
		parsed.RawQuery = "[redacted]"
		changed = true
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword("[redacted]", "[redacted]")
		} else {
			parsed.User = url.User("[redacted]")
		}
		changed = true
	}
	for key, values := range query {
		if !sensitiveConfigKey(key) {
			continue
		}
		for i := range values {
			values[i] = "[redacted]"
		}
		changed = true
	}
	if !changed {
		return raw
	}
	if queryErr == nil {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

var configDiagnosticURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)

func redactConfigDiagnostic(d config.Diagnostic) config.Diagnostic {
	redact := func(value string) string { return configDiagnosticURL.ReplaceAllStringFunc(value, redactConfigURL) }
	d.Message = redact(d.Message)
	d.Fallback = redact(d.Fallback)
	d.Scope = redact(d.Scope)
	return d
}
