package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

// testNetTimeoutErr implements net.Error for timeout classification tests.
type testNetTimeoutErr struct {
	timeout   bool
	temporary bool
}

func (e testNetTimeoutErr) Error() string   { return "net timeout" }
func (e testNetTimeoutErr) Timeout() bool   { return e.timeout }
func (e testNetTimeoutErr) Temporary() bool { return e.temporary }

// tlsHandshakeTimeoutErr mimics crypto/tls-style handshake deadline errors.
type tlsHandshakeTimeoutErr struct{}

func (tlsHandshakeTimeoutErr) Error() string   { return "tls: handshake timeout" }
func (tlsHandshakeTimeoutErr) Timeout() bool   { return true }
func (tlsHandshakeTimeoutErr) Temporary() bool { return false }

type scriptedCall struct {
	resp         *message.Response
	err          error
	streams      []message.StreamDelta
	expectTuning *RequestTuning
}

type scriptedProvider struct {
	mu    sync.Mutex
	calls []scriptedCall
	count int
}

func (p *scriptedProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	if ctx.Err() != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.count++
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	if len(p.calls) == 0 {
		return nil, errors.New("unexpected provider call")
	}
	next := p.calls[0]
	p.calls = p.calls[1:]
	if next.expectTuning != nil {
		if tuning != *next.expectTuning {
			return nil, errors.New("unexpected request tuning")
		}
	}
	if cb != nil {
		for _, delta := range next.streams {
			cb(delta)
		}
	}
	return next.resp, next.err
}

func (p *scriptedProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

type recordingProvider struct {
	scriptedProvider
	apiKeys   []string
	maxTokens []int
}

type constantErrProvider struct {
	err   error
	calls int
}

func (p *constantErrProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.calls++
	return nil, p.err
}

type recordingTuningProvider struct {
	calls  int
	tuning []RequestTuning
}

func (p *recordingTuningProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning RequestTuning,
	_ StreamCallback,
) (*message.Response, error) {
	p.calls++
	p.tuning = append(p.tuning, tuning)
	return &message.Response{Content: "ok"}, nil
}

func TestVisibleStreamTrackerMarksToolUseAsStreaming(t *testing.T) {
	var got []message.StreamDelta
	tracker := &visibleStreamTracker{
		inner: func(delta message.StreamDelta) {
			got = append(got, delta)
		},
		onVisibleStart: func() {
			got = append(got, message.StreamDelta{
				Type:   "status",
				Status: &message.StatusDelta{Type: "streaming"},
			})
			got = append(got, message.StreamDelta{Type: "key_confirmed"})
		},
	}

	tracker.Callback(message.StreamDelta{
		Type:   "status",
		Status: &message.StatusDelta{Type: "waiting_token"},
	})
	tracker.Callback(message.StreamDelta{
		Type: "tool_use_start",
		ToolCall: &message.ToolCallDelta{
			ID:    "call-1",
			Name:  "Write",
			Input: `{"path":"demo.txt","content":"hello"}`,
		},
	})

	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}
	if got[0].Type != "status" || got[0].Status == nil || got[0].Status.Type != "waiting_token" {
		t.Fatalf("got[0] = %#v, want waiting_token status", got[0])
	}
	if got[1].Type != "status" || got[1].Status == nil || got[1].Status.Type != "streaming" {
		t.Fatalf("got[1] = %#v, want streaming status", got[1])
	}
	if got[2].Type != "key_confirmed" {
		t.Fatalf("got[2].Type = %q, want key_confirmed", got[2].Type)
	}
	if got[3].Type != "tool_use_start" || got[3].ToolCall == nil || got[3].ToolCall.ID != "call-1" {
		t.Fatalf("got[3] = %#v, want tool_use_start for call-1", got[3])
	}
}

func TestVisibleStreamTrackerRearmsAfterRollback(t *testing.T) {
	var got []message.StreamDelta
	tracker := &visibleStreamTracker{
		inner: func(delta message.StreamDelta) {
			got = append(got, delta)
		},
		onVisibleStart: func() {
			got = append(got, message.StreamDelta{
				Type:   "status",
				Status: &message.StatusDelta{Type: "streaming"},
			})
			got = append(got, message.StreamDelta{Type: "key_confirmed"})
		},
	}

	tracker.Callback(message.StreamDelta{
		Type: "tool_use_start",
		ToolCall: &message.ToolCallDelta{
			ID:    "call-1",
			Name:  "Write",
			Input: `{"path":"demo.txt"}`,
		},
	})
	tracker.Callback(message.StreamDelta{
		Type:     "rollback",
		Rollback: &message.RollbackDelta{Reason: "retry same key"},
	})
	tracker.Callback(message.StreamDelta{
		Type: "text",
		Text: "retry succeeded",
	})

	if len(got) != 7 {
		t.Fatalf("len(got) = %d, want 7", len(got))
	}
	if got[0].Type != "status" || got[0].Status == nil || got[0].Status.Type != "streaming" {
		t.Fatalf("got[0] = %#v, want first streaming status", got[0])
	}
	if got[1].Type != "key_confirmed" {
		t.Fatalf("got[1].Type = %q, want key_confirmed", got[1].Type)
	}
	if got[2].Type != "tool_use_start" {
		t.Fatalf("got[2].Type = %q, want tool_use_start", got[2].Type)
	}
	if got[3].Type != "rollback" {
		t.Fatalf("got[3].Type = %q, want rollback", got[3].Type)
	}
	if got[4].Type != "status" || got[4].Status == nil || got[4].Status.Type != "streaming" {
		t.Fatalf("got[4] = %#v, want second streaming status", got[4])
	}
	if got[5].Type != "key_confirmed" {
		t.Fatalf("got[5].Type = %q, want second key_confirmed", got[5].Type)
	}
	if got[6].Type != "text" || got[6].Text != "retry succeeded" {
		t.Fatalf("got[6] = %#v, want retry text", got[6])
	}
}

func (p *recordingProvider) CompleteStream(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.apiKeys = append(p.apiKeys, apiKey)
	p.maxTokens = append(p.maxTokens, maxTokens)
	p.mu.Unlock()
	return p.scriptedProvider.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, cb)
}

func testProviderConfig(name, model string) *ProviderConfig {
	return testProviderConfigWithKeys(name, model, []string{"test-key"})
}

func testProviderConfigWithKeys(name, model string, keys []string) *ProviderConfig {
	return NewProviderConfig(name, config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			model: {
				Limit: config.ModelLimit{
					Context: 128000,
					Output:  4096,
				},
			},
		},
	}, keys)
}

