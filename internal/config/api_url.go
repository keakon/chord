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

// InferProviderTypeFromAPIURL returns the provider type an api_url path
// implies when provider.type is omitted, or "" when the path names no known
// endpoint.
func InferProviderTypeFromAPIURL(apiURL string) string {
	switch {
	case APIURLPathHasSuffix(apiURL, "/responses"):
		return ProviderTypeResponses
	case APIURLPathHasSuffix(apiURL, "/chat/completions"):
		return ProviderTypeChatCompletions
	case APIURLPathHasSuffix(apiURL, "/messages"):
		return ProviderTypeMessages
	case APIURLPathHasSuffix(apiURL, "/models"):
		return ProviderTypeGenerateContent
	default:
		return ""
	}
}

// EffectiveProviderType returns the provider type the runtime resolves for a
// provider config: the configured type, the Responses type a codex preset
// implies, or the type inferred from api_url. It returns "" when none of them
// resolves; the runtime then refuses the provider.
func EffectiveProviderType(cfg ProviderConfig) string {
	if cfg.Type != "" {
		return cfg.Type
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Preset), ProviderPresetCodex) {
		return ProviderTypeResponses
	}
	return InferProviderTypeFromAPIURL(cfg.APIURL)
}
