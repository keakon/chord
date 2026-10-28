package llm

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

const (
	openAICodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	// openAICodexOriginator is Chord's baseline originator: the default client
	// name the Codex CLI reports when nothing overrides it. A provider that
	// emulates the Codex client reports the TUI name instead (see
	// codexOriginator), the same value its User-Agent carries.
	openAICodexOriginator = "codex_cli_rs"
	openAICodexBetaHeader = "responses=experimental"
)

// normalizeReasoningEffort normalizes casing/whitespace and reports whether the
// normalized form differs from the input. It never silently drops a value: an
// unknown effort is returned as-is so it reaches the upstream gateway verbatim
// (a 400 from the provider is fail-loud and far more debuggable than a silent omit).
// We intentionally keep this transport-agnostic: provider/model-specific support
// must be decided by configuration and upstream validation, not by a stale local
// whitelist in Chord.
func normalizeReasoningEffort(effort string) (normalized string, changed bool) {
	normalized = strings.ToLower(strings.TrimSpace(effort))
	return normalized, normalized != effort
}

// resolveResponsesReasoningEffort normalizes the effort and passes it through.
// Model/provider support is intentionally fail-loud upstream instead of being
// filtered by a local hard-coded whitelist.
func resolveResponsesReasoningEffort(effort string) string {
	normalized, changed := normalizeReasoningEffort(effort)
	if normalized == "" {
		return ""
	}
	if changed {
		log.Warnf("normalizing reasoning effort for Responses request requested=%v effective=%v", effort, normalized)
	}
	return normalized
}

func resolveOpenAIOAuthAPIURL(apiURL string) string {
	if apiURL == "" {
		return openAICodexResponsesURL
	}

	parsed, err := url.Parse(apiURL)
	if err != nil {
		return apiURL
	}
	if strings.EqualFold(parsed.Hostname(), "api.openai.com") {
		return openAICodexResponsesURL
	}
	return apiURL
}

// codexOriginator returns the originator header value for a Codex/Responses
// request: the TUI client name when the request presents as the Codex client —
// the same value its User-Agent reports — otherwise Chord's baseline.
func codexOriginator(provider *ProviderConfig, model string) string {
	if provider != nil && provider.codexShapeEmulated(model) {
		return codexUserAgentProduct
	}
	return openAICodexOriginator
}

// applyResponsesIdentityHeaders sets the shared Responses identity and beta
// headers while keeping User-Agent configurable per provider.
func applyResponsesIdentityHeaders(h http.Header, provider *ProviderConfig, model string) {
	h.Set(headerOpenAIBeta, openAICodexBetaHeader)
	setProviderLLMUserAgent(h, provider, model)
	h.Set(headerOriginator, codexOriginator(provider, model))
}

// applyResponsesStreamingHeaders sets headers used by streaming Responses
// requests, including the SSE Accept.
func applyResponsesStreamingHeaders(h http.Header, provider *ProviderConfig, model string) {
	applyResponsesIdentityHeaders(h, provider, model)
	h.Set("Accept", "text/event-stream")
}

func applyProviderAuthHeader(h http.Header, scheme, apiKey string) {
	switch scheme {
	case config.AuthSchemeAnthropicAPIKey:
		h.Set("x-api-key", apiKey)
	case config.AuthSchemeAPIKey:
		h.Set("api-key", apiKey)
	default:
		h.Set("Authorization", "Bearer "+apiKey)
	}
}

// applyOpenAIOAuthHeaders sets the full set of headers for Codex requests using
// an OAuth session key.
//
// sessionID must be the caller's stable session identity: the Codex backend
// derives cache affinity from it.
func applyOpenAIOAuthHeaders(req *http.Request, provider *ProviderConfig, model, apiKey, sessionID string) {
	applyProviderAuthHeader(req.Header, config.AuthSchemeBearer, apiKey)
	applyResponsesStreamingHeaders(req.Header, provider, model)
	applyCodexSessionIdentity(req.Header, sessionID)

	if provider == nil {
		return
	}
	if info := provider.oauthInfoForKey(apiKey); info != nil && info.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", info.AccountID)
	}
}
