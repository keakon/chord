package llm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"
)

func TestSelectKey_CodexSmartTreatsSnapshotAsHeadroomNotSoftCooldown(t *testing.T) {
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"key-a", "key-b"})
	reset := time.Now().Add(time.Hour)
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 20, ResetsAt: reset},
	})

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("SelectKeyWithContext: %v", err)
	}
	if key != "key-a" {
		t.Fatalf("expected snapshot-backed key-a, got %s", key)
	}
}

// --- Cooldown ---

func TestSelectKey_CooldownSkipsKey(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})

	// Use key-a first so it gets a LastUsed timestamp.
	key, _, _ := p.SelectKeyWithContext(context.Background())
	if key != "key-a" {
		t.Fatalf("expected key-a, got %s", key)
	}

	// Put key-b in cooldown for 1 minute.
	p.MarkCooldown("key-b", 1*time.Minute)

	// Next select should skip key-b (in cooldown) and pick key-a again.
	key, _, _ = p.SelectKeyWithContext(context.Background())
	if key != "key-a" {
		t.Fatalf("expected key-a (key-b in cooldown), got %s", key)
	}
}

func TestSelectKey_AllKeysCooldown(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})

	// Put both keys in cooldown.
	p.MarkCooldown("key-a", 10*time.Second)
	p.MarkCooldown("key-b", 20*time.Second)

	_, _, err := p.SelectKeyWithContext(context.Background())
	if err == nil {
		t.Fatal("expected error when all keys are cooling")
	}

	cooling, ok := errors.AsType[*AllKeysCoolingError](err)
	if !ok {
		t.Fatalf("expected AllKeysCoolingError, got %T: %v", err, err)
	}

	// RetryAfter should be close to 10s (the earliest cooldown end).
	if cooling.RetryAfter < 5*time.Second || cooling.RetryAfter > 15*time.Second {
		t.Fatalf("expected RetryAfter ~10s, got %v", cooling.RetryAfter)
	}
}

func TestSelectKey_AllKeysCoolingCarriesCooldownCause(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	err429 := error(&APIError{StatusCode: 429, Message: "rate limited", RetryAfter: 10 * time.Second})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	if res := markKeyCooldown(context.Background(), p, "key-b", "model-2", err429); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(429) cooldownApplied = false, want the key to enter cooldown")
	}

	// key-b recovers first (10s vs 1min), so its failure explains the wait.
	cause := selectCoolingError(t, p).cause
	if cause == nil {
		t.Fatal("cooling error carries no cause for cooldowns recorded from API failures")
	}
	if cause.Err != err429 || cause.Model != "model-2" || cause.Key != "key-b" {
		t.Fatalf("cooling cause = (%v, %q, %q), want the 429 of key-b/model-2", cause.Err, cause.Model, cause.Key)
	}
}

func TestClaimKeyCooldownCauseReportsEachFailureOnce(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	cause := selectCoolingError(t, p).cause
	if cause == nil {
		t.Fatal("cooling error carries no cause for a recorded 401")
	}
	if !p.claimKeyCooldownCause(cause) {
		t.Fatal("first claim of a recorded cause must succeed")
	}
	if p.claimKeyCooldownCause(cause) {
		t.Fatal("the same cause must be handed to only one reporting request")
	}

	// A newer failure replaces the cause; the stale one must not be reportable.
	err429 := error(&APIError{StatusCode: 429, Message: "rate limited", RetryAfter: 10 * time.Second})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err429); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(429) cooldownApplied = false, want the key to enter cooldown")
	}
	if p.claimKeyCooldownCause(cause) {
		t.Fatal("a cause replaced by a newer failure must not be reported")
	}
	replaced := selectCoolingError(t, p).cause
	if replaced == nil || replaced.Err != err429 {
		t.Fatalf("cooling cause after the new failure = %v, want the 429", replaced)
	}
	if !p.claimKeyCooldownCause(replaced) {
		t.Fatal("the replacement cause must be claimable")
	}
}

