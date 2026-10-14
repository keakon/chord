package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

// --- SelectKey / SelectKeyWithContext ---

func TestSelectKey_NoKeys(t *testing.T) {
	p := newTestProviderConfig(nil)
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error for no keys: %v", err)
	}
	if key != "" {
		t.Fatalf("expected empty key for no-keys provider, got %q", key)
	}
}

func TestSelectKeyRefreshOnlyUnrecoverableFailurePersistsRefreshState(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.yaml")
	statePath := filepath.Join(dir, "auth.state.json")
	if err := os.WriteFile(authPath, []byte(`openai:
  - refresh: refresh-only
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var authMu sync.Mutex
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_grant","message":"refresh token expired"}}`)
	}))
	defer refreshServer.Close()

	refreshStateKey := config.OAuthRefreshStateKey("refresh-only")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{""})
	defer p.Close()
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, statePath, &auth, &authMu, map[string]OAuthKeySetup{
		refreshStateKey: {CredentialIndex: 0, RefreshSHA256: refreshStateKey, Status: config.OAuthStatusNormal},
	}, "")

	_, _, err = p.SelectKeyWithContext(context.Background())
	if err == nil {
		t.Fatal("expected unrecoverable refresh failure")
	}

	if got := auth["openai"][0].OAuth; got == nil || got.Status != config.OAuthStatusExpired {
		t.Fatalf("expected in-memory refresh-only OAuth credential status=expired, got %#v", got)
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	record, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", RefreshSHA256: refreshStateKey})
	if !ok {
		t.Fatalf("expected refresh-only failure state under %q, got %#v", refreshStateKey, state)
	}
	if record.Status != config.OAuthStatusExpired || record.RefreshSHA256 != refreshStateKey {
		t.Fatalf("refresh-only failure state = %#v, want expired refresh_sha256 state", record)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(auth): %v", err)
	}
	if strings.Contains(string(data), "status:") {
		t.Fatalf("auth.yaml should not persist OAuth status, got:\n%s", data)
	}
}

func TestSelectKeyRefreshOnlyBackfillsAccountUserIDAfterRefresh(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.yaml")
	statePath := filepath.Join(dir, "auth.state.json")
	if err := os.WriteFile(authPath, []byte(`openai:
  - refresh: refresh-only
`), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	newAccess := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1","email":"user@example.com","exp":4102444800}`)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+newAccess+`","refresh_token":"refresh-new","expires_in":3600}`)
	}))
	defer refreshServer.Close()

	var authMu sync.Mutex
	refreshStateKey := config.OAuthRefreshStateKey("refresh-only")
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{""})
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, statePath, &auth, &authMu, map[string]OAuthKeySetup{
		refreshStateKey: {CredentialIndex: 0, RefreshSHA256: refreshStateKey, Status: config.OAuthStatusNormal},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != newAccess {
		t.Fatalf("selected key = %q, want refreshed access", key)
	}
	if got := auth["openai"][0].OAuth.AccountUserID; got != "user-1__acc-1" {
		t.Fatalf("in-memory account_user_id = %q, want refreshed account user", got)
	}
	loaded, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	if got := loaded["openai"][0].OAuth.AccountUserID; got != "user-1__acc-1" {
		t.Fatalf("persisted account_user_id = %q, want refreshed account user", got)
	}
	state, err := config.LoadAuthState(statePath)
	if err != nil {
		t.Fatalf("LoadAuthState: %v", err)
	}
	if _, ok := config.FindOAuthStateRecord(state, config.OAuthStateKey{Provider: "openai", AccountUserID: "user-1__acc-1"}); !ok {
		t.Fatalf("refreshed auth.state missing account_user_id record: %#v", state)
	}
}

func TestSelectKeyRefreshFailureDoesNotMarkExpiredFromLocalExpires(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expires := time.Now().Add(-time.Minute).UnixMilli()
	access := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  - refresh: old-refresh
    access: %s
    expires: %d
    account_id: acc-1
`, access, expires), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access})
	refreshHit := false
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHit = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer refreshServer.Close()
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, "", &auth, &authMu, map[string]OAuthKeySetup{
		access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != access {
		t.Fatalf("selected key = %q, want original access", key)
	}
	if refreshHit {
		t.Fatal("refresh endpoint should not be called before trying existing access token")
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected in-memory auth OAuth credential status=normal, got %#v", got)
	}
}

