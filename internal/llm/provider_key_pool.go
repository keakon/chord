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

func (p *ProviderConfig) forEachKeyStateByKeyLocked(key string, fn func(*KeyState)) {
	if key == "" || fn == nil {
		return
	}
	for _, ks := range p.keyStates {
		if ks != nil && ks.Key == key {
			fn(ks)
		}
	}
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
	return !ks.Recovering
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
	p.markCooldownWithModeLocked(ks, d, true)
}

func (p *ProviderConfig) markCooldownWithModeLocked(ks *KeyState, d time.Duration, exponential bool) {
	p.markCooldownWithCapLocked(ks, d, exponential, maxProviderRetryDelay)
}

func (p *ProviderConfig) markCooldownWithCapLocked(ks *KeyState, d time.Duration, exponential bool, cap time.Duration) {
	if ks == nil {
		return
	}
	if d <= 0 {
		ks.CooldownCount = 0
		ks.CooldownEnd = time.Time{}
		ks.cooldownCause = nil
		return
	}
	ks.CooldownCount++
	ks.Recovering = true
	effective := d
	if exponential {
		effective = saturatingDoublingDuration(d, cap, ks.CooldownCount-1)
	}
	end := time.Now().Add(effective)
	if end.After(ks.CooldownEnd) {
		ks.CooldownEnd = end
	}
}

// MarkTransportCooldown paces retries after a transport-level failure that the
// caller handles itself (a preserved stream interruption escalated for
// continuation) rather than retrying in place. It grows with the credential's
// consecutive failures like an ordinary cooldown but saturates at its own cap,
// which is deliberately far below maxProviderRetryDelay: this cooldown throttles
// one caller's restart cadence, and the same ProviderConfig is shared with
// compaction, sub-agents and thinking translation, which must not be starved for a
// minute because one reply kept truncating.
//
// Providers configured without keys carry the wait on the provider itself, so
// the throttle still applies where there is no credential to rotate to.
func (p *ProviderConfig) MarkTransportCooldown(key string, base, cap time.Duration) {
	if base <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.keyStates) == 0 {
		p.keylessCooldownCount++
		effective := saturatingDoublingDuration(base, cap, p.keylessCooldownCount-1)
		if end := time.Now().Add(effective); end.After(p.keylessCooldownEnd) {
			p.keylessCooldownEnd = end
		}
		return
	}
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		ks.TransportFailureCount++
		ks.Recovering = true
		effective := saturatingDoublingDuration(base, cap, ks.TransportFailureCount-1)
		if end := time.Now().Add(effective); end.After(ks.CooldownEnd) {
			ks.CooldownEnd = end
		}
	})
}

// ClearTransportCooldown resets the truncation backoff after a reply completes
// end to end. First visible output is not enough: that is exactly what a
// gateway which truncates every reply produces.
//
// For a keyed provider it clears only the growth counter, leaving any wait
// already in flight to expire like every other cooldown. A keyless provider
// also has its wait cleared: there is no second credential to rotate to, so
// keeping the door shut after the endpoint proved healthy would stall the next
// request for nothing.
func (p *ProviderConfig) ClearTransportCooldown(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.keyStates) == 0 {
		p.keylessCooldownCount = 0
		p.keylessCooldownEnd = time.Time{}
		return
	}
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		ks.TransportFailureCount = 0
	})
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

	burst := max(rpm/5, 5)
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
		// A provider-scoped cooldown still gates the call: with no credential to
		// rotate to, waiting it out is the only way a transport backoff can
		// apply here (see MarkTransportCooldown).
		if wait := time.Until(p.keylessCooldownEnd); wait > 0 {
			p.mu.Unlock()
			return "", false, &AllKeysCoolingError{RetryAfter: wait}
		}
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
		retryAfter, coolingOwner := p.earliestKeyRecoveryLocked(now)
		if retryAfter <= 0 {
			retryAfter = 10 * time.Second
		}
		var cause *keyCooldownCause
		if coolingOwner != nil {
			cause = coolingOwner.cooldownCause
		}
		p.mu.Unlock()
		return "", false, &AllKeysCoolingError{RetryAfter: retryAfter, cause: cause}
	}

	// With an existing access token, let the provider prove whether it still works;
	// refresh only after an auth failure. Select-time refresh is reserved for
	// refresh-only slots that have no access token to try.
	if selectedKS.OAuthInfo != nil && p.oauthRefresher != nil && selectedKS.Key == "" {
		if err := p.refreshOAuthKey(ctx, selectedKS); err != nil {
			if config.IsOAuthCredentialUnrecoverableAfterAccessExpiry(err) {
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
				return "", false, fmt.Errorf("OAuth credential unrecoverable after access token expiry provider=%v: %w", p.name, err)
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
	now := time.Now()
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		p.markTemporaryUnavailableLocked(ks, now, d)
	})
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
	p.forEachKeyStateByKeyLocked(key, p.markRecoveringLocked)
}

