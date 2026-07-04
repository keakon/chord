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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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

func TestUpdateOAuthMetadataReappliesLoadedAuthState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	updatedAt := time.Now().Add(-time.Minute).UnixMilli()
	resetAt := time.Now().Add(30 * time.Minute).UnixMilli()
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
		record.Status = config.OAuthStatusNormal
		record.UpdatedAt = updatedAt
		record.CodexPrimaryUsedPct = 42
		record.CodexPrimaryWindowMin = 300
		record.CodexPrimaryResetAt = resetAt
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli()}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()
	p.mu.Lock()
	p.lastSelectedSlot = 0
	initialSnap := p.polledRateLimitByCredIdx[0]
	p.mu.Unlock()
	if initialSnap != nil {
		t.Fatalf("initial snapshot matched before metadata update: %#v", initialSnap)
	}

	p.UpdateOAuthMetadata(map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a"},
	})

	p.mu.Lock()
	info := p.keyStates[0].OAuthInfo
	snap := p.polledRateLimitByCredIdx[0]
	p.mu.Unlock()
	if info == nil || info.AccountUserID != "user-1__acc-1" || info.AccountID != "acc-1" || info.StateUpdatedAt != updatedAt {
		t.Fatalf("OAuthInfo after metadata/state update = %#v", info)
	}
	if snap == nil || snap.Primary == nil || snap.Primary.UsedPct != 42 || snap.Primary.WindowMinutes != 300 || snap.Primary.ResetsAt.UnixMilli() != resetAt {
		t.Fatalf("expected auth.state snapshot after metadata update, got %#v", snap)
	}
}

func TestUpdateOAuthMetadataReappliesRefreshFallbackAuthState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	refreshSHA := config.OAuthRefreshStateKey("refresh-a")
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", RefreshSHA256: refreshSHA}, func(record *config.OAuthStateRecord) (bool, error) {
		record.RefreshSHA256 = refreshSHA
		record.Status = config.OAuthStatusInvalidated
		record.UpdatedAt = time.Now().UnixMilli()
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli()}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	p.UpdateOAuthMetadata(map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", RefreshSHA256: refreshSHA},
	})

	p.mu.Lock()
	info := p.keyStates[0].OAuthInfo
	invalid := p.keyStates[0].Invalid
	p.mu.Unlock()
	if info == nil || info.AccountUserID != "user-1__acc-1" || info.RefreshSHA256 != refreshSHA || info.Status != config.OAuthStatusInvalidated || !invalid {
		t.Fatalf("OAuthInfo after refresh fallback state update = %#v invalid=%v", info, invalid)
	}
	if healthy, total := p.HealthyKeyCount(); healthy != 0 || total != 0 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 0/0 from refresh fallback auth.state", healthy, total)
	}
}

func TestUpdateOAuthMetadataKeepsDuplicateAccessTokenSlotsDistinct(t *testing.T) {
	access := "shared-access"
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access, access})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", nil, nil, map[string]OAuthKeySetup{
		OAuthKeySetupSlotKey(0, access): {HasKeySlot: true, KeySlot: 0, CredentialIndex: 0, Access: access, Expires: time.Now().Add(time.Hour).UnixMilli()},
		OAuthKeySetupSlotKey(1, access): {HasKeySlot: true, KeySlot: 1, CredentialIndex: 1, Access: access, Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	p.UpdateOAuthMetadata(map[string]OAuthKeySetup{
		OAuthKeySetupSlotKey(0, access): {HasKeySlot: true, KeySlot: 0, CredentialIndex: 0, AccountUserID: "user-a__acct-a", AccountID: "acct-a", Access: access},
		OAuthKeySetupSlotKey(1, access): {HasKeySlot: true, KeySlot: 1, CredentialIndex: 1, AccountUserID: "user-b__acct-b", AccountID: "acct-b", Access: access},
	})

	p.mu.Lock()
	info0 := p.keyStates[0].OAuthInfo
	info1 := p.keyStates[1].OAuthInfo
	p.mu.Unlock()
	if info0 == nil || info0.CredentialIndex != 0 || info0.AccountID != "acct-a" || info0.AccountUserID != "user-a__acct-a" {
		t.Fatalf("slot 0 OAuthInfo = %#v, want first credential metadata", info0)
	}
	if info1 == nil || info1.CredentialIndex != 1 || info1.AccountID != "acct-b" || info1.AccountUserID != "user-b__acct-b" {
		t.Fatalf("slot 1 OAuthInfo = %#v, want second credential metadata", info1)
	}
}