func TestSelectKeyMissingRefreshTokenStillUsesExistingAccessToken(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.yaml")
	expires := time.Now().Add(-time.Minute).UnixMilli()
	access := testProviderOAuthJWT(`{"chatgpt_account_id":"acc-1","chatgpt_user_id":"user-1"}`)
	if err := os.WriteFile(authPath, fmt.Appendf(nil, `openai:
  - access: %s
    expires: %d
    account_id: acc-1
`, access, expires), 0o600); err != nil {
		t.Fatalf("WriteFile(auth): %v", err)
	}
	auth, err := config.LoadAuthConfig(authPath)
	if err != nil {
		t.Fatalf("LoadAuthConfig: %v", err)
	}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{access})
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("refresh endpoint should not be called before trying existing access token")
	}))
	defer refreshServer.Close()
	p.SetOAuthRefresher(refreshServer.URL, "client-id", authPath, "", &auth, &authMu, map[string]OAuthKeySetup{
		access: {CredentialIndex: 0, AccountUserID: "user-1__acc-1", AccountID: "acc-1", Expires: expires},
	}, "")

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != access {
		t.Fatalf("selected key = %q, want existing access token", key)
	}
	if got := auth["openai"][0].OAuth; got == nil || got.Status != config.OAuthStatusNormal {
		t.Fatalf("expected in-memory auth OAuth credential status=normal, got %#v", got)
	}
}

func TestSelectKey_SingleKey(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
	// Calling again should still return the same key (only one available).
	key, _, err = p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
}

func TestSelectKey_MultipleKeys_Sticky(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b", "key-c"})

	// Default strategy is sticky: always returns the same key until it cools down.
	for i := range 4 {
		key, _, err := p.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if key != "key-a" {
			t.Fatalf("call %d: expected key-a (sticky), got %s", i, key)
		}
	}
}

func TestNewProviderConfig_CodexDefaultsToSmartKeyOrder(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"k1", "k2"})
	if p.keyOrder != config.KeyOrderSmart {
		t.Fatalf("keyOrder = %q, want %q", p.keyOrder, config.KeyOrderSmart)
	}
}

func TestNewProviderConfig_NormalizesUnknownKeySelectionDefensively(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, KeyRotation: "per-call", KeyOrder: "round_robin"}, []string{"k1", "k2"})
	if p.keyRotation != config.KeyRotationOnFailure {
		t.Fatalf("keyRotation = %q, want %q", p.keyRotation, config.KeyRotationOnFailure)
	}
	if p.keyOrder != config.KeyOrderSequential {
		t.Fatalf("keyOrder = %q, want %q", p.keyOrder, config.KeyOrderSequential)
	}
}

func TestSelectKey_CodexSmartPrefersSoonResetUsableHeadroom(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, ResetsAt: now.Add(6 * time.Hour)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 90, ResetsAt: now.Add(10 * time.Minute)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b with soon-reset usable headroom", key)
	}
}

func TestSelectKey_CodexSmartDoesNotDemoteNinetyNinePercentUsed(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 99, WindowMinutes: 300, ResetsAt: now.Add(10 * time.Minute)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("selected key = %q, want key-a because 99%% used still has usable quota and resets sooner", key)
	}
}

func TestSelectKey_CodexSmartDemotesOneHundredPercentUsed(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 100, WindowMinutes: 300, ResetsAt: now.Add(10 * time.Minute)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b because 100%% used key-a is tried last", key)
	}
}

func TestSelectKey_CodexSmartComparesPrimaryBeforeSecondaryWindow(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	now := time.Now()
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 10, WindowMinutes: 300, ResetsAt: now.Add(6 * time.Hour)},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 10, WindowMinutes: 10080, ResetsAt: now.Add(10 * time.Minute)},
	})
	p.UpdateKeySnapshot("key-b", &ratelimit.KeyRateLimitSnapshot{
		Primary:   &ratelimit.RateLimitWindow{UsedPct: 90, WindowMinutes: 300, ResetsAt: now.Add(10 * time.Minute)},
		Secondary: &ratelimit.RateLimitWindow{UsedPct: 90, WindowMinutes: 10080, ResetsAt: now.Add(6 * time.Hour)},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b because the primary 5h window resets sooner", key)
	}
}