func TestClaimKeyCooldownCauseConcurrentWaitersReportOnce(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	cause := selectCoolingError(t, p).cause
	const waiters = 8
	results := make(chan bool, waiters)
	var wg sync.WaitGroup
	wg.Add(waiters)
	for range waiters {
		go func() {
			defer wg.Done()
			results <- p.claimKeyCooldownCause(cause)
		}()
	}
	wg.Wait()
	close(results)
	claimed := 0
	for ok := range results {
		if ok {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("successful claims = %d, want exactly 1", claimed)
	}
}

func TestMarkKeyCooldownCauseReportedBlocksClaim(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	cause := selectCoolingError(t, p).cause
	p.markKeyCooldownCauseReported(err401)
	if p.claimKeyCooldownCause(cause) {
		t.Fatal("a cause already shown to the user as a retry error must not be reported again")
	}
}

func TestCooldownCauseClearedOnSuccessAndReset(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	causeFor := func() *keyCooldownCause {
		p.mu.Lock()
		defer p.mu.Unlock()
		ks := p.keyStateByKeyLocked("key-a")
		if ks == nil {
			return nil
		}
		return ks.cooldownCause
	}
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	if causeFor() == nil {
		t.Fatal("expected the 401 to be recorded as the cooldown cause")
	}
	p.MarkKeySuccess("key-a")
	if got := causeFor(); got != nil {
		t.Fatalf("cooldown cause after MarkKeySuccess = %v, want nil", got)
	}

	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	p.MarkCooldown("key-a", 0)
	if got := causeFor(); got != nil {
		t.Fatalf("cooldown cause after a zero-duration MarkCooldown reset = %v, want nil", got)
	}
}

func TestDrainPendingKeyReportsReplaysCoolingOnce(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	err429 := error(&APIError{StatusCode: 429, Message: "rate limited", RetryAfter: 10 * time.Second})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err429); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(429) cooldownApplied = false, want the key to enter cooldown")
	}

	// A caller without a callback must not consume the report.
	p.drainPendingKeyReports(nil)

	var drained []message.StreamDelta
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 1 {
		t.Fatalf("drained deltas = %d, want exactly 1 retry error", len(drained))
	}
	if drained[0].Type != message.StreamDeltaRetryError || drained[0].Err != err429 {
		t.Fatalf("drained delta = (%q, %v), want the recorded 429 as a retry error", drained[0].Type, drained[0].Err)
	}
	if drained[0].Provider != "test" || drained[0].Model != "model-1" || drained[0].MaskedKey == "" {
		t.Fatalf("retry error route = (%q, %q, key=%q), want the provider, model and masked key", drained[0].Provider, drained[0].Model, drained[0].MaskedKey)
	}

	drained = nil
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 0 {
		t.Fatalf("second drain emitted %d deltas, want none: each cause is reported once", len(drained))
	}
}

func TestDrainPendingKeyReportsEmitsInvalidationOverlayOnce(t *testing.T) {
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-1", Email: "user@example.com", Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	err401 := error(&APIError{StatusCode: 401, Message: "Your account has been disabled."})
	if res := markKeyCooldown(context.Background(), p, "oauth-key", "model-1", err401); !res.cooldownApplied || !res.deactivated {
		t.Fatalf("markKeyCooldown(401) = %+v, want a permanent deactivation", res)
	}

	var drained []message.StreamDelta
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 1 {
		t.Fatalf("drained deltas = %d, want exactly 1 key overlay", len(drained))
	}
	if drained[0].Type != message.StreamDeltaKeyDeactivated {
		t.Fatalf("drained delta type = %q, want %q", drained[0].Type, message.StreamDeltaKeyDeactivated)
	}
	if drained[0].AccountID != "acc-1" || drained[0].Email != "user@example.com" {
		t.Fatalf("overlay identity = (%q, %q), want the deactivated account", drained[0].AccountID, drained[0].Email)
	}

	drained = nil
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 0 {
		t.Fatalf("second drain emitted %d deltas, want none", len(drained))
	}
}

func TestDrainPendingKeyReportsSkipsFailureAlreadyReported(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	err401 := error(&APIError{StatusCode: 401, Message: "unauthorized"})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	p.markKeyCooldownCauseReported(err401)

	var drained []message.StreamDelta
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 0 {
		t.Fatalf("drained deltas = %d, want none for a failure already shown as a retry error", len(drained))
	}
}

