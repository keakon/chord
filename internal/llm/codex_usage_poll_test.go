package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

type noopProvider struct{}

func (noopProvider) CompleteStream(
	context.Context,
	string,
	string,
	string,
	[]message.Message,
	[]message.ToolDefinition,
	int,
	RequestTuning,
	StreamCallback,
) (*message.Response, error) {
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func (noopProvider) Complete(
	context.Context,
	string,
	string,
	string,
	[]message.Message,
	[]message.ToolDefinition,
	int,
	RequestTuning,
) (*message.Response, error) {
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func TestResolveCodexUsageURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{
			in:   "https://chatgpt.com/backend-api/codex/responses",
			want: "https://chatgpt.com/backend-api/wham/usage",
		},
		{
			in:   "https://chatgpt.com/backend-api/codex/responses/",
			want: "https://chatgpt.com/backend-api/wham/usage",
		},
		{
			in:   "https://chatgpt.com",
			want: "https://chatgpt.com/backend-api/wham/usage",
		},
	}
	for _, tc := range tests {
		got, err := resolveCodexUsageURL(tc.in)
		if err != nil {
			t.Fatalf("resolveCodexUsageURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("resolveCodexUsageURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCodexWarmupMarksDeactivatedOAuthCredential(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authPath, []byte(`openai:
  - refresh: refresh-a
    access: access-a
    expires: 32503680000000
    account_id: acc-a
  - refresh: refresh-b
    access: access-b
    expires: 32503680000000
    account_id: acc-b
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}

	seenA := make(chan struct{}, 1)
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("ChatGPT-Account-ID") {
		case "acc-a":
			select {
			case seenA <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"account deactivated","code":"account_deactivated"}}`)
		case "acc-b":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":20,"reset_after_seconds":3600}},"credits":{"has_credits":true,"unlimited":false}}`)
		default:
			t.Errorf("unexpected account id %q", r.Header.Get("ChatGPT-Account-ID"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer usageServer.Close()

	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:     config.ProviderTypeResponses,
		APIURL:   usageServer.URL + "/backend-api/codex/responses",
		Preset:   config.ProviderPresetCodex,
		KeyOrder: config.KeyOrderSequential,
	}, config.ExtractAPIKeys(auth["openai"]))
	prov.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, authPath, &auth, &authMu, map[string]OAuthKeySetup{
		"access-a": {CredentialIndex: 0, AccountID: "acc-a", Expires: 32503680000000},
		"access-b": {CredentialIndex: 1, AccountID: "acc-b", Expires: 32503680000000},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		return nil, nil
	})
	defer prov.StopCodexRateLimitPolling()

	if !prov.StartCodexWarmup(t.Context()) {
		t.Fatal("expected codex warmup to start")
	}
	select {
	case <-seenA:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup did not probe deactivated account")
	}

	waitForOAuthStatusInAuthFile(t, authPath, "access-a", config.OAuthStatusDeactivated)
	updated, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig(updated): %v", err)
	}
	if got := updated["openai"][1].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected sibling OAuth credential to remain normal, got %#v", got)
	}
}

func TestCodexWarmupMarksExpiredOAuthCredentialWhenRefreshTokenInvalid(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(authPath, []byte(`openai:
  - refresh: refresh-a
    access: access-a
    expires: 32503680000000
    account_id: acc-a
  - refresh: refresh-b
    access: access-b
    expires: 32503680000000
    account_id: acc-b
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}

	seenA := make(chan struct{}, 1)
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("ChatGPT-Account-ID") {
		case "acc-a":
			select {
			case seenA <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"access token expired","code":"invalid_token"}}`)
		case "acc-b":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":20,"reset_after_seconds":3600}},"credits":{"has_credits":true,"unlimited":false}}`)
		default:
			t.Errorf("unexpected account id %q", r.Header.Get("ChatGPT-Account-ID"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer usageServer.Close()

	refreshHit := make(chan struct{}, 1)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case refreshHit <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Your refresh token has already been used to generate a new access token.","code":"refresh_token_reused"}}`)
	}))
	defer refreshServer.Close()

	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:     config.ProviderTypeResponses,
		APIURL:   usageServer.URL + "/backend-api/codex/responses",
		Preset:   config.ProviderPresetCodex,
		KeyOrder: config.KeyOrderSequential,
	}, config.ExtractAPIKeys(auth["openai"]))
	prov.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, &auth, &authMu, map[string]OAuthKeySetup{
		"access-a": {CredentialIndex: 0, AccountID: "acc-a", Expires: 32503680000000},
		"access-b": {CredentialIndex: 1, AccountID: "acc-b", Expires: 32503680000000},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		return nil, nil
	})
	defer prov.StopCodexRateLimitPolling()

	if !prov.StartCodexWarmup(t.Context()) {
		t.Fatal("expected codex warmup to start")
	}
	select {
	case <-seenA:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup did not probe expired account")
	}
	select {
	case <-refreshHit:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup did not attempt OAuth refresh after 401")
	}

	waitForOAuthStatusInAuthFile(t, authPath, "access-a", config.OAuthStatusExpired)
	updated, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig(updated): %v", err)
	}
	if got := updated["openai"][1].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected sibling OAuth credential to remain normal, got %#v", got)
	}
}

