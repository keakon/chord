package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestCurrentRateLimitSnapshotPrefersPolledSnapshotAfterInlineClear(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"openai": {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
	}}
	a.SetProviderModelRef("openai/gpt-5.5")

	polled := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 33},
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	inline := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 77},
		Source:     ratelimit.SnapshotSourceInlineKey,
	}

	prov := llm.NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-token"})
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
		map[string]llm.OAuthKeySetup{"oauth-token": {CredentialIndex: 0, AccountID: "acc-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		"",
	)
	if _, _, err := prov.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	prov.UpdatePolledRateLimitSnapshotForCredentialIndex(0, polled)
	client := llm.NewClient(prov, stubProvider{}, "gpt-5.5", 1024, "")
	a.SwapLLMClient(client, "gpt-5.5", 128000)

	a.updateRateLimitSnapshot(inline)
	if got := a.CurrentRateLimitSnapshot(); got != inline {
		t.Fatalf("CurrentRateLimitSnapshot() = %#v, want inline %#v", got, inline)
	}
	a.clearCurrentRateLimitSnapshot()
	if got := a.CurrentRateLimitSnapshot(); got != polled {
		t.Fatalf("after clear CurrentRateLimitSnapshot() = %#v, want polled %#v", got, polled)
	}
}

func TestPolledRateLimitUpdateEmitsEventViaCallback(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig = &config.Config{Providers: map[string]config.ProviderConfig{
		"openai": {Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex},
	}}

	provCfg := llm.NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-token"})
	// Configure a Codex OAuth slot so the callback path can attach polled snapshots.
	authCfg := config.AuthConfig{
		"openai": {
			{OAuth: &config.OAuthCredential{Access: "oauth-token", Refresh: "refresh-token", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-1"}},
		},
	}
	var authMu sync.Mutex
	provCfg.SetOAuthRefresher(
		config.OpenAIOAuthTokenURL,
		config.OpenAIOAuthClientID,
		"",
		&authCfg,
		&authMu,
		map[string]llm.OAuthKeySetup{"oauth-token": {CredentialIndex: 0, AccountID: "acc-1", Expires: time.Now().Add(time.Hour).UnixMilli()}},
		"",
	)
	if _, _, err := provCfg.SelectKeyWithContext(t.Context()); err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	client := llm.NewClient(provCfg, stubProvider{}, "gpt-5.5", 1024, "")
	a.SwapLLMClient(client, "gpt-5.5", 128000)

	// The callback should now be wired. Update the polled snapshot on the
	// provider and verify that a RateLimitUpdatedEvent arrives on outputCh.
	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   "openai",
		CapturedAt: time.Now(),
		Primary:    &ratelimit.RateLimitWindow{UsedPct: 55},
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	provCfg.UpdatePolledRateLimitSnapshotForCredentialIndex(0, snap)

	select {
	case evt := <-a.outputCh:
		if _, ok := evt.(RateLimitUpdatedEvent); !ok {
			t.Fatalf("expected RateLimitUpdatedEvent, got %T", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for RateLimitUpdatedEvent after polled snapshot update")
	}
}