func TestSelectKey_UpdatesInlineDisplayToSelectedKeySnapshot(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	snapA := &ratelimit.KeyRateLimitSnapshot{
		Provider: "openai",
		Primary:  &ratelimit.RateLimitWindow{UsedPct: 20},
		Source:   ratelimit.SnapshotSourceInlineKey,
	}
	p.UpdateKeySnapshot("key-a", snapA)

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("selected key = %q, want key-a", key)
	}
	if got := p.CurrentInlineRateLimitSnapshot(); got != snapA {
		t.Fatalf("CurrentInlineRateLimitSnapshot() = %#v, want key-a snapshot %#v", got, snapA)
	}

	p.MarkCooldown("key-a", time.Minute)
	key, _, err = p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext after cooldown: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b", key)
	}
	if got := p.CurrentInlineRateLimitSnapshot(); got != nil {
		t.Fatalf("CurrentInlineRateLimitSnapshot() = %#v, want nil for selected key without snapshot", got)
	}

	snapB := &ratelimit.KeyRateLimitSnapshot{
		Provider: "openai",
		Primary:  &ratelimit.RateLimitWindow{UsedPct: 40},
		Source:   ratelimit.SnapshotSourceInlineKey,
	}
	p.UpdateKeySnapshot("key-b", snapB)
	if got := p.CurrentInlineRateLimitSnapshot(); got != snapB {
		t.Fatalf("CurrentInlineRateLimitSnapshot() = %#v, want key-b snapshot %#v", got, snapB)
	}
}

func TestSelectKey_CodexSmartPrefersNonSoftCooledKey(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-a", CodexPrimaryResetAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	p.keyStates[0].SoftCooldownUntil = time.Now().Add(2 * time.Hour)
	p.keyStates[1].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-b"}
	p.mu.Unlock()
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("expected non-soft-cooled key-b, got %s", key)
	}
}

func TestSelectKey_Sticky_keepsPinnedKeyDespiteCodex100PercentSnapshot(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	reset := time.Now().Add(time.Hour)
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: reset},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a (sticky; header quota is informational only), got %s", key)
	}
}

// --- Context cancellation ---

func TestSelectKeyWithContext_Cancelled(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	// Set rate limiter with very low rate to force waiting.
	p.SetRateLimiter(1) // 1 rpm = very slow

	// Use the one allowed burst token.
	_, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("first call should succeed: %v", err)
	}

	// Now cancel context before the next token is available.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = p.SelectKeyWithContext(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

// --- Rate limiter ---

func TestSetRateLimiter(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})

	// No limiter by default.
	if p.limiter != nil {
		t.Fatal("expected no limiter by default")
	}

	// Set a limiter.
	p.SetRateLimiter(60)
	if p.limiter == nil {
		t.Fatal("expected limiter to be set")
	}

	// Disable.
	p.SetRateLimiter(0)
	if p.limiter != nil {
		t.Fatal("expected limiter to be nil after disabling")
	}
}

// --- Concurrency ---

func TestSelectKey_Concurrent(t *testing.T) {
	keys := []string{"key-a", "key-b", "key-c"}
	p := newTestProviderConfig(keys)

	var wg sync.WaitGroup
	results := make(chan string, 100)

	for range 100 {
		wg.Go(func() {
			key, _, err := p.SelectKeyWithContext(context.Background())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			results <- key
		})
	}

	wg.Wait()
	close(results)

	// Verify all returned keys are valid.
	validKeys := map[string]bool{"key-a": true, "key-b": true, "key-c": true}
	for key := range results {
		if !validKeys[key] {
			t.Errorf("invalid key: %s", key)
		}
	}
}

func TestSelectKey_OnFailure_PrefersHealthyOverRecoveringPinnedKey(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})
	cp.mu.Lock()
	cp.stickyIdx = 0
	cp.keyStates[0].Recovering = true
	cp.lastSelectedSlot = 0
	cp.lastSelectedKey = "key-a"
	cp.mu.Unlock()

	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("expected healthy key-b, got %s", key)
	}
	if !switched {
		t.Fatal("expected slot switch when moving from recovering pinned key to healthy alternative")
	}
}

func TestSelectKey_OnFailure_UsesRecoveringPinnedKeyWhenNoHealthyAlternative(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})
	cp.mu.Lock()
	cp.stickyIdx = 0
	cp.keyStates[0].Recovering = true
	cp.keyStates[1].CooldownEnd = time.Now().Add(time.Minute)
	cp.lastSelectedSlot = 0
	cp.lastSelectedKey = "key-a"
	cp.mu.Unlock()

	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected recovering pinned key-a, got %s", key)
	}
	if switched {
		t.Fatal("did not expect slot switch when no healthy alternative exists")
	}
}