func TestDrainPendingKeyReportsSkipsOverlayAlreadyEmitted(t *testing.T) {
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-1", Email: "user@example.com", Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	err401 := error(&APIError{StatusCode: 401, Message: "Your account has been disabled."})
	if res := markKeyCooldown(context.Background(), p, "oauth-key", "model-1", err401); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	// The failing request had its own callback and already showed the overlay.
	p.markKeyCooldownCauseDeltasEmitted(err401)

	var drained []message.StreamDelta
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 0 {
		t.Fatalf("drained deltas = %d, want none for an overlay already shown", len(drained))
	}
}

func TestMarkKeyCooldownCauseReportedMatchesTheRecordedFailure(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	errFirst := error(&APIError{StatusCode: 401, Message: "first failure"})
	errNewer := error(&APIError{StatusCode: 429, Message: "newer failure", RetryAfter: 10 * time.Second})
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", errFirst); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(401) cooldownApplied = false, want the key to enter cooldown")
	}
	// A newer failure replaces the cause on the key while the first request is
	// still unwinding.
	if res := markKeyCooldown(context.Background(), p, "key-a", "model-1", errNewer); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(429) cooldownApplied = false, want the key to enter cooldown")
	}
	// The first request reports its own failure late; only that failure may be
	// marked, so the newer cause still reaches the user.
	p.markKeyCooldownCauseReported(errFirst)

	var drained []message.StreamDelta
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 1 || drained[0].Type != message.StreamDeltaRetryError || drained[0].Err != errNewer {
		t.Fatalf("drained deltas = %v, want only the newer failure as a retry error", drained)
	}
}

func TestMarkKeyCooldownCauseDeltasEmittedMatchesTheRecordedFailure(t *testing.T) {
	p := newTestProviderConfig([]string{"oauth-key"})
	p.mu.Lock()
	p.keyStates[0].OAuthInfo = &OAuthKeyInfo{AccountID: "acc-1", Email: "user@example.com", Expires: time.Now().Add(time.Hour).UnixMilli()}
	p.mu.Unlock()
	errOverlay := error(&APIError{StatusCode: 401, Message: "Your account has been disabled."})
	if res := markKeyCooldown(context.Background(), p, "oauth-key", "model-1", errOverlay); !res.cooldownApplied || !res.deactivated {
		t.Fatalf("markKeyCooldown(401) = %+v, want a permanent deactivation", res)
	}
	// The overlay of the first failure reached the user through the request's
	// own callback; a newer failure meanwhile replaced the cause on the key.
	errNewer := error(&APIError{StatusCode: 429, Message: "rate limited", RetryAfter: 10 * time.Second})
	if res := markKeyCooldown(context.Background(), p, "oauth-key", "model-2", errNewer); !res.cooldownApplied {
		t.Fatal("markKeyCooldown(429) cooldownApplied = false, want the key to enter cooldown")
	}
	p.markKeyCooldownCauseDeltasEmitted(errOverlay)

	var drained []message.StreamDelta
	p.drainPendingKeyReports(func(delta message.StreamDelta) { drained = append(drained, delta) })
	if len(drained) != 1 || drained[0].Type != message.StreamDeltaRetryError || drained[0].Err != errNewer {
		t.Fatalf("drained deltas = %v, want only the newer failure as a retry error", drained)
	}
}

func TestSelectKey_CooldownExpires(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})

	// Put key-a in cooldown that already expired.
	p.mu.Lock()
	for _, ks := range p.keyStates {
		if ks.Key == "key-a" {
			ks.CooldownEnd = time.Now().Add(-1 * time.Second) // expired
		}
	}
	p.mu.Unlock()

	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// key-a's cooldown has expired, so it should be selectable.
	// Both keys are at zero LastUsed, but key-a's cooldown just expired,
	// so key-a is still eligible.
	if key != "key-a" && key != "key-b" {
		t.Fatalf("expected key-a or key-b, got %s", key)
	}
}

func TestMarkCooldown_NonExistentKey(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	// Should not panic.
	p.MarkCooldown("nonexistent", 10*time.Second)
}

