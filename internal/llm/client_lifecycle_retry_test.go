package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestNewClientCodexWarmupProbesMultipleAccounts(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "" {
			select {
			case seen <- got:
			default:
			}
		}
		if got := r.URL.Path; got != "/backend-api/wham/usage" {
			t.Errorf("request path = %q, want /backend-api/wham/usage", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":20,"reset_after_seconds":3600},"secondary_window":{"used_percent":40,"reset_after_seconds":7200}},"credits":{"has_credits":true,"unlimited":false}}`)
	}))
	defer server.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-a","exp":4102444800}`), Refresh: "refresh-a", Expires: expires, AccountID: "acc-a"}},
		// Insert an API key between OAuth slots so slot index != OAuth credential index.
		{APIKey: "api-key-1"},
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-b","exp":4102444800}`), Refresh: "refresh-b", Expires: expires, AccountID: "acc-b"}},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/backend-api/codex/responses",
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Limit: config.ModelLimit{Context: 128000, Output: 1024}},
		},
	}, config.ExtractAPIKeys(creds))
	prov.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		creds[0].OAuth.Access: {CredentialIndex: 0, AccountID: "acc-a", Expires: expires},
		creds[2].OAuth.Access: {CredentialIndex: 2, AccountID: "acc-b", Expires: expires},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		return nil, nil
	})

	warmupClient := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")
	if warmupClient.codexWarmupStarted {
		t.Fatal("constructor started unbound warmup")
	}
	var warmupGate executiongate.Gate
	warmupGate.Freeze()
	warmupClient.SetExecutionGate(&warmupGate, "test")
	warmupClient.startCodexWarmup()
	if warmupClient.codexWarmupStarted {
		t.Fatal("paused binding started warmup")
	}
	if err := warmupGate.Resume(); err != nil {
		t.Fatal(err)
	}
	warmupClient.SetExecutionGate(&warmupGate, "test")
	warmupClient.startCodexWarmup()
	defer warmupClient.Close()

	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case accountID := <-seen:
			got[accountID] = true
		case <-deadline:
			t.Fatalf("warmup probes = %#v, want both accounts", got)
		}
	}
	for _, want := range []string{"acc-a", "acc-b"} {
		if !got[want] {
			t.Fatalf("warmup probes = %#v, want account %s", got, want)
		}
	}
}

func TestNewClientCodexWarmupCancelsOnInvalidateRouting(t *testing.T) {
	started := make(chan string, 1)
	canceled := make(chan struct{})
	var canceledOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "" {
			select {
			case started <- got:
			default:
			}
		}
		<-r.Context().Done()
		canceledOnce.Do(func() {
			close(canceled)
		})
	}))
	defer server.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-a","exp":4102444800}`), Refresh: "refresh-a", Expires: expires, AccountID: "acc-a"}},
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-b","exp":4102444800}`), Refresh: "refresh-b", Expires: expires, AccountID: "acc-b"}},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/backend-api/codex/responses",
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Limit: config.ModelLimit{Context: 128000, Output: 1024}},
		},
	}, config.ExtractAPIKeys(creds))
	prov.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		creds[0].OAuth.Access: {CredentialIndex: 0, AccountID: "acc-a", Expires: expires},
		creds[1].OAuth.Access: {CredentialIndex: 1, AccountID: "acc-b", Expires: expires},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		return nil, nil
	})

	c := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")
	c.SetExecutionGate(&executiongate.Gate{}, "test")
	c.startCodexWarmup()
	defer c.Close()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request did not start")
	}

	c.InvalidateRouting("model_client_swapped")

	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request was not cancelled after InvalidateRouting")
	}
}

func TestClientCloseCancelsWarmupAndRejectsNewRequests(t *testing.T) {
	started := make(chan string, 1)
	canceled := make(chan struct{})
	var canceledOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- "started":
		default:
		}
		<-r.Context().Done()
		canceledOnce.Do(func() { close(canceled) })
	}))
	defer server.Close()

	expires := time.Now().Add(time.Hour).UnixMilli()
	creds := []config.ProviderCredential{
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-a","exp":4102444800}`), Refresh: "refresh-a", Expires: expires, AccountID: "acc-a"}},
		{OAuth: &config.OAuthCredential{Access: testProviderOAuthJWT(`{"chatgpt_account_id":"acc-b","exp":4102444800}`), Refresh: "refresh-b", Expires: expires, AccountID: "acc-b"}},
	}
	auth := config.AuthConfig{"openai": creds}
	var authMu sync.Mutex
	prov := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/backend-api/codex/responses",
		Preset: config.ProviderPresetCodex,
		Models: map[string]config.ModelConfig{"gpt-5.5": {Limit: config.ModelLimit{Context: 128000, Output: 1024}}},
	}, config.ExtractAPIKeys(creds))
	prov.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		creds[0].OAuth.Access: {CredentialIndex: 0, AccountID: "acc-a", Expires: expires},
		creds[1].OAuth.Access: {CredentialIndex: 1, AccountID: "acc-b", Expires: expires},
	}, "")
	prov.StartCodexRateLimitPolling(func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error) { return nil, nil })
	c := NewClient(prov, noopProvider{}, "gpt-5.5", 1024, "")
	c.SetExecutionGate(&executiongate.Gate{}, "test")
	c.startCodexWarmup()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request did not start")
	}
	c.Close()
	c.Close()
	if !c.IsClosed() {
		t.Fatal("Close must permanently invalidate the client")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup request was not cancelled by Close")
	}
	if _, err := c.CompleteStream(context.Background(), nil, nil, nil); err == nil || err.Error() != "llm client is closed" {
		t.Fatalf("CompleteStream after Close error = %v, want closed error", err)
	}
}

func TestCompleteStreamWithRetryStopsOnRoutingInvalidationDuringBackoff(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{err: &APIError{StatusCode: 500, Message: "retry later"}}}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	resultCh := make(chan error, 1)
	go func() {
		_, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, nil)
		resultCh <- err
	}()

	// Observe the first provider attempt instead of guessing a fixed window:
	// under load the async goroutine may not be scheduled within 100ms, and
	// invalidating before the retry backoff starts would not exercise the
	// abort path.
	deadline := time.Now().Add(2 * time.Second)
	for impl.CallCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if impl.CallCount() < 1 {
		t.Fatal("timed out waiting for CompleteStream to start the first attempt")
	}
	c.InvalidateRouting("model_pool_changed")

	select {
	case err := <-resultCh:
		if !IsRoutingInvalidated(err) {
			t.Fatalf("CompleteStream err = %v, want routing invalidated", err)
		}
		if got := impl.CallCount(); got != 1 {
			t.Fatalf("provider calls = %d, want 1 after invalidation aborts retry chain", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CompleteStream to stop after routing invalidation")
	}
}

func TestCompleteStreamReturnsSuccessWhenRoutingChangesDuringSuccessfulAttempt(t *testing.T) {
	cfg := testProviderConfigWithKeys("primary-prov", "primary-model", []string{"k1"})
	impl := &recordingProvider{}
	impl.calls = []scriptedCall{{
		streams: []message.StreamDelta{{Type: "text", Text: "visible"}},
		resp:    &message.Response{Content: "ok"},
	}}
	c := NewClient(cfg, impl, "primary-model", 4096, "sys")

	resp, err := c.CompleteStream(context.Background(), []message.Message{{Role: "user", Content: "hi"}}, nil, func(delta message.StreamDelta) {
		if delta.Type == "text" {
			c.InvalidateRouting("model_pool_changed")
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
