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
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
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
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}, {OAuth: &config.OAuthCredential{Access: "key-b", Refresh: "refresh-b", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-2__acc-2", AccountID: "acc-2"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
		"key-b": {CredentialIndex: 1, AccountUserID: "user-2__acc-2", AccountID: "acc-2", Access: "key-b", Expires: time.Now().Add(time.Hour).UnixMilli()},
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

func TestMaybeReloadAuthStateSkipsUnchangedFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = time.Now().UnixMilli()
		record.CodexPrimaryUsedPct = 25
		record.CodexPrimaryWindowMin = 300
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	p.mu.Lock()
	if got := p.polledRateLimitByCredIdx[0]; got == nil || got.Primary == nil || got.Primary.UsedPct != 25 {
		p.mu.Unlock()
		t.Fatalf("expected initial auth.state snapshot, got %#v", got)
	}
	p.polledRateLimitByCredIdx[0] = &ratelimit.KeyRateLimitSnapshot{Primary: &ratelimit.RateLimitWindow{UsedPct: 99}}
	changed := p.maybeReloadAuthStateLocked()
	got := p.polledRateLimitByCredIdx[0]
	p.mu.Unlock()

	if changed {
		t.Fatal("maybeReloadAuthStateLocked changed unchanged file")
	}
	if got == nil || got.Primary == nil || got.Primary.UsedPct != 99 {
		t.Fatalf("unchanged file was reapplied from YAML, got %#v", got)
	}
}

func TestKeyStatsDoNotReloadAuthState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = time.Now().UnixMilli()
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()
	p.mu.Lock()
	p.stopAuthStateMonitorLocked()
	p.mu.Unlock()

	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusDeactivated
		record.UpdatedAt = time.Now().UnixMilli()
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord deactivate: %v", err)
	}

	if healthy, total := p.HealthyKeyCount(); healthy != 1 || total != 1 {
		t.Fatalf("HealthyKeyCount reloaded auth.state: healthy=%d total=%d, want 1/1", healthy, total)
	}

	p.mu.Lock()
	changed := p.maybeReloadAuthStateLocked()
	p.mu.Unlock()
	if !changed {
		t.Fatal("explicit auth.state reload did not observe changed file")
	}
	if healthy, total := p.HealthyKeyCount(); healthy != 0 || total != 0 {
		t.Fatalf("HealthyKeyCount after explicit reload = %d/%d, want 0/0", healthy, total)
	}
}

func TestAuthStateMonitorReloadEmitsPolledUpdate(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	updated := make(chan struct{}, 1)
	p.mu.Lock()
	p.lastSelectedSlot = 0
	p.mu.Unlock()
	p.SetOnPolledRateLimitUpdated(func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	})

	captured := time.Now()
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = captured.UnixMilli()
		record.CodexPrimaryUsedPct = 25
		record.CodexPrimaryWindowMin = 300
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p.reloadAuthStateFromMonitor()

	select {
	case <-updated:
	default:
		t.Fatal("expected polled update callback after auth.state reload")
	}
	p.mu.Lock()
	snap := p.polledRateLimitByCredIdx[0]
	p.mu.Unlock()
	if snap == nil || snap.Primary == nil || snap.Primary.UsedPct != 25 {
		t.Fatalf("expected reloaded polled snapshot, got %#v", snap)
	}
}

func TestAuthStateMonitorReloadClearsSnapshotWhenStateFileRemoved(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	updated := make(chan struct{}, 1)
	p.mu.Lock()
	p.lastSelectedSlot = 0
	p.mu.Unlock()
	p.SetOnPolledRateLimitUpdated(func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	})

	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = time.Now().UnixMilli()
		record.CodexPrimaryUsedPct = 55
		record.CodexPrimaryWindowMin = 300
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}
	p.reloadAuthStateFromMonitor()
	select {
	case <-updated:
	default:
		t.Fatal("expected initial polled update callback after auth.state reload")
	}
	if got := p.CurrentPolledRateLimitSnapshot(); got == nil || got.Primary == nil || got.Primary.UsedPct != 55 {
		t.Fatalf("expected initial auth.state snapshot, got %#v", got)
	}

	if err := os.Remove(statePath); err != nil {
		t.Fatalf("Remove(auth.state): %v", err)
	}
	p.reloadAuthStateFromMonitor()

	select {
	case <-updated:
	default:
		t.Fatal("expected polled update callback after auth.state removal")
	}
	if got := p.CurrentPolledRateLimitSnapshot(); got != nil {
		t.Fatalf("current polled snapshot after auth.state removal = %#v, want nil", got)
	}
}

