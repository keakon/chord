package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/keakon/chord/internal/ratelimit"
)

func resolveCodexUsageURL(apiURL string) (string, error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		apiURL = openAICodexResponsesURL
	}
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return "", err
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if idx := strings.Index(path, "/backend-api"); idx >= 0 {
		parsed.Path = path[:idx] + "/backend-api/wham/usage"
		return parsed.String(), nil
	}
	parsed.Path = "/backend-api/wham/usage"
	return parsed.String(), nil
}

func FetchCodexUsageSnapshot(ctx context.Context, provider *ProviderConfig, key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
	if provider == nil || strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("missing provider or OAuth key for codex usage poll")
	}
	usageURL, err := resolveCodexUsageURL(provider.APIURL())
	if err != nil {
		return nil, fmt.Errorf("resolve codex usage URL: %w", err)
	}
	client, err := NewHTTPClientWithProxy(provider.EffectiveProxyURL(), 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("create codex usage HTTP client: %w", err)
	}
	// NOTE: match codex-rs behavior more closely by relying on request-level timeout.
	// The Go transport has TLSHandshakeTimeout=10s; we keep client timeout at 30s but
	// allow callers to extend ctx deadline when they explicitly want slower probes.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build codex usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", openAICodexUserAgent())
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send codex usage request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read codex usage response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var compact any
		if json.Unmarshal(body, &compact) == nil {
			if b, mErr := json.Marshal(compact); mErr == nil {
				body = b
			}
		}
		return nil, fmt.Errorf("codex usage endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	snaps, err := ratelimit.ParseCodexUsagePayload(body)
	if err != nil {
		return nil, fmt.Errorf("parse codex usage payload: %w", err)
	}
	for _, snap := range snaps {
		if snap != nil {
			snap.Provider = provider.Name()
		}
	}
	return snaps, nil
}