func TestClient_RetriableErrorTriesOtherKeysBeforeFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5", []string{"key-a", "key-b"})
	fallbackCfg := testProviderConfig("qt", "glm-5.1")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 503, Message: "upstream unavailable"}},
		{resp: &message.Response{Content: "ok from second key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "fallback should not run"}}}

	client := NewClient(primaryCfg, primaryImpl, "gpt-5.5", 512, "")
	client.SetModelPool([]FallbackModel{{
		ProviderConfig: primaryCfg,
		ProviderImpl:   primaryImpl,
		ModelID:        "gpt-5.5",
		MaxTokens:      512,
		ContextLimit:   128000,
	}, {
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "glm-5.1",
		MaxTokens:      512,
		ContextLimit:   128000,
	}}, 0)

	resp, err := client.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hello"}}, nil, func(message.StreamDelta) {})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from second key" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	if got := len(primaryImpl.apiKeys); got != 2 {
		t.Fatalf("initial pool entry call count = %d, want 2", got)
	}
	if primaryImpl.apiKeys[0] != "key-a" || primaryImpl.apiKeys[1] != "key-b" {
		t.Fatalf("initial pool-entry apiKeys = %#v, want [key-a key-b]", primaryImpl.apiKeys)
	}
	if got := len(fallbackImpl.apiKeys); got != 0 {
		t.Fatalf("later pool entry should not be called before second key succeeds, got %d calls", got)
	}
}

func TestMarkKeyCooldown429UsesRetryAfterOrDefault(t *testing.T) {
	ctx := context.Background()
	snapReset := time.Now().Add(3 * time.Minute)
	snap := &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 50, ResetsAt: snapReset},
	}
	err429 := &APIError{StatusCode: 429, Message: "rate limited"}

	t.Run("default_is_1s_when_no_retry_after", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		p.UpdateKeySnapshot("k1", snap)
		res := markKeyCooldown(ctx, p, "k1", err429)
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for 429")
		}
		p.mu.Lock()
		var end time.Time
		for _, ks := range p.keyStates {
			if ks.Key == "k1" {
				end = ks.CooldownEnd
			}
		}
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 500*time.Millisecond || remain > 1500*time.Millisecond {
			t.Fatalf("expected ~1s cooldown, got remaining %v", remain)
		}
	})

	t.Run("retry_after_is_capped_at_1m", func(t *testing.T) {
		p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, []string{"k1"})
		res := markKeyCooldown(ctx, p, "k1", &APIError{StatusCode: 429, Message: "rl", RetryAfter: 2 * time.Minute})
		if !res.cooldownApplied {
			t.Fatal("expected cooldownApplied=true for Retry-After 429")
		}
		p.mu.Lock()
		var end time.Time
		for _, ks := range p.keyStates {
			if ks.Key == "k1" {
				end = ks.CooldownEnd
			}
		}
		p.mu.Unlock()
		remain := time.Until(end)
		if remain < 55*time.Second || remain > 61*time.Second {
			t.Fatalf("expected ~1m cooldown cap, got remaining %v", remain)
		}
	})
}

func TestMarkKeyCooldown429CodexOAuthQuotaExhaustedUsesResetWindow(t *testing.T) {
	ctx := context.Background()
	resetPrimary := time.Now().Add(2 * time.Hour)
	resetSecondary := time.Now().Add(24 * time.Hour)
	p := NewProviderConfig("p", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	p.UpdateKeySnapshot("oauth-key", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetPrimary},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: resetSecondary},
	})
	res := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 429, Message: "quota exhausted"})
	if !res.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for quota exhaustion")
	}
	p.mu.Lock()
	ks := p.keyStates[0]
	exhaustedUntil := ks.ExhaustedUntil
	cooldownEnd := ks.CooldownEnd
	p.mu.Unlock()
	if exhaustedUntil.Before(resetSecondary.Add(-2*time.Second)) || exhaustedUntil.After(resetSecondary.Add(2*time.Second)) {
		t.Fatalf("ExhaustedUntil = %v, want ~%v", exhaustedUntil, resetSecondary)
	}
	if !cooldownEnd.IsZero() {
		t.Fatalf("CooldownEnd = %v, want zero when quota exhaustion uses hard reset window", cooldownEnd)
	}
}

func TestMarkKeyCooldown401OAuthRefreshReturnsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Access:  "old-access-token",
			Refresh: "old-refresh-token",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		},
	}}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(creds))
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, Expires: creds[0].OAuth.Expires}}, "")

	result := markKeyCooldown(ctx, p, "old-access-token", &APIError{StatusCode: 401, Message: "account deactivated"})
	if !result.oauthRefreshed {
		t.Fatal("expected oauthRefreshed=true")
	}
	if result.cooldownApplied {
		t.Fatal("expected cooldownApplied=false after successful refresh")
	}
	if result.refreshedKey != "new-access-token" {
		t.Fatalf("refreshedKey = %q, want new-access-token", result.refreshedKey)
	}

	key, _, err := p.SelectKeyWithContext(ctx)
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "new-access-token" {
		t.Fatalf("selected key = %q, want refreshed key", key)
	}
}

func TestMarkKeyCooldown403OAuthRefreshReturnsRefreshedKey(t *testing.T) {
	ctx := context.Background()
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access-token-403","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{{
		OAuth: &config.OAuthCredential{
			Access:  "old-access-token-403",
			Refresh: "old-refresh-token",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		},
	}}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, config.ExtractAPIKeys(creds))
	p.SetOAuthRefresher(refreshServer.URL, "client-id", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, Expires: creds[0].OAuth.Expires}}, "")

	result := markKeyCooldown(ctx, p, "old-access-token-403", &APIError{StatusCode: 403, Message: "forbidden"})
	if !result.oauthRefreshed {
		t.Fatal("expected oauthRefreshed=true")
	}
	if result.cooldownApplied {
		t.Fatal("expected cooldownApplied=false after successful refresh")
	}
	if result.refreshedKey != "new-access-token-403" {
		t.Fatalf("refreshedKey = %q, want new-access-token-403", result.refreshedKey)
	}
}

func TestMarkKeyCooldown401OAuthNoRefresherPersistsDeactivatedKey(t *testing.T) {
	ctx := context.Background()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:    "oauth-key",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountID: "acc-1", Expires: auth["openai"][0].OAuth.Expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusDeactivated {
		t.Fatal("expected auth config OAuth credential to be marked deactivated")
	}

	_, _, err := p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected NoUsableKeysError after disabling only OAuth key")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestMarkKeyCooldown401OAuthNoRefresherDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	// no OAuthRefresher set → TryRefreshOAuthKey returns false → MarkDeactivated
	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 401, Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldown403OAuthNoRefresherPersistsDeactivatedKey(t *testing.T) {
	ctx := context.Background()
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:    "oauth-key",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acc-1",
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {CredentialIndex: 0, AccountID: "acc-1", Expires: auth["openai"][0].OAuth.Expires},
	}, "")

	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if auth["openai"][0].OAuth.Status != config.OAuthStatusDeactivated {
		t.Fatal("expected auth config OAuth credential to be marked deactivated")
	}

	_, _, err := p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected NoUsableKeysError after disabling only OAuth key")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
}

func TestMarkKeyCooldown403OAuthNoRefresherDeactivatesKey(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Code: "account_deactivated", Message: "account deactivated"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	_, total := p.AvailableKeyCount()
	if total != 0 {
		t.Fatalf("total = %d, want 0: deactivated OAuth key should be excluded", total)
	}
}

