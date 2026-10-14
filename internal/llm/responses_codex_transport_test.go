package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestResponsesProvider_CodexOAuth429EmitsRateLimitDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-codex-primary-used-percent", "91")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("x-codex-primary-reset-after-seconds", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Preset: config.ProviderPresetCodex,
		APIURL: server.URL + "/v1/responses",
		Models: map[string]config.ModelConfig{
			"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	dumpDir := t.TempDir()

	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.SetDumpWriter(NewDumpWriter(dumpDir))
	var deltas []message.StreamDelta
	_, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(delta message.StreamDelta) {
			deltas = append(deltas, delta)
		},
	)
	if err == nil {
		t.Fatal("expected 429 error")
	}

	var got *message.StreamDelta
	for i := range deltas {
		if deltas[i].Type == "rate_limits" && deltas[i].RateLimit != nil {
			got = &deltas[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("did not receive rate_limits delta: %#v", deltas)
	}
	if got.RateLimit.Provider != "openai" {
		t.Fatalf("delta provider = %q, want openai", got.RateLimit.Provider)
	}
	if snap := providerCfg.KeySnapshot("oauth-key"); snap == nil || snap.Primary == nil || snap.Primary.UsedPercent() != 91 {
		t.Fatalf("stored snapshot = %#v, want primary used percent 91", snap)
	}

	entries := waitForDumpEntries(t, dumpDir, 1)
	data, err := os.ReadFile(filepath.Join(dumpDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var dump struct {
		HTTPStatus  int         `json:"http_status"`
		HTTPHeaders http.Header `json:"http_headers"`
		HTTPBody    string      `json:"http_body"`
		Error       string      `json:"error"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if dump.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("http_status = %d, want %d", dump.HTTPStatus, http.StatusTooManyRequests)
	}
	if dump.HTTPBody != `{"error":{"message":"rate limited"}}` {
		t.Fatalf("http_body = %q, want raw body", dump.HTTPBody)
	}
	if got := dump.HTTPHeaders.Get("x-codex-primary-used-percent"); got != "91" {
		t.Fatalf("dump header x-codex-primary-used-percent = %q, want 91", got)
	}
	if dump.Error == "" {
		t.Fatal("expected non-empty error in dump")
	}
}

func TestResponsesProvider_CodexWSUsageLimitSkipsHTTPFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		return nil, false, &APIError{StatusCode: http.StatusInternalServerError, Code: "usage_limit_reached", Message: "The usage limit has been reached"}
	}

	_, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("CompleteStream error = %v, want usage limit error", err)
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 after terminal Codex WebSocket error", httpCalls)
	}
}

func TestResponsesProvider_RequestOverridesDisableCodexWebSocket(t *testing.T) {
	var httpCalls int
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Compat: &config.ProviderCompatConfig{RequestOverrides: &config.RequestOverridesConfig{
			Body: map[string]any{"custom": true},
		}},
		Models: map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls int
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls++
		return nil, false, nil
	}

	_, err := r.CompleteStream(context.Background(), "oauth-key", "gpt-5", "system", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if wsCalls != 0 {
		t.Fatalf("ws calls = %d, want 0 when request overrides are configured", wsCalls)
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d, want 1", httpCalls)
	}
	if gotBody["custom"] != true {
		t.Fatalf("custom = %#v, want true", gotBody["custom"])
	}
}

func TestResponsesProvider_CodexWSIncrementalRetryUsageLimitSkipsHTTPFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls int
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls++
		if wsCalls == 1 {
			return nil, true, fmt.Errorf("previous_response_not_found")
		}
		return nil, false, &APIError{StatusCode: http.StatusInternalServerError, Message: "The usage limit has been reached"}
	}

	_, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("CompleteStream error = %v, want usage limit error", err)
	}
	if wsCalls != 2 {
		t.Fatalf("ws calls = %d, want 2", wsCalls)
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 after terminal Codex WebSocket retry error", httpCalls)
	}
}

func TestResponsesProvider_CodexWSUsageLimitCodeStillSkipsHTTPFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		return nil, false, &APIError{StatusCode: http.StatusInternalServerError, Code: "usage_limit_reached", Message: "The usage limit has been reached"}
	}

	_, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("CompleteStream error = %v, want usage limit error", err)
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 after terminal Codex WebSocket error", httpCalls)
	}
}

func TestMarkKeyCooldownCodexWSUsageLimitWithoutHTTPStatusUsesResetWindow(t *testing.T) {
	ctx := context.Background()
	resetPrimary := time.Now().Add(2 * time.Hour)
	resetSecondary := time.Now().Add(24 * time.Hour)
	p := NewProviderConfig("codex", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	p.UpdateKeySnapshot("oauth-key", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetPrimary},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetSecondary},
	})
	res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: http.StatusInternalServerError, Code: "usage_limit_reached", Message: "The usage limit has been reached"})
	if !res.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for Codex WebSocket usage limit")
	}
	p.mu.Lock()
	exhaustedUntil := p.keyStates[0].ExhaustedUntil
	cooldownEnd := p.keyStates[0].CooldownEnd
	p.mu.Unlock()
	if exhaustedUntil.Before(resetSecondary.Add(-2*time.Second)) || exhaustedUntil.After(resetSecondary.Add(2*time.Second)) {
		t.Fatalf("ExhaustedUntil = %v, want ~%v", exhaustedUntil, resetSecondary)
	}
	if !cooldownEnd.IsZero() {
		t.Fatalf("CooldownEnd = %v, want zero when quota exhaustion uses hard reset window", cooldownEnd)
	}
}

func TestMarkKeyCooldownCodexWSMessageOnlyUsageLimitUsesResetWindow(t *testing.T) {
	ctx := context.Background()
	resetPrimary := time.Now().Add(2 * time.Hour)
	resetSecondary := time.Now().Add(24 * time.Hour)
	p := NewProviderConfig("codex", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	p.UpdateKeySnapshot("oauth-key", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetPrimary},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetSecondary},
	})
	res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: http.StatusInternalServerError, Message: "You've hit your usage limit"})
	if !res.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for message-only Codex usage limit")
	}
	p.mu.Lock()
	exhaustedUntil := p.keyStates[0].ExhaustedUntil
	cooldownEnd := p.keyStates[0].CooldownEnd
	p.mu.Unlock()
	if exhaustedUntil.Before(resetSecondary.Add(-2*time.Second)) || exhaustedUntil.After(resetSecondary.Add(2*time.Second)) {
		t.Fatalf("ExhaustedUntil = %v, want ~%v", exhaustedUntil, resetSecondary)
	}
	if !cooldownEnd.IsZero() {
		t.Fatalf("CooldownEnd = %v, want zero when quota exhaustion uses hard reset window", cooldownEnd)
	}
}

func TestMarkKeyCooldownCodexWSMessageOnlyQuotaUsesResetWindow(t *testing.T) {
	ctx := context.Background()
	resetPrimary := time.Now().Add(90 * time.Minute)
	resetSecondary := time.Now().Add(3 * time.Hour)
	p := NewProviderConfig("codex", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	p.UpdateKeySnapshot("oauth-key", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetPrimary},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetSecondary},
	})
	res := markKeyCooldown(ctx, p, "oauth-key", "test-model", &APIError{StatusCode: http.StatusInternalServerError, Message: "You have exceeded your current quota"})
	if !res.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for message-only Codex quota exhaustion")
	}
	p.mu.Lock()
	exhaustedUntil := p.keyStates[0].ExhaustedUntil
	cooldownEnd := p.keyStates[0].CooldownEnd
	p.mu.Unlock()
	if exhaustedUntil.Before(resetSecondary.Add(-2*time.Second)) || exhaustedUntil.After(resetSecondary.Add(2*time.Second)) {
		t.Fatalf("ExhaustedUntil = %v, want ~%v", exhaustedUntil, resetSecondary)
	}
	if !cooldownEnd.IsZero() {
		t.Fatalf("CooldownEnd = %v, want zero when quota exhaustion uses hard reset window", cooldownEnd)
	}
}

func TestResponsesProvider_CodexWSIncrementalFailureRetriesWSFullThenHTTPFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-http","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:               config.ProviderTypeChatCompletions,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls []bool
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		call := len(wsCalls)
		wsCalls = append(wsCalls, call == 0)
		if call == 0 {
			return nil, true, fmt.Errorf("previous_response_not_found")
		}
		return nil, false, fmt.Errorf("ws still unavailable")
	}

	resp, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.ProviderResponseID != "resp-http" {
		t.Fatalf("unexpected response after fallback: %#v", resp)
	}
	if len(wsCalls) != 2 {
		t.Fatalf("ws calls = %d, want 2 (incremental then full)", len(wsCalls))
	}
	if !wsCalls[0] || wsCalls[1] {
		t.Fatalf("ws retry sequence = %#v, want [incremental, full]", wsCalls)
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d, want 1 after ws retries", httpCalls)
	}
}

func TestResponsesProvider_CodexWSChainStateMismatchRetriesWSFullThenStops(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls int
	var retryOpts codexWSCompleteOptions
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, opts codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls++
		if wsCalls == 2 {
			retryOpts = opts
		}
		return nil, false, &APIError{StatusCode: http.StatusBadRequest, Message: "No tool call found for function call output with call_id call_abc."}
	}

	_, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil || !strings.Contains(err.Error(), "No tool call found") {
		t.Fatalf("CompleteStream error = %v, want chain-state mismatch error", err)
	}
	if wsCalls != 2 {
		t.Fatalf("ws calls = %d, want 2 (initial + chain-reset retry)", wsCalls)
	}
	if !retryOpts.SkipPrewarm {
		t.Fatal("chain-state mismatch retry must skip prewarm so it sends the full input without previous_response_id")
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 (chain-mismatch does not fall back to HTTP)", httpCalls)
	}
}

func TestResponsesProvider_CodexWSChainStateMismatchRecoversAfterReset(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("codex", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls int
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls++
		if wsCalls == 1 {
			return nil, false, &APIError{StatusCode: http.StatusBadRequest, Message: "No tool call found for function call output with call_id call_abc."}
		}
		return &message.Response{ProviderResponseID: "resp-ws-recovered"}, false, nil
	}

	resp, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.ProviderResponseID != "resp-ws-recovered" {
		t.Fatalf("unexpected response after chain-mismatch retry: %#v", resp)
	}
	if wsCalls != 2 {
		t.Fatalf("ws calls = %d, want 2", wsCalls)
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 when ws retry succeeds", httpCalls)
	}
}

func TestResponsesProvider_CodexWSProtocolCloseDoesNotDisableFutureWebSocketAttempts(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-http","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:               config.ProviderTypeChatCompletions,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls int
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls++
		return nil, false, &websocket.CloseError{Code: websocket.ClosePolicyViolation, Text: "parse_failed"}
	}

	for i := range 2 {
		resp, err := r.CompleteStream(
			context.Background(), "oauth-key", "gpt-5", "system",
			[]message.Message{{Role: "user", Content: "hello"}},
			nil, 128, RequestTuning{}, func(message.StreamDelta) {},
		)
		if err != nil {
			t.Fatalf("CompleteStream call %d: %v", i+1, err)
		}
		if resp == nil || resp.ProviderResponseID != "resp-http" {
			t.Fatalf("call %d unexpected response after fallback: %#v", i+1, resp)
		}
	}
	if wsCalls != 2 {
		t.Fatalf("ws calls = %d, want 2; protocol close must not disable future websocket attempts", wsCalls)
	}
	if httpCalls != 2 {
		t.Fatalf("http calls = %d, want 2 fallback calls", httpCalls)
	}
}

func TestResponsesProvider_CodexWSCancelDoesNotRetryOrFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:               config.ProviderTypeChatCompletions,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wsCalls int
	var deltas []message.StreamDelta
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time, _ codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls++
		cancel()
		return nil, true, fmt.Errorf("previous_response_not_found")
	}

	_, err := r.CompleteStream(
		ctx, "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(delta message.StreamDelta) {
			deltas = append(deltas, delta)
		},
	)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if wsCalls != 1 {
		t.Fatalf("ws calls = %d, want 1 after cancel", wsCalls)
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 after cancel", httpCalls)
	}
	if len(deltas) != 0 {
		t.Fatalf("unexpected status deltas after cancel: %#v", deltas)
	}
}