func waitForOAuthStatusInAuthFile(t *testing.T, authPath, access string, want config.OAuthCredentialStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastStatus config.OAuthCredentialStatus
	for time.Now().Before(deadline) {
		auth, err := config.LoadAuthConfig(authPath)
		if err != nil {
			t.Fatalf("LoadAuthConfig(%s): %v", authPath, err)
		}
		for _, cred := range auth["openai"] {
			if cred.OAuth != nil && cred.OAuth.Access == access {
				lastStatus = cred.OAuth.Status
				if lastStatus == want {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("OAuth credential %q status = %q, want %q", access, lastStatus, want)
}

func TestCurrentRateLimitSnapshotForRefPrefersPolledWhenInlineMissing(t *testing.T) {
	prov := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-token"})
	// Mark the single key as Codex OAuth so polled snapshots can be associated with it.
	authCfg := config.AuthConfig{
		"openai": {
			{OAuth: &config.OAuthCredential{Access: "oauth-token", Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-1"}},
		},
	}
	var authMu sync.Mutex
	prov.SetOAuthRefresher(
		config.OpenAIOAuthTokenURL,
		config.OpenAIOAuthClientID,
		"",
		&authCfg,
		&authMu,
		map[string]OAuthKeySetup{"oauth-token": {CredentialIndex: 0, AccountID: "acc-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		"",
	)
	if _, _, err := prov.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	polled := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 42},
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	prov.UpdatePolledRateLimitSnapshotForCredentialIndex(0, polled)
	c := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")
	if got := c.CurrentRateLimitSnapshotForRef("openai/gpt-5.5"); got != polled {
		t.Fatalf("CurrentRateLimitSnapshotForRef() = %#v, want polled %#v", got, polled)
	}
}

func TestCurrentRateLimitSnapshotForRefPrefersInlineOverPolled(t *testing.T) {
	prov := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"k1"})
	inline := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 88},
		Source:     ratelimit.SnapshotSourceInlineKey,
	}
	polled := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 42},
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	prov.UpdatePolledRateLimitSnapshotForCredentialIndex(0, polled)
	prov.UpdateKeySnapshot("k1", inline)
	if _, _, err := prov.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	c := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")
	if got := c.CurrentRateLimitSnapshotForRef("openai/gpt-5.5"); got != inline {
		t.Fatalf("CurrentRateLimitSnapshotForRef() = %#v, want inline %#v", got, inline)
	}
}

func TestCurrentRateLimitSnapshotForRefPrefersPolledWhenInlineStale(t *testing.T) {
	prov := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-token"})
	// Mark the single key as Codex OAuth so polled snapshots can be associated with it.
	authCfg := config.AuthConfig{
		"openai": {
			{OAuth: &config.OAuthCredential{Access: "oauth-token", Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-1"}},
		},
	}
	var authMu sync.Mutex
	prov.SetOAuthRefresher(
		config.OpenAIOAuthTokenURL,
		config.OpenAIOAuthClientID,
		"",
		&authCfg,
		&authMu,
		map[string]OAuthKeySetup{"oauth-token": {CredentialIndex: 0, AccountID: "acc-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		"",
	)
	if _, _, err := prov.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}

	inline := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now().Add(-2 * time.Minute),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 88},
		Source:     ratelimit.SnapshotSourceInlineKey,
	}
	polled := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 42},
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	prov.UpdatePolledRateLimitSnapshotForCredentialIndex(0, polled)
	prov.UpdateKeySnapshot("oauth-token", inline)

	c := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")
	if got := c.CurrentRateLimitSnapshotForRef("openai/gpt-5.5"); got != polled {
		t.Fatalf("CurrentRateLimitSnapshotForRef() = %#v, want polled %#v when inline is stale", got, polled)
	}
}

func TestCodexRateLimitPollingStartsOnlyAfterOAuthProviderSelection(t *testing.T) {
	prov := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-token"})
	authCfg := config.AuthConfig{
		"openai": {
			{OAuth: &config.OAuthCredential{Access: "oauth-token", Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		},
	}
	var authMu sync.Mutex
	prov.SetOAuthRefresher(
		config.OpenAIOAuthTokenURL,
		config.OpenAIOAuthClientID,
		"",
		&authCfg,
		&authMu,
		map[string]OAuthKeySetup{
			"oauth-token": {CredentialIndex: 0, AccountID: "acc-1", Expires: time.Now().Add(time.Hour).UnixMilli()},
		},
		"",
	)

	var pollHits atomic.Int32
	pollStarted := make(chan struct{}, 1)
	prov.StartCodexRateLimitPolling(func(key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		pollHits.Add(1)
		select {
		case pollStarted <- struct{}{}:
		default:
		}
		return nil, nil
	})
	defer prov.StopCodexRateLimitPolling()

	select {
	case <-pollStarted:
		t.Fatal("polling started before provider was selected")
	case <-time.After(50 * time.Millisecond):
	}
	if got := pollHits.Load(); got != 0 {
		t.Fatalf("poll hits before selection = %d, want 0", got)
	}

	if _, _, err := prov.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}

	select {
	case <-pollStarted:
	case <-time.After(time.Second):
		t.Fatal("polling did not start after provider selection")
	}
	if got := pollHits.Load(); got == 0 {
		t.Fatal("expected at least one poll hit after provider selection")
	}
}

func TestUpdatePolledRateLimitSnapshotCallsOnPolledUpdate(t *testing.T) {
	prov := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"k1"})

	var called atomic.Bool
	prov.SetOnPolledRateLimitUpdated(func() {
		called.Store(true)
	})

	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 42},
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	prov.UpdatePolledRateLimitSnapshotForCredentialIndex(0, snap)

	if !called.Load() {
		t.Fatal("onPolledUpdate callback was not called after UpdatePolledRateLimitSnapshotForCredentialIndex")
	}
}

func TestUpdatePolledRateLimitSnapshotForCredentialIndexNoCallbackForNilSnap(t *testing.T) {
	prov := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"k1"})

	var called atomic.Bool
	prov.SetOnPolledRateLimitUpdated(func() {
		called.Store(true)
	})

	prov.UpdatePolledRateLimitSnapshotForCredentialIndex(0, nil)
	if called.Load() {
		t.Fatal("onPolledUpdate should not be called for nil snapshot")
	}
}
