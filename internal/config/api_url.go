package config

import (
	"net/url"
	"strings"
)

const GeminiAPIURLRequirement = "api_url version root ending in /v1, /v1beta or /v1alpha; remove a trailing /models from Gemini URLs"

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
	case IsGeminiAPIURL(apiURL) && (APIURLPathHasSuffix(apiURL, "/v1beta") || APIURLPathHasSuffix(apiURL, "/v1alpha") || isOfficialGeminiURL(apiURL)):
		return ProviderTypeGenerateContent
	default:
		return ""
	}
}

// IsGeminiAPIURL reports whether apiURL names a native Gemini version root.
// A proxy can include a path prefix; model resources are appended by the client.
func IsGeminiAPIURL(apiURL string) bool {
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	return APIURLPathHasSuffix(apiURL, "/v1") || APIURLPathHasSuffix(apiURL, "/v1beta") || APIURLPathHasSuffix(apiURL, "/v1alpha")
}

func isOfficialGeminiURL(apiURL string) bool {
	u, err := url.Parse(strings.TrimSpace(apiURL))
	return err == nil && strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com")
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
