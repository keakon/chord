package llm

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

const (
	openAICodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	openAICodexOriginator   = "codex_cli_rs"
	openAICodexBetaHeader   = "responses=experimental"
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

// applyResponsesIdentityHeaders sets the shared Responses identity and beta
// headers while keeping User-Agent configurable per provider.
func applyResponsesIdentityHeaders(h http.Header, provider *ProviderConfig) {
	h.Set(headerOpenAIBeta, openAICodexBetaHeader)
	setProviderLLMUserAgent(h, provider)
	h.Set("originator", openAICodexOriginator)
}

// applyResponsesStreamingHeaders sets headers used by streaming Responses
// requests. The compact endpoint is unary JSON and does not use the SSE Accept.
func applyResponsesStreamingHeaders(h http.Header, provider *ProviderConfig) {
	applyResponsesIdentityHeaders(h, provider)
	h.Set("Accept", "text/event-stream")
}

func applyAzureResponsesStreamingHeaders(h http.Header, provider *ProviderConfig) {
	setProviderLLMUserAgent(h, provider)
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
// an OAuth session key. Callers choose whether the request is streaming so the
// unary compact endpoint does not inherit the SSE Accept header.
func applyOpenAIOAuthHeaders(req *http.Request, provider *ProviderConfig, apiKey string, streaming bool) {
	applyProviderAuthHeader(req.Header, config.AuthSchemeBearer, apiKey)
	if streaming {
		applyResponsesStreamingHeaders(req.Header, provider)
	} else {
		applyResponsesIdentityHeaders(req.Header, provider)
	}
	req.Header.Set(headerSessionID, newOpenAIOAuthSessionID())

	if provider == nil {
		return
	}
	if info := provider.oauthInfoForKey(apiKey); info != nil && info.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", info.AccountID)
	}
}

func newOpenAIOAuthSessionID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}

	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		buf[0:4],
		buf[4:6],
		buf[6:8],
		buf[8:10],
		buf[10:16],
	)
}
