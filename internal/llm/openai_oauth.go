package llm

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	openAICodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	openAICodexOriginator   = "codex_cli_rs"
	openAICodexBetaHeader   = "responses=experimental"
)

func normalizeResponsesReasoningEffort(effort string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "":
		return "", false
	case "low", "medium", "high", "xhigh":
		return normalized, normalized != effort
	default:
		return "", true
	}
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

// applyOpenAIOAuthHeaders sets the full set of headers for Codex requests using
// an OAuth session key. Callers choose whether the request is streaming so the
// unary compact endpoint does not inherit the SSE Accept header.
func applyOpenAIOAuthHeaders(req *http.Request, provider *ProviderConfig, apiKey string, streaming bool) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
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