func TestMarkKeyCooldown403OAuthNonDeactivationMessageUsesCooldown(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	// generic 403 (e.g. from proxy) — message does not match deactivation keywords
	result := markKeyCooldown(ctx, p, "oauth-key", &APIError{StatusCode: 403, Message: "forbidden"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true")
	}
	if result.deactivatedAccountID != "" || result.deactivatedEmail != "" {
		t.Fatal("expected no deactivation for proxy 403")
	}
	_, total := p.AvailableKeyCount()
	if total != 1 {
		t.Fatalf("total = %d, want 1: proxy 403 should not deactivate key", total)
	}
}

func TestMarkKeyCooldown401NonOAuthUsesCooldown(t *testing.T) {
	ctx := context.Background()
	p := newTestProviderConfig([]string{"plain-key"})
	result := markKeyCooldown(ctx, p, "plain-key", &APIError{StatusCode: 401, Message: "unauthorized"})
	if !result.cooldownApplied {
		t.Fatal("expected cooldownApplied=true for plain key 401")
	}
	// plain key should use cooldown, not deactivation — total stays 1
	_, total := p.AvailableKeyCount()
	if total != 1 {
		t.Fatalf("total = %d, want 1: non-OAuth key should not be deactivated", total)
	}
}

func TestClientComplete401OAuthRefreshRotatesToNextKey(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: "old-access-token", Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "old-access-token" {
		t.Fatalf("first call key = %q, want old-access-token", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientCompleteStreamVisibleInterruptionRetriesSameKey(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "ok from same key"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from same key" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "k1" || impl.apiKeys[1] != "k1" {
		t.Fatalf("expected same key retry after visible interruption, got %#v", impl.apiKeys)
	}
	healthy, total := primaryCfg.HealthyKeyCount()
	if total != 2 || healthy != 2 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 2/2 after same-key recovery", healthy, total)
	}
}

func TestCompleteStreamWithRetryDoesNotResetRetryCountAfterVisibleOutputOnly(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "should not be reached"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.completeStreamWithRetry(
		context.Background(),
		primaryCfg,
		impl,
		"gpt-test",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		false,
		nil,
		1,
		&CallStatus{},
	)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completeStreamWithRetry err = %v, want io.ErrUnexpectedEOF", err)
	}
	if resp != nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 when visible output does not reset retry budget", got)
	}
}

func TestCompleteStreamWithRetryHonorsExplicitMaxAttempts(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{streams: []message.StreamDelta{{Type: "text", Text: "partial-1"}}, err: io.ErrUnexpectedEOF},
		{resp: &message.Response{Content: "should not be reached"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	_, err := c.completeStreamWithRetry(
		context.Background(),
		cfg,
		impl,
		"primary-model",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		false,
		nil,
		1,
		&CallStatus{},
	)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("completeStreamWithRetry err = %v, want io.ErrUnexpectedEOF", err)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 when explicit maxAttempts=1", got)
	}
}

func TestClientCompleteStreamDefaultRetriesOrdinaryErrorsUntilRecovery(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "upstream overloaded"}},
		{resp: &message.Response{Content: "recovered"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "recovered" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retrying into the next round", got)
	}
}

func TestClientCompleteStreamConnectionEstablishmentTimeoutRetriesProviderNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: tlsHandshakeTimeoutErr{}},
		{resp: &message.Response{Content: "recovered after reconnect"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "recovered after reconnect" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retrying the provider in the next round", got)
	}
}

func TestClientCompleteConnectionEstablishmentTimeoutRetriesProviderNextRound(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: tlsHandshakeTimeoutErr{}},
		{resp: &message.Response{Content: "recovered after reconnect"}},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	resp, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if resp == nil || resp.Content != "recovered after reconnect" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retrying the provider in the next round", got)
	}
}

func TestCompleteStreamWithRetryKeepsRetryingWhileAllKeysCooling(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-test", []string{"k1"})
	primaryCfg.MarkCooldown("k1", 20*time.Millisecond)
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{resp: &message.Response{Content: "ok after cooldown"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.completeStreamWithRetry(
		context.Background(),
		primaryCfg,
		impl,
		"gpt-test",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		false,
		nil,
		1,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after cooldown" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if got := impl.CallCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1 after cooldown wait", got)
	}
}

func TestCompleteStreamWithRetryPrefersShortestRoundWaitAcrossModels(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-primary", []string{"k1"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "gpt-fallback", []string{"k2"})
	primaryCfg.MarkCooldown("k1", time.Minute)
	fallbackCfg.MarkCooldown("k2", time.Second)
	implPrimary := &recordingProvider{}
	implFallback := &recordingProvider{}
	implFallback.calls = []scriptedCall{{resp: &message.Response{Content: "ok after short wait"}}}
	c := NewClient(primaryCfg, implPrimary, "gpt-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   implFallback,
		ModelID:        "gpt-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	start := time.Now()
	resp, err := c.completeStreamWithRetry(
		context.Background(),
		primaryCfg,
		implPrimary,
		"gpt-primary",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		c.fallbackModels,
		2,
		&CallStatus{},
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok after short wait" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %v, want to avoid waiting for the 1m primary cooldown", elapsed)
	}
}

func TestMergeRoundWaitPrefersShortestCoolingWindow(t *testing.T) {
	got := mergeRoundWait(0, time.Minute)
	got = mergeRoundWait(got, time.Second)
	if got != time.Second {
		t.Fatalf("mergeRoundWait(1m, 1s) = %v, want 1s", got)
	}
	got = mergeRoundWait(0, 0)
	if got != time.Second {
		t.Fatalf("mergeRoundWait(0, 0) = %v, want 1s clamp", got)
	}
	got = mergeRoundWait(0, 2*time.Minute)
	if got != time.Minute {
		t.Fatalf("mergeRoundWait(0, 2m) = %v, want 1m cap", got)
	}
}

func TestClampEffectiveMaxTokensUsesCurrentRequestEstimate(t *testing.T) {
	model := config.ModelConfig{
		Limit: config.ModelLimit{
			Context: 200000,
			Output:  64000,
		},
	}
	messages := []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 540000), // ~180k tokens
	}}

	got := clampEffectiveMaxTokens(
		model,
		64000,
		64000,
		RequestTuning{},
		"",
		messages,
		nil,
		150000,
	)
	if got != 18000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 18000", got)
	}
}

func TestClampEffectiveMaxTokensStartsShrinkingAtDefaultOutputBoundary(t *testing.T) {
	model := config.ModelConfig{
		Limit: config.ModelLimit{
			Context: 200000,
			Output:  32000,
		},
	}
	messages := []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 504000), // ~168k tokens
	}}

	got := clampEffectiveMaxTokens(
		model,
		32000,
		32000,
		RequestTuning{},
		"",
		messages,
		nil,
		0,
	)
	if got != 30000 {
		t.Fatalf("clampEffectiveMaxTokens() = %d, want 30000", got)
	}
}