func TestAuthStateMonitorReloadIgnoresNonCurrentSnapshot(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	auth := config.AuthConfig{"openai": {
		{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}},
		{OAuth: &config.OAuthCredential{Access: "key-b", Refresh: "refresh-b", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-2__acc-2", AccountID: "acc-2"}},
	}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
		"key-b": {CredentialIndex: 1, AccountUserID: "user-2__acc-2", AccountID: "acc-2", Access: "key-b", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	updated := make(chan struct{}, 1)
	p.mu.Lock()
	p.lastSelectedSlot = 0
	p.mu.Unlock()
	p.SetOnPolledRateLimitUpdated(func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	})

	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-2__acc-2", AccountID: "acc-2"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = time.Now().UnixMilli()
		record.CodexPrimaryUsedPct = 90
		record.CodexPrimaryWindowMin = 300
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p.reloadAuthStateFromMonitor()

	select {
	case <-updated:
		t.Fatal("did not expect polled update callback for non-current auth.state change")
	default:
	}
	p.mu.Lock()
	current := p.polledRateLimitByCredIdx[0]
	nonCurrent := p.polledRateLimitByCredIdx[1]
	p.mu.Unlock()
	if current != nil {
		t.Fatalf("current snapshot = %#v, want nil", current)
	}
	if nonCurrent == nil || nonCurrent.Primary == nil || nonCurrent.Primary.UsedPct != 90 {
		t.Fatalf("expected non-current snapshot to be cached, got %#v", nonCurrent)
	}
}

func TestProviderCloseStopsAuthStateMonitor(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")

	p.mu.Lock()
	started := p.authStateMonitor != nil
	p.mu.Unlock()
	if !started {
		t.Fatal("expected auth state monitor to start")
	}

	p.Close()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.authStateMonitor != nil {
		t.Fatal("expected Close to clear auth state monitor")
	}
	if p.codexPollFetchFn != nil {
		t.Fatal("expected Close to clear Codex polling fetch function")
	}
}

func TestWakeCodexRateLimitPollingSkipsFetchAfterAuthStateReload(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = time.Now().UnixMilli()
		record.CodexPrimaryUsedPct = 40
		record.CodexPrimaryWindowMin = 300
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	fetchCalled := make(chan struct{}, 1)
	p.StartCodexRateLimitPolling(func(key, accountID string) ([]*ratelimit.KeyRateLimitSnapshot, error) {
		select {
		case fetchCalled <- struct{}{}:
		default:
		}
		return nil, nil
	})
	p.mu.Lock()
	p.lastSelectedSlot = 0
	p.mu.Unlock()

	p.WakeCodexRateLimitPolling()

	select {
	case <-fetchCalled:
		t.Fatal("did not expect external /wham/usage fetch after fresh auth.state reload")
	case <-time.After(100 * time.Millisecond):
	}
	p.mu.Lock()
	snap := p.polledRateLimitByCredIdx[0]
	p.mu.Unlock()
	if snap == nil || snap.Primary == nil || snap.Primary.UsedPct != 40 {
		t.Fatalf("expected auth.state snapshot to be used, got %#v", snap)
	}
}

func TestPersistAuthStateForKeyPreservesDeactivatedStatus(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1", Status: config.OAuthStatusDeactivated}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli(), Status: config.OAuthStatusDeactivated},
	}, "")

	p.mu.Lock()
	p.keyStates[0].Invalid = true
	p.mu.Unlock()

	snap := &ratelimit.KeyRateLimitSnapshot{Primary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: time.Now().Add(time.Hour)}}
	p.persistAuthStateForKey("key-a", snap, time.Time{})

	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	record, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"})
	if !ok {
		t.Fatal("expected auth.state record for key-a")
	}
	if record.Status != config.OAuthStatusDeactivated {
		t.Fatalf("expected deactivated status preserved, got %q", record.Status)
	}
}

func TestPersistAuthStateForKeyPreservesRuntimeDeactivatedStatus(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.yaml")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli(), Status: config.OAuthStatusNormal},
	}, "")

	p.MarkDeactivated("key-a")
	snap := &ratelimit.KeyRateLimitSnapshot{Primary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: time.Now().Add(time.Hour)}}
	p.persistAuthStateForKey("key-a", snap, time.Time{})

	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	record, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"})
	if !ok {
		t.Fatal("expected auth.state record for key-a")
	}
	if record.Status != config.OAuthStatusDeactivated {
		t.Fatalf("expected runtime deactivated status preserved, got %q", record.Status)
	}
}
