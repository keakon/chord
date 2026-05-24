package llm

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/keakon/golog/log"
	"golang.org/x/time/rate"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

func (p *ProviderConfig) keyStateBySlotLocked(slot int) *KeyState {
	if slot < 0 || slot >= len(p.keyStates) {
		return nil
	}
	return p.keyStates[slot]
}

func (p *ProviderConfig) keyStateByKeyLocked(key string) *KeyState {
	for _, ks := range p.keyStates {
		if ks.Key == key {
			return ks
		}
	}
	return nil
}

func (p *ProviderConfig) keyStateSelectableLocked(now time.Time, ks *KeyState) bool {
	if ks == nil {
		return false
	}
	return keyStateSelectable(now, ks)
}

func (p *ProviderConfig) keyStateHealthyLocked(now time.Time, ks *KeyState) bool {
	if !p.keyStateSelectableLocked(now, ks) {
		return false
	}
	return !ks.Recovering && !oauthTokenLikelyExpired(now, ks)
}

func oauthTokenLikelyExpired(now time.Time, ks *KeyState) bool {
	return ks != nil && ks.OAuthInfo != nil && ks.OAuthInfo.Expires > 0 && time.UnixMilli(ks.OAuthInfo.Expires).Before(now.Add(time.Minute))
}

func (p *ProviderConfig) markHealthyLocked(ks *KeyState) {
	if ks == nil {
		return
	}
	ks.Recovering = false
}

func (p *ProviderConfig) markRecoveringLocked(ks *KeyState) {
	if ks == nil {
		return
	}
	ks.Recovering = true
}

func (p *ProviderConfig) markCooldownLocked(ks *KeyState, d time.Duration) {
	if ks == nil {
		return
	}
	if d <= 0 {
		ks.CooldownCount = 0
		ks.CooldownEnd = time.Time{}
		return
	}
	ks.CooldownCount++
	ks.Recovering = true
	// Exponential backoff: d * 2^(count-1), capped at 1 minute.
	const maxCooldown = 1 * time.Minute
	effective := saturatingDoublingDuration(d, maxCooldown, ks.CooldownCount-1)
	ks.CooldownEnd = time.Now().Add(effective)
}

func (p *ProviderConfig) markQuotaExhaustedLocked(ks *KeyState, until time.Time) {
	if ks == nil {
		return
	}
	if until.After(ks.ExhaustedUntil) {
		ks.ExhaustedUntil = until
	}
	if until.After(ks.SoftCooldownUntil) {
		ks.SoftCooldownUntil = until
	}
	ks.CooldownEnd = time.Time{}
	ks.Recovering = true
}

func (p *ProviderConfig) markTemporaryUnavailableLocked(ks *KeyState, now time.Time, d time.Duration) {
	if ks == nil || d <= 0 {
		return
	}
	if ks.CooldownEnd.After(now) || ks.ExhaustedUntil.After(now) {
		return
	}
	ks.CooldownEnd = now.Add(d)
	ks.Recovering = true
}

func (p *ProviderConfig) bestCandidateIndexLocked(now time.Time, candidates []int) int {
	if len(candidates) == 0 {
		return -1
	}
	best := candidates[0]
	for _, idx := range candidates[1:] {
		if idx < 0 || idx >= len(p.keyStates) {
			continue
		}
		if p.keyOrder == config.KeyOrderSmart {
			if p.codexSmartLessLocked(now, p.keyStates[idx], p.keyStates[best]) {
				best = idx
			}
			continue
		}
		if p.keyStates[idx].LastUsed.Before(p.keyStates[best].LastUsed) {
			best = idx
		}
	}
	return best
}

