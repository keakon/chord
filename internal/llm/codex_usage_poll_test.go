package llm

import (
	"context"
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
