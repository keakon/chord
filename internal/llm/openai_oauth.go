package llm

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	openAICodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	openAICodexOriginator   = "codex_cli_rs"
	openAICodexBetaHeader   = "responses=experimental"
)

func normalizeOpenAIOAuthReasoningEffort(effort string) (string, bool) {
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

func applyOpenAIOAuthHeaders(req *http.Request, provider *ProviderConfig, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", openAICodexBetaHeader)
	req.Header.Set("User-Agent", openAICodexUserAgent())
	req.Header.Set("originator", openAICodexOriginator)
	req.Header.Set("session_id", newOpenAIOAuthSessionID())

	if provider == nil {
		return
	}
	if info := provider.oauthInfoForKey(apiKey); info != nil && info.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", info.AccountID)
	}
}

func openAICodexUserAgent() string {
	return fmt.Sprintf("%s/0.0.1 (%s %s)", openAICodexOriginator, runtime.GOOS, runtime.GOARCH)
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
