package llm

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestSelectKeyUsesAuthStateSnapshotCache(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountID: "acc-1", Access: "key-a"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.CodexPrimaryUsedPct = 10
		record.CodexPrimaryWindowMin = 60
		record.CodexPrimaryResetAt = time.Now().Add(90 * time.Minute).UnixMilli()
		record.CodexSecondaryUsedPct = 80
		record.CodexSecondaryWindowMin = 60
		record.CodexSecondaryResetAt = time.Now().Add(30 * time.Minute).UnixMilli()
		record.UpdatedAt = time.Now().Add(-time.Hour).UnixMilli()
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex, KeyOrder: config.KeyOrderSmart}, []string{"key-a", "key-b"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-1"}}, {OAuth: &config.OAuthCredential{Access: "key-b", Refresh: "refresh-b", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-2"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
		"key-b": {CredentialIndex: 1, AccountID: "acc-2", Access: "key-b", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("selected key = %q, want key-a from restored state", key)
	}
	if got := p.CurrentPolledRateLimitSnapshot(); got == nil || got.Source != ratelimit.SnapshotSourcePolledUsage {
		t.Fatalf("expected current polled snapshot from auth.state, got %#v", got)
	}

	if err := os.WriteFile(statePath, []byte("openai:\n  acc-2: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(state): %v", err)
	}
	p.mu.Lock()
	p.maybeReloadAuthStateLocked()
	p.mu.Unlock()
}