func TestEstimateRequestInputTokensIncludesSystemPromptAndTools(t *testing.T) {
	systemPrompt := strings.Repeat("s", 3000) // ~1000
	messages := []message.Message{{
		Role:    "user",
		Content: strings.Repeat("m", 6000), // ~2000
	}}
	tools := []message.ToolDefinition{{
		Name:        "Read",
		Description: strings.Repeat("d", 1500),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": strings.Repeat("p", 1500),
				},
			},
		},
	}}

	got := estimateRequestInputTokens(systemPrompt, messages, tools)
	if got <= estimateInputTokens(messages) {
		t.Fatalf("estimateRequestInputTokens() = %d, want to exceed bare message estimate", got)
	}
}

func TestCompleteStreamCoolingStatusUsesMergedRoundWait(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "gpt-primary", []string{"k1"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "gpt-fallback", []string{"k2"})
	primaryCfg.MarkCooldown("k1", 700*time.Millisecond)
	fallbackCfg.MarkCooldown("k2", time.Minute)
	implPrimary := &recordingProvider{}
	implPrimary.calls = []scriptedCall{{resp: &message.Response{Content: "ok after short cooling"}}}
	implFallback := &recordingProvider{}
	c := NewClient(primaryCfg, implPrimary, "gpt-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   implFallback,
		ModelID:        "gpt-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	var coolingDetails []string
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == "status" && delta.Status != nil && delta.Status.Type == "cooling" {
			coolingDetails = append(coolingDetails, delta.Status.Detail)
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok after short cooling" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(coolingDetails) == 0 {
		t.Fatal("expected at least one cooling status")
	}
	if got := coolingDetails[len(coolingDetails)-1]; got != "1s" {
		t.Fatalf("last cooling detail = %q, want 1s", got)
	}
}

func TestClientCompleteStream401OAuthRefreshRotatesToNextKey(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: "old-access-token", Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "old-access-token" {
		t.Fatalf("first call key = %q, want old-access-token", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientComplete403OAuthRefreshRotatesToNextKey(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access-token-403","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: "old-access-token-403", Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, Expires: creds[0].OAuth.Expires}}, "")

	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 403, Message: "forbidden"}},
		{resp: &message.Response{Content: "ok from key-2"}},
	}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from key-2" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "old-access-token-403" {
		t.Fatalf("first call key = %q, want old-access-token-403", impl.apiKeys[0])
	}
	if impl.apiKeys[1] != "key-2" {
		t.Fatalf("second call key = %q, want key-2", impl.apiKeys[1])
	}
}

func TestClientCompleteStreamDeactivatedOnlyOAuthKeyReturnsNoUsableKeys(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:    "deactivated-oauth-key",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acc-deactivated",
		Status:    config.OAuthStatusDeactivated,
	}}}}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"deactivated-oauth-key"})
	primaryCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", &auth, &authMu, map[string]OAuthKeySetup{
		"deactivated-oauth-key": {CredentialIndex: 0, AccountID: "acc-deactivated", Expires: auth["openai"][0].OAuth.Expires, Status: config.OAuthStatusDeactivated},
	}, "")

	impl := &recordingProvider{}
	c := NewClient(primaryCfg, impl, "gpt-test", 4096, "sys")

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected NoUsableKeysError")
	}
	var noUsable *NoUsableKeysError
	if !errors.As(err, &noUsable) {
		t.Fatalf("expected NoUsableKeysError, got %T: %v", err, err)
	}
	if len(impl.apiKeys) != 0 {
		t.Fatalf("expected no provider calls, got %d", len(impl.apiKeys))
	}
}

func TestClientCompleteStream401OAuthRefreshThenFallback(t *testing.T) {
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: "old-access-token", Refresh: "refresh-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		{APIKey: "key-2"},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-test": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, config.ExtractAPIKeys(creds))
	primaryCfg.SetOAuthRefresher(refreshServer.URL, "client-id", "", &auth, &authMu, map[string]OAuthKeySetup{creds[0].OAuth.Access: {CredentialIndex: 0, Expires: creds[0].OAuth.Expires}}, "")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 401, Message: "account deactivated"}},
		{err: &APIError{StatusCode: 401, Message: "invalid api key"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}}
	c := NewClient(primaryCfg, primaryImpl, "gpt-test", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(primaryImpl.apiKeys) != 2 {
		t.Fatalf("expected 2 initial-entry calls, got %d", len(primaryImpl.apiKeys))
	}
	if primaryImpl.apiKeys[0] != "old-access-token" || primaryImpl.apiKeys[1] != "key-2" {
		t.Fatalf("initial-entry keys = %#v, want [old-access-token key-2]", primaryImpl.apiKeys)
	}
	if len(fallbackImpl.apiKeys) != 1 {
		t.Fatalf("expected 1 fallback call, got %d", len(fallbackImpl.apiKeys))
	}
	st := c.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected fallback to trigger")
	}
}

func TestClientFallbackChainUsedOnce(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: &APIError{StatusCode: 413, Message: "context too long"}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "ok from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{
		{
			ProviderConfig: fallbackCfg,
			ProviderImpl:   fallbackImpl,
			ModelID:        "fallback-model",
			MaxTokens:      4096,
			ContextLimit:   128000,
		},
	})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("expected success with fallback, got error: %v", err)
	}
	if resp == nil || resp.Content != "ok from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	if got := primaryImpl.CallCount(); got != 1 {
		t.Fatalf("expected initial pool entry called once, got %d", got)
	}
	if got := fallbackImpl.CallCount(); got != 1 {
		t.Fatalf("expected fallback called once, got %d", got)
	}

	st := c.LastCallStatus()
	if !st.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if st.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("unexpected running model ref: %q", st.RunningModelRef)
	}
	if st.RunningContextLimit != 128000 {
		t.Fatalf("unexpected running context limit: %d", st.RunningContextLimit)
	}
}

func TestClientKeyStatsFollowRunningFallbackProvider(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 413, Message: "context too long"}}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{{resp: &message.Response{Content: "ok from fallback"}}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	if available, total := c.KeyStats(); available != 10 || total != 10 {
		t.Fatalf("initial KeyStats = %d/%d, want 10/10", available, total)
	}

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}

	if available, total := c.KeyStats(); available != 1 || total != 1 {
		t.Fatalf("fallback KeyStats = %d/%d, want 1/1", available, total)
	}
}