func (p *ProviderConfig) postSelectLocked(selectedKS *KeyState, selectedIdx int, now time.Time) (string, bool) {
	if !selectedKS.ExhaustedUntil.IsZero() && !now.Before(selectedKS.ExhaustedUntil) {
		selectedKS.ExhaustedUntil = time.Time{}
		selectedKS.Recovering = true
	}
	if selectedKS.RateLimit != nil {
		p.inlineDisplaySnap = selectedKS.RateLimit
	} else if selectedIdx != p.lastSelectedSlot {
		p.inlineDisplaySnap = nil
	}
	selectedKey := selectedKS.Key
	// Suppress the switched flag when only one key is selectable to avoid
	// spurious key_switched notifications. When other keys are cooling or
	// exhausted, the same key is repeatedly returned — that is a retry, not
	// a switch. Also suppress when a key was deactivated between selections
	// (e.g. compact ↔ main call interleaving that might leave lastSelectedSlot
	// out of sync).
	selectableSlots := 0
	for _, ks := range p.keyStates {
		if p.keyStateSelectableLocked(now, ks) {
			selectableSlots++
		}
	}
	switched := selectableSlots > 1 && p.lastSelectedSlot >= 0 && p.lastSelectedSlot != selectedIdx
	p.lastSelectedSlot = selectedIdx
	p.lastSelectedKey = selectedKey
	selectedKS.EverSelected = true
	return selectedKey, switched
}

func (p *ProviderConfig) pickRandomHealthyCandidateLocked(now time.Time, excludeIdx int) int {
	var healthy []int
	var fallback []int
	for i, ks := range p.keyStates {
		if i == excludeIdx {
			continue
		}
		if !p.keyStateSelectableLocked(now, ks) {
			continue
		}
		fallback = append(fallback, i)
		if p.keyStateHealthyLocked(now, ks) {
			healthy = append(healthy, i)
		}
	}
	candidates := healthy
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return -1
	}
	if p.keyOrder == config.KeyOrderSmart {
		return p.bestCandidateIndexLocked(now, candidates)
	}
	return candidates[rand.Intn(len(candidates))]
}

func (p *ProviderConfig) selectOnFailureKeyLocked(now time.Time) (*KeyState, int) {
	pinnedIdx := p.stickyIdx
	pinned := p.keyStateBySlotLocked(pinnedIdx)
	if p.keyOrder == config.KeyOrderSmart && pinned != nil && pinned.EverSelected {
		if p.keyStateSelectableLocked(now, pinned) {
			if p.keyStateHealthyLocked(now, pinned) {
				pinned.LastUsed = now
				return pinned, pinnedIdx
			}
			if altIdx := p.pickRandomHealthyCandidateLocked(now, pinnedIdx); altIdx >= 0 {
				p.stickyIdx = altIdx
				selected := p.keyStates[altIdx]
				selected.LastUsed = now
				return selected, altIdx
			}
			pinned.LastUsed = now
			return pinned, pinnedIdx
		}
	} else if p.keyOrder != config.KeyOrderSmart && pinned != nil && p.keyStateSelectableLocked(now, pinned) {
		if p.keyStateHealthyLocked(now, pinned) {
			pinned.LastUsed = now
			return pinned, pinnedIdx
		}
		if altIdx := p.pickRandomHealthyCandidateLocked(now, pinnedIdx); altIdx >= 0 {
			p.stickyIdx = altIdx
			selected := p.keyStates[altIdx]
			selected.LastUsed = now
			return selected, altIdx
		}
		pinned.LastUsed = now
		return pinned, pinnedIdx
	}

	var healthy []int
	var fallback []int
	for i, ks := range p.keyStates {
		if !p.keyStateSelectableLocked(now, ks) {
			continue
		}
		fallback = append(fallback, i)
		if p.keyStateHealthyLocked(now, ks) {
			healthy = append(healthy, i)
		}
	}
	candidates := healthy
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return nil, -1
	}
	var idx int
	if p.keyOrder == config.KeyOrderRandom {
		idx = candidates[rand.Intn(len(candidates))]
	} else {
		idx = p.bestCandidateIndexLocked(now, candidates)
	}
	p.stickyIdx = idx
	selected := p.keyStates[idx]
	selected.LastUsed = now
	return selected, idx
}

// SetOAuthRefresher configures OAuth credential refresh support.
// oauthKeys must map the current access token string to the auth.yaml slot metadata
// for each OAuth credential that should participate in selection.

func (p *ProviderConfig) Warmup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for _, ks := range p.keyStates {
		ks.LastUsed = now
	}
}

// SetRateLimiter configures an optional rate limiter. rpm is the maximum
// requests per minute. If rpm <= 0, rate limiting is disabled.