// MarkCooldown puts the specified key into cooldown for the given duration.
// The key will not be selected by SelectKey until the cooldown expires.
// If d > 0, the count is incremented and exponential backoff applied (capped at 1min).
// If d == 0, the count is reset.

func (p *ProviderConfig) MarkCooldown(key string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		p.markCooldownLocked(ks, d)
	})
}

// MarkServerDirectedCooldown puts the key into cooldown for exactly the given
// duration: no exponential doubling and no 60s cap. It carries a
// server-directed wait (a Retry-After hint already bounded by
// retry_after_max_s), which must be honored verbatim.

func (p *ProviderConfig) MarkServerDirectedCooldown(key string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		p.markCooldownWithModeLocked(ks, d, false)
	})
}

func (p *ProviderConfig) markRateLimitCooldown(key string, retryAfter time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	applied := false
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		// Server-directed pacing wins: a Retry-After hint (already bounded by
		// retry_after_max_s) applies verbatim whether or not explicit retry
		// pacing is configured. retry_backoff / retry_delay_ms only govern
		// Chord-generated delays when the error carries no hint.
		if retryAfter > 0 {
			p.markCooldownWithModeLocked(ks, retryAfter, false)
			applied = true
			return
		}
		if !p.retryPacingExplicit {
			p.markCooldownWithModeLocked(ks, time.Second, true)
			applied = true
			return
		}
		switch p.retryBackoff {
		case config.RetryBackoffNone:
			p.markRecoveringLocked(ks)
		case config.RetryBackoffFixed:
			p.markCooldownWithModeLocked(ks, p.retryDelay, false)
			applied = true
		default:
			p.markCooldownWithModeLocked(ks, p.retryDelay, true)
			applied = true
		}
	})
	return applied
}

// maxPendingKeyCooldownCauses bounds the per-provider queue of failures waiting
// for a request that can report them: a burst of background failures must not
// grow it without bound.
const maxPendingKeyCooldownCauses = 32

// noteKeyCooldownCause stores the API failure behind a cooldown that was just
// applied, replacing any previous cause on the key, and queues it for the next
// request with a stream callback. It is called for every cooldownApplied
// result, including ones produced by callers without a stream callback, so the
// failure is not lost when no request ever waits the cooldown out.
func (p *ProviderConfig) noteKeyCooldownCause(key, model string, err error, result markKeyCooldownResult) {
	if p == nil || err == nil {
		return
	}
	cause := &keyCooldownCause{
		Err:      err,
		Model:    model,
		Key:      key,
		result:   result,
		provider: p,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	matched := false
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		matched = true
		ks.cooldownCause = cause
	})
	if !matched {
		// A keyless provider (or a key no longer in the pool) has no state to
		// carry the failure and no credential to name, so there is nothing key
		// specific to report later.
		return
	}
	if len(p.pendingCauses) >= maxPendingKeyCooldownCauses {
		p.pendingCauses = p.pendingCauses[1:]
	}
	p.pendingCauses = append(p.pendingCauses, cause)
}

// drainPendingKeyReports replays recorded cooldown causes to a request that can
// surface them in the UI. Callers run it at request entry, so failures recorded
// by callers without a stream callback (background extraction or thinking
// translation) or by requests that never reached a cooling wait are shown
// instead of staying invisible. Each cause is consumed at most once: a
// permanent invalidation replays the key overlay, everything else becomes a
// retry error. Causes are drained even when the cooldown already expired,
// because the error panel is a diagnostic log of what happened, not a snapshot
// of what is still cooling.
func (p *ProviderConfig) drainPendingKeyReports(cb StreamCallback) {
	if p == nil || cb == nil {
		return
	}
	type pendingKeyReport struct {
		cause   *keyCooldownCause
		overlay bool
	}
	var reports []pendingKeyReport
	p.mu.Lock()
	for _, cause := range p.pendingCauses {
		if cause == nil {
			continue
		}
		if cause.result.invalidated || cause.result.deactivated || cause.result.expired {
			if cause.deltasEmitted {
				continue
			}
			cause.deltasEmitted = true
			cause.reported = true
			reports = append(reports, pendingKeyReport{cause: cause, overlay: true})
			continue
		}
		if cause.reported {
			continue
		}
		cause.reported = true
		reports = append(reports, pendingKeyReport{cause: cause})
	}
	p.pendingCauses = nil
	p.mu.Unlock()

	for _, report := range reports {
		if report.overlay {
			emitKeyCooldownDeltas(cb, report.cause.result)
			continue
		}
		providerName, accountID, email := retryErrorFields(p, report.cause.Key)
		emitRetryError(cb, report.cause.Err, providerName, report.cause.Model, maskedKey(report.cause.Key), accountID, email)
	}
}