func TestKeyStatsForRef_matchesPrimaryWhenLastCallUsedFallback(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"p1", "p2"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{{err: &APIError{StatusCode: 413, Message: "context too long"}}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{{resp: &message.Response{Content: "ok"}}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	if _, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	gotAvail, gotTot := c.KeyStats()
	if gotAvail != 1 || gotTot != 1 {
		t.Fatalf("KeyStats (lastCall=fallback) = %d/%d, want 1/1", gotAvail, gotTot)
	}
	// Sidebar MODEL may still show selected primary while MainAgent uses this ref:
	pAvail, pTot := c.KeyStatsForRef("primary-prov/primary-model")
	if pAvail != 2 || pTot != 2 {
		t.Fatalf("KeyStatsForRef(primary) = %d/%d, want 2/2", pAvail, pTot)
	}
}

func TestKeyStatsForRefIgnoresInlineVariantSuffix(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5-mini", []string{"k1", "k2", "k3"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{}
	fallbackImpl := &scriptedProvider{}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5-mini", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	avail, total := c.KeyStatsForRef("codex/gpt-5.5-mini@high")
	if avail != 3 || total != 3 {
		t.Fatalf("KeyStatsForRef(codex/gpt-5.5-mini@high) = %d/%d, want 3/3", avail, total)
	}
}

func TestCurrentRateLimitSnapshotForRefIgnoresInlineVariantSuffix(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("codex", "gpt-5.5-mini", []string{"k1", "k2"})
	fallbackCfg := testProviderConfigWithKeys("fallback-prov", "fallback-model", []string{"fb1"})

	primaryImpl := &scriptedProvider{}
	fallbackImpl := &scriptedProvider{}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5-mini", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "codex",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 42},
	}
	primaryCfg.UpdateKeySnapshot("k1", snap)
	if _, _, err := primaryCfg.SelectKeyWithContext(context.Background()); err != nil {
		t.Fatalf("SelectKeyWithContext() error = %v", err)
	}

	got := c.CurrentRateLimitSnapshotForRef("codex/gpt-5.5-mini@high")
	if got != snap {
		t.Fatalf("CurrentRateLimitSnapshotForRef(codex/gpt-5.5-mini@high) = %#v, want %#v", got, snap)
	}
}

func TestSetModelPoolWrapAround(t *testing.T) {
	cfg0 := testProviderConfig("prov", "model0")
	cfg1 := testProviderConfig("prov", "model1")
	cfg2 := testProviderConfig("prov", "model2")
	cfg3 := testProviderConfig("prov", "model3")

	// model0 and model2 fail; model3 succeeds.
	impl0 := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fail"}}}}
	impl1 := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fail"}}}}
	impl2 := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "from model2"}}}}
	impl3 := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "from model3"}}}}

	pool := []FallbackModel{
		{ProviderConfig: cfg0, ProviderImpl: impl0, ModelID: "model0", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg1, ProviderImpl: impl1, ModelID: "model1", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg2, ProviderImpl: impl2, ModelID: "model2", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg3, ProviderImpl: impl3, ModelID: "model3", MaxTokens: 4096, ContextLimit: 128000},
	}

	// Select model1 (index 1): retry order should be model1 -> model2 -> model3 -> model0.
	// model1 fails, model2 succeeds.
	c := NewClient(cfg1, impl1, "model1", 4096, "sys")
	c.SetModelPool(pool, 1)

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp == nil || resp.Content != "from model2" {
		t.Fatalf("expected 'from model2', got: %#v", resp)
	}
	st := c.LastCallStatus()
	if st.RunningModelRef != "prov/model2" {
		t.Fatalf("expected running=prov/model2, got %q", st.RunningModelRef)
	}
	if st.RunningContextLimit != 128000 {
		t.Fatalf("expected running context limit=128000, got %d", st.RunningContextLimit)
	}
	if !st.FallbackTriggered {
		t.Fatal("expected model-pool advance to set FallbackTriggered=true")
	}
}

func TestModelPoolStickyCursorKeepsSuccessfulModel(t *testing.T) {
	cfg0 := testProviderConfig("prov", "model0")
	cfg1 := testProviderConfig("prov", "model1")
	cfg2 := testProviderConfig("prov", "model2")

	impl0 := &scriptedProvider{calls: []scriptedCall{
		{err: &APIError{StatusCode: 500, Message: "m0-fail-1"}},
		{err: &APIError{StatusCode: 500, Message: "m0-fail-2"}},
	}}
	impl1 := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "from model1 call1"}},
		{resp: &message.Response{Content: "from model1 call2"}},
	}}
	impl2 := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "from model2"}},
	}}

	pool := []FallbackModel{
		{ProviderConfig: cfg0, ProviderImpl: impl0, ModelID: "model0", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg1, ProviderImpl: impl1, ModelID: "model1", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: cfg2, ProviderImpl: impl2, ModelID: "model2", MaxTokens: 4096, ContextLimit: 128000},
	}

	c := NewClient(cfg0, impl0, "model0", 4096, "sys")
	c.SetModelPool(pool, 0)

	resp1, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-1"}}, nil, nil)
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if resp1 == nil || resp1.Content != "from model1 call1" {
		t.Fatalf("first response = %#v, want from model1 call1", resp1)
	}
	st1 := c.LastCallStatus()
	if st1.SelectedModelRef != "prov/model0" {
		t.Fatalf("first cursor-start SelectedModelRef = %q, want prov/model0", st1.SelectedModelRef)
	}
	if st1.RunningModelRef != "prov/model1" {
		t.Fatalf("first RunningModelRef = %q, want prov/model1", st1.RunningModelRef)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "from model1 call2" {
		t.Fatalf("second response = %#v, want from model1 call2", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "prov/model1" {
		t.Fatalf("second cursor-start SelectedModelRef = %q, want prov/model1", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "prov/model1" {
		t.Fatalf("second RunningModelRef = %q, want prov/model1", st2.RunningModelRef)
	}
	if impl0.CallCount() != 1 {
		t.Fatalf("model0 calls = %d, want 1 (should not reset to model0 on second request)", impl0.CallCount())
	}
}

func TestModelPoolStickyCursorPreservesPinnedFallbackVariant(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := NewProviderConfig("qt", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Variants: map[string]config.ModelVariant{
					"xhigh": {Reasoning: &config.ReasoningConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"fb-key"})

	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "primary failed"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "fallback first"}},
		{resp: &message.Response{Content: "fallback second"}},
	}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "primary-model", MaxTokens: 4096, ContextLimit: 128000},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "gpt-5.5", MaxTokens: 4096, ContextLimit: 128000, Variant: "xhigh"},
	}, 0)

	resp1, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-1"}}, nil, nil)
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if resp1 == nil || resp1.Content != "fallback first" {
		t.Fatalf("first response = %#v, want fallback first", resp1)
	}
	st1 := c.LastCallStatus()
	if st1.RunningModelRef != "qt/gpt-5.5@xhigh" {
		t.Fatalf("first RunningModelRef = %q, want qt/gpt-5.5@xhigh", st1.RunningModelRef)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "fallback second" {
		t.Fatalf("second response = %#v, want fallback second", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "qt/gpt-5.5@xhigh" {
		t.Fatalf("second SelectedModelRef = %q, want qt/gpt-5.5@xhigh", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "qt/gpt-5.5@xhigh" {
		t.Fatalf("second RunningModelRef = %q, want qt/gpt-5.5@xhigh", st2.RunningModelRef)
	}
}

func TestModelPoolStickyCursorDoesNotLeakPrimaryVariantToVariantlessPinnedFallback(t *testing.T) {
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 400000, Output: 128000},
				Variants: map[string]config.ModelVariant{
					"high": {Thinking: &config.ThinkingConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})
	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fb-key"})

	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "fail primary"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{
		{resp: &message.Response{Content: "fallback first"}},
		{resp: &message.Response{Content: "fallback second"}},
	}}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5", 4096, "sys")
	c.SetVariant("high")
	c.SetModelPool([]FallbackModel{
		{ProviderConfig: primaryCfg, ProviderImpl: primaryImpl, ModelID: "gpt-5.5", MaxTokens: 4096, ContextLimit: 400000, Variant: "high"},
		{ProviderConfig: fallbackCfg, ProviderImpl: fallbackImpl, ModelID: "fallback-model", MaxTokens: 4096, ContextLimit: 128000},
	}, 0)

	resp1, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-1"}}, nil, nil)
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if resp1 == nil || resp1.Content != "fallback first" {
		t.Fatalf("first response = %#v, want fallback first", resp1)
	}
	st1 := c.LastCallStatus()
	if st1.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("first RunningModelRef = %q, want fallback-prov/fallback-model", st1.RunningModelRef)
	}

	resp2, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi-2"}}, nil, nil)
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
	if resp2 == nil || resp2.Content != "fallback second" {
		t.Fatalf("second response = %#v, want fallback second", resp2)
	}
	st2 := c.LastCallStatus()
	if st2.SelectedModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("second SelectedModelRef = %q, want fallback-prov/fallback-model", st2.SelectedModelRef)
	}
	if st2.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("second RunningModelRef = %q, want fallback-prov/fallback-model", st2.RunningModelRef)
	}
}

