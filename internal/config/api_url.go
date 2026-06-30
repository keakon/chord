package config

import (
	"net/url"
	"strings"
)

// APIURLPathHasSuffix reports whether apiURL's path ends with suffix.
// Query strings and fragments are ignored so endpoint URLs such as
// /responses?api-version=v1 still match /responses.
func APIURLPathHasSuffix(apiURL, suffix string) bool {
	path := apiURLPathForSuffixMatch(apiURL)
	if path == "" {
		return false
	}
	return strings.HasSuffix(path, suffix)
}

func apiURLPathForSuffixMatch(apiURL string) string {
	trimmed := strings.TrimSpace(apiURL)
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" {
		return strings.TrimSuffix(parsed.Path, "/")
	}
	path, _, _ := strings.Cut(trimmed, "#")
	path, _, _ = strings.Cut(path, "?")
	return strings.TrimSuffix(strings.TrimSpace(path), "/")
}