func TestKeyStatsReloadAuthState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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

	if healthy, total := p.HealthyKeyCount(); healthy != 0 || total != 0 {
		t.Fatalf("HealthyKeyCount after auth.state change = %d/%d, want 0/0", healthy, total)
	}
}

func TestKeyStatsApplyRefreshFallbackAuthState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	refreshSHA := config.OAuthRefreshStateKey("refresh-token")
	if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", RefreshSHA256: refreshSHA}, func(record *config.OAuthStateRecord) (bool, error) {
		record.RefreshSHA256 = refreshSHA
		record.AccountID = "acc-1"
		record.Email = "user@example.com"
		record.Status = config.OAuthStatusInvalidated
		return true, nil
	}); err != nil {
		t.Fatalf("UpsertOAuthStateRecord: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountID: "acc-1", Email: "user@example.com", RefreshSHA256: refreshSHA, Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	if healthy, total := p.HealthyKeyCount(); healthy != 0 || total != 0 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 0/0 from refresh auth.state", healthy, total)
	}
}

func TestKeyStatsIgnoreAccountIDOnlyAuthState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	if err := os.WriteFile(statePath, []byte(`{"openai":{"account_id:shared":{"account_id":"shared","status":"invalidated"}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountID: "shared", Email: "user@example.com", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	if healthy, total := p.HealthyKeyCount(); healthy != 1 || total != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/1: account_id-only auth.state must not invalidate keys", healthy, total)
	}
}

func TestAuthStateMonitorReloadEmitsPolledUpdate(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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

func TestAuthStateMonitorReloadDetectsSameStatContentChange(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a"})
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{Access: "key-a", Refresh: "refresh-a", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountUserID: "user-1__acc-1", AccountID: "acc-1"}}}}
	var authMu sync.Mutex
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", statePath, &auth, &authMu, map[string]OAuthKeySetup{
		"key-a": {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Access: "key-a", Expires: time.Now().Add(time.Hour).UnixMilli()},
	}, "")
	defer p.Close()

	updated := make(chan struct{}, 2)
	p.mu.Lock()
	p.lastSelectedSlot = 0
	p.mu.Unlock()
	p.SetOnPolledRateLimitUpdated(func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	})

	upsertUsedPct := func(usedPct int) {
		t.Helper()
		if _, _, _, err := config.UpsertOAuthStateRecord(statePath, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1", AccountID: "acc-1"}, func(record *config.OAuthStateRecord) (bool, error) {
			record.Status = config.OAuthStatusNormal
			record.UpdatedAt = 1000
			record.CodexPrimaryUsedPct = float64(usedPct)
			record.CodexPrimaryWindowMin = 300
			return true, nil
		}); err != nil {
			t.Fatalf("UpsertOAuthStateRecord(%d): %v", usedPct, err)
		}
	}

	upsertUsedPct(55)
	p.reloadAuthStateFromMonitor()
	select {
	case <-updated:
	default:
		t.Fatal("expected initial polled update callback after auth.state reload")
	}
	p.mu.Lock()
	cachedMTime := p.authStateMTime
	cachedSize := p.authStateSize
	p.mu.Unlock()

	upsertUsedPct(56)
	if err := os.Chtimes(statePath, cachedMTime, cachedMTime); err != nil {
		t.Fatalf("Chtimes(auth.state): %v", err)
	}
	if info, err := os.Stat(statePath); err != nil {
		t.Fatalf("Stat(auth.state): %v", err)
	} else if info.Size() != cachedSize || !info.ModTime().Equal(cachedMTime) {
		t.Fatalf("auth.state stat = (%v, %v), want (%v, %v)", info.ModTime(), info.Size(), cachedMTime, cachedSize)
	}

	p.reloadAuthStateFromMonitor()
	select {
	case <-updated:
	default:
		t.Fatal("expected polled update callback after same-stat auth.state content change")
	}
	if got := p.CurrentPolledRateLimitSnapshot(); got == nil || got.Primary == nil || got.Primary.UsedPct != 56 {
		t.Fatalf("expected same-stat reloaded snapshot used pct 56, got %#v", got)
	}
}

func TestAuthStateMonitorReloadIgnoresNonCurrentSnapshot(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
	statePath := filepath.Join(t.TempDir(), "auth.state.json")
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