func TestClientReadTimeoutTriesNextKeyBeforeFallbackModel(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: testNetTimeoutErr{timeout: true}},
		{err: testNetTimeoutErr{timeout: true}},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 2 {
		t.Fatalf("initial-entry CallCount=%d, want 2 (both keys before advancing to the next pool entry)", primaryImpl.CallCount())
	}
	if len(primaryImpl.apiKeys) != 2 || primaryImpl.apiKeys[0] != "k1" || primaryImpl.apiKeys[1] != "k2" {
		t.Fatalf("initial-entry keys = %#v, want [k1 k2]", primaryImpl.apiKeys)
	}
	if fallbackImpl.CallCount() != 1 {
		t.Fatalf("fallback CallCount=%d, want 1", fallbackImpl.CallCount())
	}
}

func TestClientRetryEmitsRollbackAfterVisibleToolStream(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{
				streams: []message.StreamDelta{{
					Type:     "tool_use_start",
					ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"},
				}},
				err: testNetTimeoutErr{timeout: true},
			},
			{
				resp: &message.Response{Content: "second key ok"},
			},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "second key ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	var sawToolStart, sawRollback bool
	for _, d := range deltas {
		if d.Type == "tool_use_start" {
			sawToolStart = true
		}
		if d.Type == "rollback" {
			sawRollback = true
		}
	}
	if !sawToolStart {
		t.Fatal("expected tool_use_start before retry")
	}
	if !sawRollback {
		t.Fatal("expected rollback after visible failed attempt")
	}
}

func TestClientCompleteReadTimeoutSecondKeySucceedsNoFallback(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: testNetTimeoutErr{timeout: true}},
		{resp: &message.Response{Content: "second key ok"}},
	}
	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "second key ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 2 {
		t.Fatalf("initial-entry CallCount=%d, want 2", primaryImpl.CallCount())
	}
	if len(primaryImpl.apiKeys) != 2 || primaryImpl.apiKeys[0] != "k1" || primaryImpl.apiKeys[1] != "k2" {
		t.Fatalf("initial-entry keys = %#v, want [k1 k2]", primaryImpl.apiKeys)
	}
}

func TestClientReadTimeoutSecondKeySucceedsNoFallback(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{err: testNetTimeoutErr{timeout: true}},
		{resp: &message.Response{Content: "second key ok"}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "second key ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 2 {
		t.Fatalf("initial-entry CallCount=%d, want 2", primaryImpl.CallCount())
	}
	if len(primaryImpl.apiKeys) != 2 || primaryImpl.apiKeys[0] != "k1" || primaryImpl.apiKeys[1] != "k2" {
		t.Fatalf("initial-entry keys = %#v, want [k1 k2]", primaryImpl.apiKeys)
	}
}

func TestClientEmptyStopResponseFallsBack(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{StopReason: "stop"}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1", primaryImpl.CallCount())
	}
	if fallbackImpl.CallCount() != 1 {
		t.Fatalf("fallback CallCount=%d, want 1", fallbackImpl.CallCount())
	}
}

func TestClientCompleteStreamClampsOutputToRemainingContextWithoutLastUsage(t *testing.T) {
	cfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{
					Context: 200000,
					Output:  64000,
				},
			},
		},
	}, []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{resp: &message.Response{Content: "ok"}}}

	c := NewClient(cfg, impl, "primary-model", 64000, "")
	c.SetOutputTokenMax(64000)

	_, err := c.CompleteStream(context.Background(), []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 540000), // ~180k tokens
	}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(impl.maxTokens) != 1 {
		t.Fatalf("provider maxTokens calls = %d, want 1", len(impl.maxTokens))
	}
	if impl.maxTokens[0] != 18000 {
		t.Fatalf("provider maxTokens[0] = %d, want 18000", impl.maxTokens[0])
	}
}

func TestClientCompleteStreamReusesClampedOutputBudgetAcrossKeyRetries(t *testing.T) {
	cfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{
					Context: 202752,
					Output:  65536,
				},
			},
		},
	}, []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 429, Message: "Too many concurrent requests for this model"}},
		{resp: &message.Response{Content: "ok"}},
	}

	c := NewClient(cfg, impl, "primary-model", 65536, "")

	_, err := c.CompleteStream(context.Background(), []message.Message{{
		Role:    "user",
		Content: "hi",
	}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(impl.maxTokens) != 2 {
		t.Fatalf("provider maxTokens calls = %d, want 2", len(impl.maxTokens))
	}
	for i, got := range impl.maxTokens {
		if got != DefaultOutputTokenMax {
			t.Fatalf("provider maxTokens[%d] = %d, want %d", i, got, DefaultOutputTokenMax)
		}
	}
}

func TestClientCompleteReusesContextClampedOutputBudgetAcrossKeyRetries(t *testing.T) {
	cfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {
				Limit: config.ModelLimit{
					Context: 200000,
					Output:  64000,
				},
			},
		},
	}, []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{err: &APIError{StatusCode: 429, Message: "rate limited"}},
		{resp: &message.Response{Content: "ok"}},
	}

	c := NewClient(cfg, impl, "primary-model", 64000, "")
	c.SetOutputTokenMax(64000)

	_, err := c.CompleteStream(context.Background(), []message.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 540000), // ~180k tokens
	}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if len(impl.maxTokens) != 2 {
		t.Fatalf("provider maxTokens calls = %d, want 2", len(impl.maxTokens))
	}
	for i, got := range impl.maxTokens {
		if got != 18000 {
			t.Fatalf("provider maxTokens[%d] = %d, want 18000", i, got)
		}
	}
}