func TestMarkTemporaryUnavailable_skipsWhenAlreadyCooling(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkCooldown("key-a", 10*time.Minute)
	availBefore, _ := p.AvailableKeyCount()
	p.MarkTemporaryUnavailable("key-a", time.Second)
	availAfter, _ := p.AvailableKeyCount()
	if availBefore != availAfter {
		t.Fatalf("available %d -> %d, want unchanged when API cooldown active", availBefore, availAfter)
	}
}

func TestMarkTemporaryUnavailable_blocksUntilExpiry(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkTemporaryUnavailable("key-a", 30*time.Second)
	avail, total := p.AvailableKeyCount()
	if total != 2 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/2", avail, total)
	}
	// key-a is in cooldown (not yet healthy), key-b is healthy
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/2 (key-b healthy, key-a cooling)", healthy, htotal)
	}
}

func TestHealthyKeyCount_recoveringKeyNotCounted(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkTemporaryUnavailable("key-a", 30*time.Second)
	// Expire the cooldown so key-a is selectable but still Recovering
	p.mu.Lock()
	for _, ks := range p.keyStates {
		if ks.Key == "key-a" {
			ks.CooldownEnd = time.Now().Add(-time.Second)
		}
	}
	p.mu.Unlock()
	// AvailableKeyCount sees both keys as selectable
	avail, total := p.AvailableKeyCount()
	if total != 2 || avail != 2 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 2/2 once temporary cooldown expired", avail, total)
	}
	// HealthyKeyCount excludes recovering key-a
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/2 (key-a recovering, key-b healthy)", healthy, htotal)
	}
}

func TestAvailableKeyCount_codexSnapshotAt100DoesNotReduceAvailability(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	reset := time.Now().Add(time.Hour)
	p.UpdateKeySnapshot("key-a", &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{UsedPct: 100, ResetsAt: reset},
	})
	avail, total := p.AvailableKeyCount()
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if avail != 2 {
		t.Fatalf("available = %d, want 2 (100%% in headers does not block selection)", avail)
	}
}

func TestKeyPoolNextTransition_zeroWhenSingleKey(t *testing.T) {
	p := newTestProviderConfig([]string{"only"})
	p.MarkCooldown("only", time.Minute)
	if d := p.KeyPoolNextTransition(); d != 0 {
		t.Fatalf("KeyPoolNextTransition = %v, want 0 for single-key pool", d)
	}
}

func TestMarkQuotaExhaustedUntil_blocksSelectionUntilReset(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkQuotaExhaustedUntil("key-a", time.Now().Add(2*time.Hour))
	avail, total := p.AvailableKeyCount()
	if total != 2 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/2 with one exhausted key", avail, total)
	}
	// Exhausted key is not selectable, so healthy = available here
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/2 with one exhausted key", healthy, htotal)
	}
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Fatalf("selected key = %q, want key-b when key-a exhausted", key)
	}
}

func TestMarkQuotaExhaustedUntil_blocksAllDuplicateKeySlots(t *testing.T) {
	p := newTestProviderConfig([]string{"shared-key", "other-key", "shared-key"})
	p.mu.Lock()
	p.stickyIdx = 2
	p.mu.Unlock()
	key, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("initial SelectKeyWithContext: %v", err)
	}
	if key != "shared-key" {
		t.Fatalf("initial selected key = %q, want shared-key", key)
	}

	p.MarkQuotaExhaustedUntil("shared-key", time.Now().Add(2*time.Hour))
	avail, total := p.AvailableKeyCount()
	if total != 3 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/3 with both duplicate shared-key slots exhausted", avail, total)
	}
	key, _, err = p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("second SelectKeyWithContext: %v", err)
	}
	if key != "other-key" {
		t.Fatalf("selected key = %q, want other-key after duplicate shared-key slots exhausted", key)
	}
}