// TestSelectKey_SingleKeyNeverSwitches verifies that a provider with only one
// non-deactivated key never reports switched=true, even after cooldown/recovery
// cycles or compact ↔ main call interleaving that might leave lastSelectedSlot
// out of sync.
func TestSelectKey_SingleKeyNeverSwitches(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"only-key"})

	// First selection: lastSelectedSlot starts at -1, switched should be false.
	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "only-key" {
		t.Fatalf("expected only-key, got %s", key)
	}
	if switched {
		t.Fatal("first selection should not report switched for single key")
	}

	// Put key in cooldown, then let it expire so it becomes recovering.
	cp.MarkCooldown("only-key", 1*time.Millisecond)
	time.Sleep(2 * time.Millisecond)

	// Second selection: key is recovering but still the only option.
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after cooldown: %v", err)
	}
	if key != "only-key" {
		t.Fatalf("expected only-key after cooldown, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched for single key even after cooldown/recovery cycle")
	}

	// Mark success (clears recovery state), then select again.
	cp.MarkKeySuccess("only-key")
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after success: %v", err)
	}
	if key != "only-key" {
		t.Fatalf("expected only-key after success, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched for single key after success")
	}
}

// TestSelectKey_SingleActiveKeyAfterDeactivationNeverSwitches verifies that
// when a provider starts with two keys but one is deactivated, the remaining
// active key never reports switched=true even if lastSelectedSlot pointed to
// the now-deactivated slot.
func TestSelectKey_SingleActiveKeyAfterDeactivationNeverSwitches(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})

	// Select key-a first (slot 0).
	key, switched, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}
	if switched {
		t.Fatal("first selection should not report switched")
	}

	// Deactivate key-a; now only key-b remains active.
	cp.MarkDeactivated("key-a")

	// Select again: should get key-b (slot 1), but since only 1 active key,
	// switched should be false (no meaningful key switch).
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("expected key-b, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched when only one active key remains after deactivation")
	}
}

// TestSelectKey_OnFailure_Random verifies that on_failure+random selects a
// random available key on failure, and pins to it until it fails.
func TestSelectKey_OnFailure_Random(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
		KeyOrder:    config.KeyOrderRandom,
	}, []string{"key-a", "key-b", "key-c"})

	// First call: one of the three keys is selected and pinned.
	first, _, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Repeated calls should return the same key (pinned).
	for i := range 5 {
		key, _, err := cp.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if key != first {
			t.Errorf("iteration %d: expected pinned key %s, got %s", i, first, key)
		}
	}

	// After the pinned key fails, a new key is selected from remaining two.
	cp.MarkCooldown(first, 10*time.Second)
	second, _, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after first cooldown: %v", err)
	}
	if second == first {
		t.Errorf("expected a different key after cooldown, got %s again", first)
	}
}

// TestSelectKey_OnFailure_RandomInitialization verifies that on_failure+random
// initializes with a random stickyIdx, not always 0.
func TestSelectKey_OnFailure_RandomInitialization(t *testing.T) {
	// Test multiple independent configs to check random distribution
	const iterations = 100
	keyCount := 3
	keyList := make([]string, keyCount)
	for i := range keyCount {
		keyList[i] = fmt.Sprintf("key-%d", i)
	}

	// Collect first key choices from multiple independent configs
	firstChoices := make(map[string]int)

	for i := range iterations {
		cp := NewProviderConfig("test", config.ProviderConfig{
			KeyRotation: config.KeyRotationOnFailure,
			KeyOrder:    config.KeyOrderRandom,
		}, keyList)

		first, _, err := cp.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}

		// Verify it's one of the keys
		var found bool
		if slices.Contains(keyList, first) {
			found = true
		}
		if !found {
			t.Errorf("iteration %d: got unknown key %s", i, first)
		}

		firstChoices[first]++
	}

	// Check that distribution is somewhat even (not all selecting key-0)
	for _, key := range keyList {
		if firstChoices[key] == 0 {
			t.Errorf("key %s was never selected in %d iterations", key, iterations)
		}
		// Check that no single key dominates too much (very loose check)
		if firstChoices[key] > iterations*80/keyCount {
			t.Errorf("key %s selected %d/%d times (%.1f%%), possible bias",
				key, firstChoices[key], iterations,
				float64(firstChoices[key])/iterations*100)
		}
	}
}

// TestWarmup_SetsLastUsed verifies that calling Warmup() sets a non-zero
// LastUsed timestamp on every key.
func TestWarmup_SetsLastUsed(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{}, []string{"key-a", "key-b", "key-c"})

	for _, ks := range cp.keyStates {
		if !ks.LastUsed.IsZero() {
			t.Errorf("expected zero LastUsed before Warmup, got %v for key %s", ks.LastUsed, ks.Key)
		}
	}

	cp.Warmup()

	for _, ks := range cp.keyStates {
		if ks.LastUsed.IsZero() {
			t.Errorf("expected non-zero LastUsed after Warmup, got zero for key %s", ks.Key)
		}
	}
}