func TestClientCompleteEmptyResponseMarksKeyRecovering(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")
	primaryImpl := &recordingProvider{}
	primaryImpl.calls = []scriptedCall{
		{resp: &message.Response{StopReason: "stop"}},
	}
	fallbackImpl := &recordingProvider{}
	fallbackImpl.calls = []scriptedCall{
		{resp: &message.Response{Content: "from fallback"}},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	key, switched, err := primaryCfg.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "k2" {
		t.Fatalf("selected key after empty response = %q, want k2", key)
	}
	if !switched {
		t.Fatal("expected recovering empty-response key to yield a slot switch to k2")
	}
}

func TestClientCompleteStopsAfterModelIncompatible400WithoutFallback(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	impl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}}

	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")
	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want APIError 400", err)
	}
	if impl.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", impl.calls)
	}
}

func TestClientCompleteStreamStopsAfterModelIncompatible400WhenFallbackPoolExhausted(t *testing.T) {
	primaryCfg := testProviderConfig("primary-prov", "primary-model")
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")
	primaryImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}}
	fallbackImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("err = %v, want APIError 400", err)
	}
	if primaryImpl.calls != 1 {
		t.Fatalf("initial-entry calls = %d, want 1", primaryImpl.calls)
	}
	if fallbackImpl.calls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackImpl.calls)
	}
	status := c.LastCallStatus()
	if !status.FallbackTriggered {
		t.Fatal("expected FallbackTriggered=true")
	}
	if !status.FallbackExhausted {
		t.Fatal("expected FallbackExhausted=true")
	}
}

func TestClientDialTimeoutSkipsRemainingKeys(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	dialTimeout := &net.OpError{Op: "dial", Err: testNetTimeoutErr{timeout: true}}
	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: dialTimeout},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1 (dial failure skips second key)", primaryImpl.CallCount())
	}
}

func TestClientTLSHandshakeTimeoutSkipsRemainingKeys(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	fallbackCfg := testProviderConfig("fallback-prov", "fallback-model")

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: tlsHandshakeTimeoutErr{}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "from fallback"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "from fallback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if primaryImpl.CallCount() != 1 {
		t.Fatalf("initial-entry CallCount=%d, want 1 (TLS handshake timeout skips second key)", primaryImpl.CallCount())
	}
}

func TestCompleteStreamDoesNotRetryOtherKeysWhenContextCancelled(t *testing.T) {
	cfg := testProviderConfigWithKeys("p", "m", []string{"k1", "k2"})
	impl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "should not be reached"}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := NewClient(cfg, impl, "m", 4096, "sys")
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if impl.CallCount() != 0 {
		t.Fatalf("provider CallCount=%d, want 0 (cancel should short-circuit before any request)", impl.CallCount())
	}
}

func TestClientVisibleToolStreamRetriesSameKey(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1", "k2"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{
			streams: []message.StreamDelta{{
				Type:     "tool_use_start",
				ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"},
			}},
			err: testNetTimeoutErr{timeout: true},
		},
		{
			resp: &message.Response{Content: "ok from same key"},
		},
	}
	c := NewClient(primaryCfg, impl, "primary-model", 4096, "sys")

	var deltas []message.StreamDelta
	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok from same key" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(impl.apiKeys) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(impl.apiKeys))
	}
	if impl.apiKeys[0] != "k1" || impl.apiKeys[1] != "k1" {
		t.Fatalf("expected same key retry after visible tool interruption, got %#v", impl.apiKeys)
	}

	var sawToolStart, sawRollback, sawSameKeyRetry bool
	for _, d := range deltas {
		if d.Type == "tool_use_start" {
			sawToolStart = true
		}
		if d.Type == "rollback" {
			sawRollback = true
		}
		if d.Type == "status" && d.Status != nil && d.Status.Type == "retrying" && d.Status.Detail == "same key" {
			sawSameKeyRetry = true
		}
	}
	if !sawToolStart {
		t.Fatal("expected tool_use_start before retry")
	}
	if !sawRollback {
		t.Fatal("expected rollback after visible failed attempt")
	}
	if !sawSameKeyRetry {
		t.Fatal("expected same-key retry status after visible tool interruption")
	}
	healthy, total := primaryCfg.HealthyKeyCount()
	if total != 2 || healthy != 2 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 2/2 after same-key recovery", healthy, total)
	}
}