func TestMarkKeySuccess_ClearsPersistedCodexResetHints(t *testing.T) {
	auth := config.AuthConfig{"openai": {{OAuth: &config.OAuthCredential{
		Access:                "oauth-key",
		Refresh:               "refresh-token",
		Expires:               time.Now().Add(time.Hour).UnixMilli(),
		AccountID:             "acc-1",
		CodexPrimaryResetAt:   time.Now().Add(2 * time.Hour).UnixMilli(),
		CodexSecondaryResetAt: time.Now().Add(6 * time.Hour).UnixMilli(),
	}}}}
	var authMu sync.Mutex
	p := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex}, []string{"oauth-key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &auth, &authMu, map[string]OAuthKeySetup{
		"oauth-key": {
			CredentialIndex:       0,
			AccountID:             "acc-1",
			Expires:               auth["openai"][0].OAuth.Expires,
			CodexPrimaryResetAt:   auth["openai"][0].OAuth.CodexPrimaryResetAt,
			CodexSecondaryResetAt: auth["openai"][0].OAuth.CodexSecondaryResetAt,
		},
	}, "")
	p.MarkKeySuccess("oauth-key")
	if got := auth["openai"][0].OAuth; got.CodexPrimaryResetAt != 0 || got.CodexSecondaryResetAt != 0 {
		t.Fatalf("expected MarkKeySuccess to clear persisted codex hints, got %#v", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if info := p.keyStates[0].OAuthInfo; info == nil || info.CodexPrimaryResetAt != 0 || info.CodexSecondaryResetAt != 0 {
		t.Fatalf("expected runtime OAuth info codex hints to be cleared, got %#v", info)
	}
}

func TestHealthyKeyCount_exhaustedKeyTransitionsToRecoveringOnSelect(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	// Mark key-a exhausted but already expired (reset in the past)
	p.mu.Lock()
	for _, ks := range p.keyStates {
		if ks.Key == "key-a" {
			ks.ExhaustedUntil = time.Now().Add(-time.Second)
		}
	}
	p.mu.Unlock()
	// Before selection: key-a ExhaustedUntil expired, Recovering=false → counted as healthy
	healthy, total := p.HealthyKeyCount()
	if total != 2 || healthy != 2 {
		t.Fatalf("HealthyKeyCount before select = %d/%d, want 2/2", healthy, total)
	}
	// Select key-a — SelectKeyWithContext should set Recovering=true
	p.mu.Lock()
	p.stickyIdx = 0 // force selection of key-a
	p.mu.Unlock()
	_, _, err := p.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After selection: key-a is Recovering, only key-b is healthy
	healthy, total = p.HealthyKeyCount()
	if total != 2 || healthy != 1 {
		t.Fatalf("HealthyKeyCount after select = %d/%d, want 1/2 (key-a recovering)", healthy, total)
	}
}

func TestKeyPoolNextTransition_considersExhaustedUntil(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkQuotaExhaustedUntil("key-a", time.Now().Add(90*time.Second))
	d := p.KeyPoolNextTransition()
	if d < 60*time.Second || d > 95*time.Second {
		t.Fatalf("KeyPoolNextTransition = %v, want ~90s from exhausted reset", d)
	}
}

// --- MarkDeactivated ---

func TestMarkDeactivated_removesKeyFromTotal(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkDeactivated("key-a")

	// total should exclude deactivated key
	avail, total := p.AvailableKeyCount()
	if total != 1 || avail != 1 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 1/1 after deactivating key-a", avail, total)
	}
	healthy, htotal := p.HealthyKeyCount()
	if htotal != 1 || healthy != 1 {
		t.Fatalf("HealthyKeyCount = %d/%d, want 1/1 after deactivating key-a", healthy, htotal)
	}
	if cnt := p.KeyCount(); cnt != 1 {
		t.Fatalf("KeyCount = %d, want 1 after deactivating key-a", cnt)
	}
}

func TestMarkDeactivated_keyNotSelected(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkDeactivated("key-a")

	for range 5 {
		key, _, err := p.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("SelectKeyWithContext: %v", err)
		}
		if key == "key-a" {
			t.Fatal("deactivated key-a was selected")
		}
	}
}