func (p *ProviderConfig) SetRateLimiter(rpm int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if rpm <= 0 {
		p.limiter = nil
		return
	}

	burst := rpm / 5
	if burst < 5 {
		burst = 5
	}
	p.limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), burst)
}

// SelectKeyWithContext returns an API key that is selectable.
// Codex x-codex-* snapshots do not affect selection (only real errors e.g. 429
// apply cooldown via MarkCooldown). Selection follows key_rotation + key_order:
// on_failure pins to stickyIdx until that key is unavailable; while pinned, a
// recovering key is deprioritized in favor of any other selectable healthy key,
// but may still be retried when no healthy alternative exists. per_request picks
// a key on every call per key_order.
// key_order=sequential picks the earliest-LastUsed selectable key; key_order=random
// picks uniformly at random among selectable candidates, preferring healthy keys
// over recovering ones when possible.
// If a rate limiter is configured, it waits for a token before selecting.
// If no key is selectable, it returns AllKeysCoolingError with a retry duration.
// If the selected key is an OAuth token that is about to expire (<60s), it
// refreshes the token before returning.
// The second return value is true when the selected credential slot differs from
// the previously selected slot (i.e., a real key-slot switch occurred).

func (p *ProviderConfig) SelectKeyWithContext(ctx context.Context) (string, bool, error) {
	// Rate limiting: wait for a token before proceeding (outside the mutex).
	// This allows multiple agents/goroutines to queue up without holding the lock.
	if p.limiter != nil {
		if err := p.limiter.Wait(ctx); err != nil {
			return "", false, err
		}
	}

	p.mu.Lock()
	p.maybeReloadAuthStateLocked()

	if len(p.keyStates) == 0 {
		// No keys configured — return empty string for providers that don't require auth
		// (e.g., local services, public APIs). The provider implementation should handle
		// empty keys gracefully (e.g., omit Authorization header).
		p.mu.Unlock()
		return "", false, nil
	}

	now := time.Now()
	selectableTotal := 0
	for _, ks := range p.keyStates {
		if ks.Invalid {
			continue
		}
		selectableTotal++
	}
	if selectableTotal == 0 {
		p.mu.Unlock()
		return "", false, &NoUsableKeysError{Provider: p.name}
	}

	var selectedKS *KeyState
	selectedIdx := -1
	if p.keyRotation == config.KeyRotationOnFailure {
		selectedKS, selectedIdx = p.selectOnFailureKeyLocked(now)
	} else {
		if p.keyOrder == config.KeyOrderRandom {
			selectedIdx = p.pickRandomHealthyCandidateLocked(now, -1)
			if selectedIdx >= 0 {
				selectedKS = p.keyStates[selectedIdx]
				selectedKS.LastUsed = now
			}
		} else {
			var healthyCandidates []int
			var fallbackCandidates []int
			for i, ks := range p.keyStates {
				if !p.keyStateSelectableLocked(now, ks) {
					continue
				}
				fallbackCandidates = append(fallbackCandidates, i)
				if p.keyStateHealthyLocked(now, ks) {
					healthyCandidates = append(healthyCandidates, i)
				}
			}
			candidates := healthyCandidates
			if len(candidates) == 0 {
				candidates = fallbackCandidates
			}
			selectedIdx = p.bestCandidateIndexLocked(now, candidates)
			if selectedIdx >= 0 {
				selectedKS = p.keyStates[selectedIdx]
				selectedKS.LastUsed = now
			}
		}
	}

	if selectedKS == nil || selectedIdx < 0 {
		retryAfter := p.earliestKeyRecoveryLocked(now)
		if retryAfter <= 0 {
			retryAfter = 10 * time.Second
		}
		p.mu.Unlock()
		return "", false, &AllKeysCoolingError{RetryAfter: retryAfter}
	}

	// Refresh OAuth when no access token is available or when JWT exp/expires says
	// the token is likely stale. Provider auth failures still decide permanent status.
	if selectedKS.OAuthInfo != nil && p.oauthRefresher != nil && (selectedKS.Key == "" || oauthTokenLikelyExpired(now, selectedKS)) {
		if err := p.refreshOAuthKey(ctx, selectedKS); err != nil {
			if config.IsRefreshTokenInvalid(err) {
				persist := p.markInvalidKeyStateLocked(selectedKS, config.OAuthStatusExpired)
				hasRemaining := false
				for _, ks := range p.keyStates {
					if !ks.Invalid {
						hasRemaining = true
						break
					}
				}
				p.mu.Unlock()
				p.persistInvalidOAuthCredential(persist)
				if hasRemaining {
					return p.SelectKeyWithContext(ctx)
				}
				return "", false, fmt.Errorf("OAuth refresh token invalid provider=%v: %w", p.name, err)
			}
			// Log warning but continue with the old token (might still work).
			log.Warnf("failed to refresh OAuth token on-demand provider=%v error=%v", p.name, err)
		}
	}

	selectedKey, switched := p.postSelectLocked(selectedKS, selectedIdx, now)
	shouldRefreshCodexUsage := selectedKS.OAuthInfo != nil && p.oauthProfile == config.OAuthProfileOpenAICodex && p.codexPollFetchFn != nil
	p.mu.Unlock()
	if shouldRefreshCodexUsage {
		p.WakeCodexRateLimitPolling()
	}
	return selectedKey, switched, nil
}