func TestClientVisibleRollbackCancelStopsSameKeyRetry(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{
		{
			streams: []message.StreamDelta{{
				Type:     "tool_use_start",
				ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"},
			}},
			err: testNetTimeoutErr{timeout: true},
		},
		{
			resp: &message.Response{Content: "should not retry after cancel"},
		},
	}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deltas []message.StreamDelta
	_, err := c.CompleteStream(ctx, []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		deltas = append(deltas, delta)
		if delta.Type == "rollback" {
			cancel()
		}
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if impl.CallCount() != 1 {
		t.Fatalf("provider CallCount=%d, want 1 (no same-key retry after cancel)", impl.CallCount())
	}
	for _, d := range deltas {
		if d.Type == "status" && d.Status != nil && d.Status.Type == "retrying" && d.Status.Detail == "same key" {
			t.Fatal("unexpected same-key retry status after cancellation")
		}
	}
}

func TestClientSetVariantResetsModelDefaultsBeforeApplyingNewVariant(t *testing.T) {
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Thinking: &config.ThinkingConfig{
					Type:    "adaptive",
					Effort:  "medium",
					Display: "summarized",
				},
				Variants: map[string]config.ModelVariant{
					"high": {
						Thinking: &config.ThinkingConfig{Effort: "high"},
					},
					"hidden": {
						Thinking: &config.ThinkingConfig{Display: "omitted"},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("high")
	if got := c.tuning.Anthropic.ThinkingEffort; got != "high" {
		t.Fatalf("after @high effort = %q, want high", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "summarized" {
		t.Fatalf("after @high display = %q, want summarized", got)
	}

	c.SetVariant("hidden")
	if got := c.tuning.Anthropic.ThinkingEffort; got != "medium" {
		t.Fatalf("after switching to @hidden effort = %q, want model default medium", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "omitted" {
		t.Fatalf("after switching to @hidden display = %q, want omitted", got)
	}
}

func TestClientSetVariantBudgetOnlyDoesNotImplicitlySwitchThinkingType(t *testing.T) {
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Thinking: &config.ThinkingConfig{
					Type:    "adaptive",
					Effort:  "medium",
					Display: "summarized",
				},
				Variants: map[string]config.ModelVariant{
					"manual": {
						Thinking: &config.ThinkingConfig{Budget: 12000},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("manual")
	if got := c.tuning.Anthropic.ThinkingType; got != "adaptive" {
		t.Fatalf("after @manual type = %q, want adaptive", got)
	}
	if got := c.tuning.Anthropic.ThinkingBudget; got != 0 {
		t.Fatalf("after @manual budget = %d, want 0", got)
	}
	if got := c.tuning.Anthropic.ThinkingEffort; got != "medium" {
		t.Fatalf("after @manual effort = %q, want medium", got)
	}
	if got := c.tuning.Anthropic.ThinkingDisplay; got != "summarized" {
		t.Fatalf("after @manual display = %q, want summarized", got)
	}
}

func TestClientSetVariantMergesPromptCacheOverrides(t *testing.T) {
	cacheToolsFalse := false
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				PromptCache: &config.PromptCacheConfig{
					Mode:       "auto",
					TTL:        "1h",
					CacheTools: boolPtr(cacheToolsFalse),
				},
				Variants: map[string]config.ModelVariant{
					"explicit-tools": {
						PromptCache: &config.PromptCacheConfig{
							Mode:       "explicit",
							CacheTools: boolPtr(true),
						},
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "claude", 4096, "sys")

	c.SetVariant("explicit-tools")
	if got := c.tuning.Anthropic.PromptCacheMode; got != "explicit" {
		t.Fatalf("PromptCacheMode = %q, want explicit", got)
	}
	if got := c.tuning.Anthropic.PromptCacheTTL; got != "1h" {
		t.Fatalf("PromptCacheTTL = %q, want inherited 1h", got)
	}
	if !c.tuning.Anthropic.CacheTools {
		t.Fatal("CacheTools = false, want true")
	}
}

func TestClientSetVariantOverridesParallelToolCalls(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				ParallelToolCalls: boolPtr(false),
				Variants: map[string]config.ModelVariant{
					"parallel": {
						ParallelToolCalls: boolPtr(true),
					},
				},
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, &scriptedProvider{}, "gpt-5.5", 4096, "sys")

	if c.tuning.OpenAI.ParallelToolCalls == nil || *c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("default parallel_tool_calls = %#v, want false", c.tuning.OpenAI.ParallelToolCalls)
	}

	c.SetVariant("parallel")
	if c.tuning.OpenAI.ParallelToolCalls == nil || !*c.tuning.OpenAI.ParallelToolCalls {
		t.Fatalf("after @parallel parallel_tool_calls = %#v, want true", c.tuning.OpenAI.ParallelToolCalls)
	}
}

func TestClientRunningModelRefIncludesVariantOnPrimarySuccess(t *testing.T) {
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 400000, Output: 128000},
				Variants: map[string]config.ModelVariant{
					"high": {Thinking: &config.ThinkingConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})

	impl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "ok"}},
		},
	}

	c := NewClient(cfg, impl, "gpt-5.5", 4096, "sys")
	c.SetVariant("high")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	st := c.LastCallStatus()
	if st.RunningModelRef != "openai/gpt-5.5@high" {
		t.Fatalf("RunningModelRef = %q, want openai/gpt-5.5@high", st.RunningModelRef)
	}
}

func TestClientRunningModelRefOmitsVariantOnFallbackSuccess(t *testing.T) {
	primaryCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit: config.ModelLimit{Context: 400000, Output: 128000},
				Variants: map[string]config.ModelVariant{
					"high": {Thinking: &config.ThinkingConfig{Effort: "high"}},
				},
			},
		},
	}, []string{"k"})

	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
			},
		},
	}, []string{"fb-key"})

	primaryImpl := &scriptedProvider{
		calls: []scriptedCall{
			{err: &APIError{StatusCode: 500, Message: "fail"}},
		},
	}
	fallbackImpl := &scriptedProvider{
		calls: []scriptedCall{
			{resp: &message.Response{Content: "fallback ok"}},
		},
	}

	c := NewClient(primaryCfg, primaryImpl, "gpt-5.5", 4096, "sys")
	c.SetVariant("high")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "fallback ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	st := c.LastCallStatus()
	if st.RunningModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("RunningModelRef = %q, want fallback-prov/fallback-model (no variant leak)", st.RunningModelRef)
	}
}

func TestClientNextRequestTuningOverrideIsOneShot(t *testing.T) {
	provider := &recordingTuningProvider{}
	cfg := NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {
				Limit:             config.ModelLimit{Context: 400000, Output: 128000},
				ParallelToolCalls: boolPtr(true),
			},
		},
	}, []string{"k"})
	c := NewClient(cfg, provider, "gpt-5.5", 4096, "sys")
	parallelFalse := false
	parallelTrue := true
	c.SetNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ParallelToolCalls: &parallelFalse}})
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if len(provider.tuning) != 2 {
		t.Fatalf("len(provider.tuning) = %d, want 2", len(provider.tuning))
	}
	if provider.tuning[0].OpenAI.ParallelToolCalls == nil || *provider.tuning[0].OpenAI.ParallelToolCalls != parallelFalse {
		t.Fatalf("first tuning = %#v, want parallel_tool_calls=false", provider.tuning[0])
	}
	if provider.tuning[1].OpenAI.ParallelToolCalls == nil || *provider.tuning[1].OpenAI.ParallelToolCalls != parallelTrue {
		t.Fatalf("second tuning = %#v, want parallel_tool_calls=true", provider.tuning[1])
	}
}

func TestCompleteStreamFallbackRunningModelRefIncludesFallbackVariant(t *testing.T) {
	primaryCfg := NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	primaryCfg.MarkCooldown("k1", time.Minute)

	fallbackCfg := NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				Variants: map[string]config.ModelVariant{
					"high": {},
				},
			},
		},
	}, []string{"k2"})

	implPrimary := &recordingProvider{}
	implFallback := &recordingProvider{}
	implFallback.calls = []scriptedCall{{resp: &message.Response{Content: "ok"}}}

	c := NewClient(primaryCfg, implPrimary, "primary-model", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   implFallback,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
		Variant:        "high",
	}})

	st := &CallStatus{}
	resp, err := c.completeStreamWithRetry(
		context.Background(),
		primaryCfg,
		implPrimary,
		"primary-model",
		4096,
		RequestTuning{},
		"",
		[]message.Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		true,
		c.fallbackModels,
		1,
		st,
	)
	if err != nil {
		t.Fatalf("completeStreamWithRetry returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if st.RunningModelRef != "fallback-prov/fallback-model@high" {
		t.Fatalf("RunningModelRef = %q, want fallback-prov/fallback-model@high", st.RunningModelRef)
	}
}

func TestCompleteStreamFallbackVariantCarriesPromptCacheTuning(t *testing.T) {
	expect := RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "auto", PromptCacheTTL: "1h", CacheTools: true}}
	primaryCfg := NewProviderConfig("primary", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude-primary": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"k1"})
	fallbackCfg := NewProviderConfig("fallback", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"claude-fallback": {
				Limit: config.ModelLimit{Context: 128000, Output: 4096},
				PromptCache: &config.PromptCacheConfig{
					Mode:       "explicit",
					TTL:        "1h",
					CacheTools: boolPtr(false),
				},
				Variants: map[string]config.ModelVariant{
					"fastcache": {
						PromptCache: &config.PromptCacheConfig{
							Mode:       "auto",
							CacheTools: boolPtr(true),
						},
					},
				},
			},
		},
	}, []string{"k2"})
	primaryImpl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500, Message: "primary failed"}}}}
	fallbackImpl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "fallback ok"}, expectTuning: &expect}}}
	c := NewClient(primaryCfg, primaryImpl, "claude-primary", 4096, "sys")
	c.SetFallbackModels([]FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "claude-fallback",
		MaxTokens:      4096,
		ContextLimit:   128000,
		Variant:        "fastcache",
	}})

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.Content != "fallback ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