// markKeyCooldownCauseDeltasEmitted records that the key overlay for a
// permanently invalidated credential already reached the user through a
// callback, so a later drain does not replay it.
func (p *ProviderConfig) markKeyCooldownCauseDeltasEmitted(err error) {
	p.markKeyCooldownCauseByErr(err, func(cause *keyCooldownCause) {
		cause.deltasEmitted = true
	})
}

// claimKeyCooldownCause hands a recorded cause to exactly one reporting
// request. It returns false when the cause was already reported or is no
// longer the cause of any key, so concurrent cooling waits cannot duplicate it.
func (p *ProviderConfig) claimKeyCooldownCause(cause *keyCooldownCause) bool {
	if p == nil || cause == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cause.reported {
		return false
	}
	stillCurrent := false
	for _, ks := range p.keyStates {
		if ks.cooldownCause == cause {
			stillCurrent = true
			break
		}
	}
	if !stillCurrent {
		return false
	}
	cause.reported = true
	return true
}

// markKeyCooldownCauseReported suppresses a later cooling-wait report for the
// failure that was just handed to the user through a retry error delta. The
// cause is matched by error identity rather than by key, because a newer
// failure may have replaced it on the key in the meantime.
func (p *ProviderConfig) markKeyCooldownCauseReported(err error) {
	p.markKeyCooldownCauseByErr(err, func(cause *keyCooldownCause) {
		cause.reported = true
	})
}

// markKeyCooldownCauseByErr applies mark to every recorded cause carrying err
// by identity: the cause attached to a key and the queued copy a drain would
// replay. A newer failure can replace the cause on its key while the request
// that produced this failure is still unwinding, and matching by key would
// then stamp the newer cause and leave this failure to be reported twice.
func (p *ProviderConfig) markKeyCooldownCauseByErr(err error, mark func(*keyCooldownCause)) {
	if p == nil || err == nil || mark == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ks := range p.keyStates {
		if ks != nil && ks.cooldownCause != nil && ks.cooldownCause.Err == err {
			mark(ks.cooldownCause)
		}
	}
	for _, cause := range p.pendingCauses {
		if cause != nil && cause.Err == err {
			mark(cause)
		}
	}
}

// MarkQuotaExhaustedUntil marks a key unavailable until the real provider reset time.
// Unlike MarkCooldown, this does not use exponential backoff or the 1-minute cap.

func (p *ProviderConfig) MarkQuotaExhaustedUntil(key string, until time.Time) {
	if key == "" || until.IsZero() || !until.After(time.Now()) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		p.markQuotaExhaustedLocked(ks, until)
	})
}

// MarkKeySuccess clears soft failure state after a successful request.

func (p *ProviderConfig) MarkKeySuccess(key string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	clearSoftHints := false
	matched := false
	now := time.Now()
	p.forEachKeyStateByKeyLocked(key, func(ks *KeyState) {
		matched = true
		ks.CooldownCount = 0
		if !ks.ExhaustedUntil.After(now) {
			ks.ExhaustedUntil = time.Time{}
		}
		clearSoftHints = clearSoftHints || (ks.OAuthInfo != nil && (ks.OAuthInfo.CodexPrimaryResetAt != 0 || ks.OAuthInfo.CodexSecondaryResetAt != 0))
		ks.cooldownCause = nil
		p.markHealthyLocked(ks)
	})
	p.mu.Unlock()
	if matched && clearSoftHints {
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
	d, _ := p.earliestKeyRecoveryLocked(now)
	if d <= 0 {
		return 0
	}
	return d
}

// earliestKeyRecoveryLocked returns the minimum time until any key becomes
// selectable again (cooldown ends) along with the key that owns this recovery
// instant. Must hold p.mu.

func (p *ProviderConfig) earliestKeyRecoveryLocked(now time.Time) (time.Duration, *KeyState) {
	var minD time.Duration
	var owner *KeyState
	consider := func(ks *KeyState, end time.Time) {
		if !now.Before(end) {
			return
		}
		d := time.Until(end)
		if d <= 0 || (minD > 0 && d >= minD) {
			return
		}
		minD = d
		owner = ks
	}
	for _, ks := range p.keyStates {
		consider(ks, ks.CooldownEnd)
		consider(ks, ks.ExhaustedUntil)
	}
	return minD, owner
}