// MarkTemporaryUnavailable blocks the key until now+d if it is not already in a
// future cooldown window (e.g. from MarkCooldown after 429). Used when rotating
// to another key after retriable failures so the UI key pool reflects reality.
// Does not touch CooldownCount (no exponential stacking with API backoff).

func (p *ProviderConfig) MarkTemporaryUnavailable(key string, d time.Duration) {
	if d <= 0 || key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil {
		return
	}
	p.markTemporaryUnavailableLocked(ks, time.Now(), d)
}

// MarkRecovering marks the key as selectable-but-not-preferred. Under
// key_rotation=on_failure, selection prefers other healthy keys before retrying
// a recovering key, without applying an explicit cooldown window.

func (p *ProviderConfig) MarkRecovering(key string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markRecoveringLocked(p.keyStateByKeyLocked(key))
}

// MarkCooldown puts the specified key into cooldown for the given duration.
// The key will not be selected by SelectKey until the cooldown expires.
// If d > 0, the count is incremented and exponential backoff applied (capped at 1min).
// If d == 0, the count is reset.

func (p *ProviderConfig) MarkCooldown(key string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markCooldownLocked(p.keyStateByKeyLocked(key), d)
}

// MarkQuotaExhaustedUntil marks a key unavailable until the real provider reset time.
// Unlike MarkCooldown, this does not use exponential backoff or the 5-minute cap.

func (p *ProviderConfig) MarkQuotaExhaustedUntil(key string, until time.Time) {
	if key == "" || until.IsZero() || !until.After(time.Now()) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markQuotaExhaustedLocked(p.keyStateByKeyLocked(key), until)
}

// MarkKeySuccess clears soft failure state after a successful request.

func (p *ProviderConfig) MarkKeySuccess(key string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil {
		p.mu.Unlock()
		return
	}
	ks.CooldownCount = 0
	if !ks.ExhaustedUntil.After(time.Now()) {
		ks.ExhaustedUntil = time.Time{}
	}
	clearSoftHints := ks.OAuthInfo != nil && (ks.OAuthInfo.CodexPrimaryResetAt != 0 || ks.OAuthInfo.CodexSecondaryResetAt != 0)
	p.markHealthyLocked(ks)
	p.mu.Unlock()
	if clearSoftHints {
		p.clearCodexResetHintsForKey(key)
	}
}

// UpdateKeySnapshot stores the latest rate-limit snapshot for the given key.

func (p *ProviderConfig) UpdateKeySnapshot(key string, snap *ratelimit.KeyRateLimitSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ks := range p.keyStates {
		if ks.Key == key {
			ks.RateLimit = snap
			if ks.Key == p.lastSelectedKey {
				p.inlineDisplaySnap = snap
			}
			return
		}
	}
}

// KeySnapshot returns the latest rate-limit snapshot for the given key, or nil.

func (p *ProviderConfig) KeySnapshot(key string) *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maybeReloadAuthStateLocked()
	for _, ks := range p.keyStates {
		if ks.Key == key {
			return ks.RateLimit
		}
	}
	return nil
}