func TestMarkDeactivated_allKeysDeactivated(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a", "key-b"})
	p.MarkDeactivated("key-a")
	p.MarkDeactivated("key-b")

	if cnt := p.KeyCount(); cnt != 0 {
		t.Fatalf("KeyCount = %d, want 0 after deactivating all keys", cnt)
	}
	avail, total := p.AvailableKeyCount()
	if total != 0 || avail != 0 {
		t.Fatalf("AvailableKeyCount = %d/%d, want 0/0", avail, total)
	}
}

func TestMarkDeactivated_nonExistentKeyNoOp(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	// Should not panic.
	p.MarkDeactivated("nonexistent")
	if cnt := p.KeyCount(); cnt != 1 {
		t.Fatalf("KeyCount = %d, want 1 after no-op deactivate", cnt)
	}
}

func TestMarkCooldownSaturatesWithoutOverflow(t *testing.T) {
	p := newTestProviderConfig([]string{"key-a"})
	for range 80 {
		p.MarkCooldown("key-a", time.Second)
	}

	p.mu.Lock()
	cooldownEnd := p.keyStates[0].CooldownEnd
	p.mu.Unlock()

	until := time.Until(cooldownEnd)
	if until <= 0 {
		t.Fatalf("time.Until(CooldownEnd) = %v, want positive cooldown", until)
	}
	if until > time.Minute+2*time.Second {
		t.Fatalf("time.Until(CooldownEnd) = %v, want capped near 1m", until)
	}
}

// --- Key Rotation: on_failure ---

// TestSelectKey_OnFailure_NoCooldown verifies that repeated calls with on_failure
// rotation always return the same key when no cooldown is active.
func TestSelectKey_OnFailure_NoCooldown(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b", "key-c"})

	for i := range 5 {
		key, _, err := cp.SelectKeyWithContext(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "key-a" {
			t.Errorf("iteration %d: expected key-a, got %s", i, key)
		}
	}
}

// TestSelectKey_OnFailure_SwitchOnCooldown verifies that on_failure rotation
// switches to the next available key when the pinned key enters cooldown.
func TestSelectKey_OnFailure_SwitchOnCooldown(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})

	key, _, err := cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-a" {
		t.Errorf("expected key-a, got %s", key)
	}

	cp.MarkCooldown("key-a", 10*time.Second)

	key, _, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Errorf("expected key-b after cooldown, got %s", key)
	}

	// Subsequent calls should stay on key-b.
	key, _, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-b" {
		t.Errorf("expected key-b (pinned), got %s", key)
	}
}

// TestSelectKey_OnFailure_AllCooldown verifies that AllKeysCoolingError is
// returned when all keys are in cooldown under on_failure rotation.
func TestSelectKey_OnFailure_AllCooldown(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b"})

	cp.MarkCooldown("key-a", 10*time.Second)
	cp.MarkCooldown("key-b", 10*time.Second)

	_, _, err := cp.SelectKeyWithContext(context.Background())
	if err == nil {
		t.Fatal("expected AllKeysCoolingError, got nil")
	}
	if _, ok := errors.AsType[*AllKeysCoolingError](err); !ok {
		t.Errorf("expected *AllKeysCoolingError, got %T: %v", err, err)
	}
}

// TestSelectKey_SingleSelectableKeyAfterCooldownNeverSwitches verifies that
// when multiple keys exist but only one is selectable (others are cooling),
// the remaining selectable key never reports switched=true even if
// lastSelectedSlot pointed to a now-cooling slot.
func TestSelectKey_SingleSelectableKeyAfterCooldownNeverSwitches(t *testing.T) {
	cp := NewProviderConfig("test", config.ProviderConfig{
		KeyRotation: config.KeyRotationOnFailure,
	}, []string{"key-a", "key-b", "key-c"})

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

	// Cool down key-a; key-b also cooling; only key-c is selectable.
	cp.MarkCooldown("key-a", 10*time.Minute)
	cp.MarkCooldown("key-b", 10*time.Minute)

	// Select again: should get key-c (slot 2), but since only 1 selectable key,
	// switched should be false — this is a retry, not a key switch.
	key, switched, err = cp.SelectKeyWithContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "key-c" {
		t.Fatalf("expected key-c, got %s", key)
	}
	if switched {
		t.Fatal("should not report switched when only one selectable key remains after cooldown")
	}
}