func (p *ProviderConfig) ClearInlineDisplayRateLimitSnapshot() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inlineDisplaySnap = nil
}

func (p *ProviderConfig) CurrentKeySnapshot() *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maybeReloadAuthStateLocked()
	if p.lastSelectedKey == "" {
		if p.inlineDisplaySnap != nil {
			return p.inlineDisplaySnap
		}
		return nil
	}
	for _, ks := range p.keyStates {
		if ks.Key == p.lastSelectedKey {
			if ks.RateLimit != nil {
				return ks.RateLimit
			}
			return p.inlineDisplaySnap
		}
	}
	return nil
}

// TryRefreshOAuthKey attempts to refresh the OAuth token for the key with the
// given access token value. Returns the refreshed access token, whether a refresh
// succeeded, and the refresh error when it failed for an OAuth key. Returns
// "", false, nil if the key is not an OAuth token or no refresher is configured.

func (p *ProviderConfig) KeyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maybeReloadAuthStateLocked()
	count := 0
	for _, ks := range p.keyStates {
		if !ks.Invalid {
			count++
		}
	}
	return count
}

// AvailableKeyCount returns the number of keys that are selectable and the total
// non-deactivated key count.
// Safe for concurrent use (holds p.mu).

func (p *ProviderConfig) AvailableKeyCount() (available, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maybeReloadAuthStateLocked()
	now := time.Now()
	for _, ks := range p.keyStates {
		if ks.Invalid {
			continue
		}
		total++
		if p.keyStateSelectableLocked(now, ks) {
			available++
		}
	}
	return available, total
}

// HealthyKeyCount returns the number of keys that are selectable and have been
// re-confirmed healthy (i.e. not in recovering state), along with the total
// non-deactivated key count.

func (p *ProviderConfig) HealthyKeyCount() (healthy, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maybeReloadAuthStateLocked()
	now := time.Now()
	for _, ks := range p.keyStates {
		if ks.Invalid {
			continue
		}
		total++
		if p.keyStateHealthyLocked(now, ks) {
			healthy++
		}
	}
	return healthy, total
}

// MarkInvalidated permanently marks an OAuth key as invalidated and persists that
// state back to auth.yaml when possible. Unlike MarkExpired, this represents an
// account invalidation signal that usually requires re-auth.

func (p *ProviderConfig) ConfirmedKeyCount() (confirmed, total int) {
	return p.HealthyKeyCount()
}

func keyStateSelectable(now time.Time, ks *KeyState) bool {
	if ks.Invalid {
		return false
	}
	if now.Before(ks.ExhaustedUntil) {
		return false
	}
	if now.Before(ks.CooldownEnd) {
		return false
	}
	return !ratelimit.SnapshotBlocksKeyAt(ks.RateLimit, now)
}

// KeyPoolNextTransition returns the shortest time until some key may transition
// between blocked and unblocked (cooldown expiry).
// Used by the TUI to refresh the key pool line without polling every frame.
// Returns 0 when there is no known upcoming transition or when total keys <= 1.

func (p *ProviderConfig) KeyPoolNextTransition() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.keyStates) <= 1 {
		return 0
	}
	return p.keyPoolNextTransitionLocked(time.Now())
}

func (p *ProviderConfig) keyPoolNextTransitionLocked(now time.Time) time.Duration {
	d := p.earliestKeyRecoveryLocked(now)
	if d <= 0 {
		return 0
	}
	return d
}

// earliestKeyRecoveryLocked returns the minimum time until any key becomes
// selectable again (cooldown ends). Must hold p.mu.

func (p *ProviderConfig) earliestKeyRecoveryLocked(now time.Time) time.Duration {
	var minD time.Duration
	for _, ks := range p.keyStates {
		if now.Before(ks.CooldownEnd) {
			d := time.Until(ks.CooldownEnd)
			if d > 0 && (minD == 0 || d < minD) {
				minD = d
			}
		}
		if now.Before(ks.ExhaustedUntil) {
			d := time.Until(ks.ExhaustedUntil)
			if d > 0 && (minD == 0 || d < minD) {
				minD = d
			}
		}
	}
	return minD
}
