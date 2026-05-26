package llm

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

func codexSoftCooldownUntilFromMillis(primaryResetAt, secondaryResetAt int64, now time.Time) time.Time {
	var until time.Time
	consider := func(ms int64) {
		if ms <= 0 {
			return
		}
		reset := time.UnixMilli(ms)
		if !reset.After(now) {
			return
		}
		if until.IsZero() || reset.After(until) {
			until = reset
		}
	}
	consider(primaryResetAt)
	consider(secondaryResetAt)
	return until
}

func codexWindowResetMillis(w *ratelimit.RateLimitWindow, now time.Time) int64 {
	if w == nil || w.ResetsAt.IsZero() || !w.ResetsAt.After(now) || w.UsedPercent() < 100 {
		return 0
	}
	return w.ResetsAt.UnixMilli()
}

func (p *ProviderConfig) codexSoftCooldownUntilLocked(now time.Time, ks *KeyState) time.Time {
	if ks == nil {
		return time.Time{}
	}
	if ks.SoftCooldownUntil.After(now) {
		return ks.SoftCooldownUntil
	}
	return time.Time{}
}

func (p *ProviderConfig) codexSnapshotForKeyStateLocked(ks *KeyState) *ratelimit.KeyRateLimitSnapshot {
	if ks == nil {
		return nil
	}
	if ks.OAuthInfo != nil && p.polledRateLimitByCredIdx != nil {
		if snap := p.polledRateLimitByCredIdx[ks.OAuthInfo.CredentialIndex]; snap != nil {
			return snap
		}
	}
	return ks.RateLimit
}

func codexWindowRemainingPct(w *ratelimit.RateLimitWindow) (float64, bool) {
	if w == nil {
		return 0, false
	}
	used := w.UsedPercent()
	if used < 0 {
		return 0, false
	}
	remaining := 100 - used
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return remaining, true
}

func codexHeadroomScore(snap *ratelimit.KeyRateLimitSnapshot) (float64, bool) {
	if snap == nil {
		return 0, false
	}
	pRem, pOK := codexWindowRemainingPct(snap.Primary)
	sRem, sOK := codexWindowRemainingPct(snap.Secondary)
	switch {
	case pOK && sOK:
		if pRem < sRem {
			return pRem, true
		}
		return sRem, true
	case pOK:
		return pRem, true
	case sOK:
		return sRem, true
	default:
		return 0, false
	}
}

func codexKnownWindowRank(snap *ratelimit.KeyRateLimitSnapshot) int {
	if snap == nil {
		return 0
	}
	pRem, pOK := codexWindowRemainingPct(snap.Primary)
	sRem, sOK := codexWindowRemainingPct(snap.Secondary)
	hasPrimary := pOK && pRem > 0
	hasSecondary := sOK && sRem > 0
	switch {
	case hasPrimary && hasSecondary:
		return 3
	case hasPrimary || hasSecondary:
		return 2
	case pOK || sOK:
		return 1
	default:
		return 0
	}
}

func codexSoonestResetAt(snap *ratelimit.KeyRateLimitSnapshot, now time.Time) time.Time {
	if snap == nil {
		return time.Time{}
	}
	var reset time.Time
	consider := func(w *ratelimit.RateLimitWindow) {
		if w == nil || w.ResetsAt.IsZero() || !w.ResetsAt.After(now) {
			return
		}
		if reset.IsZero() || w.ResetsAt.Before(reset) {
			reset = w.ResetsAt
		}
	}
	consider(snap.Primary)
	consider(snap.Secondary)
	return reset
}

func codexCreditsPenalty(snap *ratelimit.KeyRateLimitSnapshot) bool {
	if snap == nil || snap.Credits == nil {
		return false
	}
	return !snap.Credits.Unlimited && !snap.Credits.HasCredits
}

func (p *ProviderConfig) codexSoftCooledLocked(now time.Time, ks *KeyState) bool {
	return p.codexSoftCooldownUntilLocked(now, ks).After(now)
}

func (p *ProviderConfig) codexSmartLessLocked(now time.Time, a, b *KeyState) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	aSoft := p.codexSoftCooledLocked(now, a)
	bSoft := p.codexSoftCooledLocked(now, b)
	if aSoft != bSoft {
		return !aSoft
	}
	aNever := !a.EverSelected
	bNever := !b.EverSelected
	if aNever != bNever {
		return aNever
	}
	aSnap := p.codexSnapshotForKeyStateLocked(a)
	bSnap := p.codexSnapshotForKeyStateLocked(b)
	aRank := codexKnownWindowRank(aSnap)
	bRank := codexKnownWindowRank(bSnap)
	if aRank != bRank {
		return aRank > bRank
	}
	aHeadroom, aKnown := codexHeadroomScore(aSnap)
	bHeadroom, bKnown := codexHeadroomScore(bSnap)
	if aKnown != bKnown {
		return aKnown
	}
	if aKnown && bKnown && aHeadroom != bHeadroom {
		return aHeadroom > bHeadroom
	}
	aReset := codexSoonestResetAt(aSnap, now)
	bReset := codexSoonestResetAt(bSnap, now)
	if !aReset.Equal(bReset) {
		if aReset.IsZero() {
			return false
		}
		if bReset.IsZero() {
			return true
		}
		return aReset.Before(bReset)
	}
	aPenalty := codexCreditsPenalty(aSnap)
	bPenalty := codexCreditsPenalty(bSnap)
	if aPenalty != bPenalty {
		return !aPenalty
	}
	if aSoft && bSoft {
		aUntil := p.codexSoftCooldownUntilLocked(now, a)
		bUntil := p.codexSoftCooldownUntilLocked(now, b)
		if !aUntil.Equal(bUntil) {
			return aUntil.Before(bUntil)
		}
	}
	if !a.LastUsed.Equal(b.LastUsed) {
		return a.LastUsed.Before(b.LastUsed)
	}
	return false
}

func (p *ProviderConfig) persistCodexResetHintsForKey(key string, primaryResetAt, secondaryResetAt int64) bool {
	if key == "" || p.oauthRefresher == nil {
		return false
	}
	p.mu.Lock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil || ks.OAuthInfo == nil {
		p.mu.Unlock()
		return false
	}
	credentialIndex := ks.OAuthInfo.CredentialIndex
	match := config.OAuthCredentialMatch{AccountID: ks.OAuthInfo.AccountID, Access: ks.Key, CredentialIndex: &credentialIndex}
	if primaryResetAt == 0 && secondaryResetAt == 0 {
		ks.SoftCooldownUntil = time.Time{}
	} else if until := codexSoftCooldownUntilFromMillis(primaryResetAt, secondaryResetAt, time.Now()); !until.IsZero() {
		ks.SoftCooldownUntil = until
	}
	if ks.OAuthInfo != nil {
		ks.OAuthInfo.CodexPrimaryResetAt = primaryResetAt
		ks.OAuthInfo.CodexSecondaryResetAt = secondaryResetAt
	}
	p.mu.Unlock()
	updated, changed, err := p.oauthRefresher.persistCodexResetHints(match, primaryResetAt, secondaryResetAt)
	if err != nil {
		log.Warnf("failed to persist codex reset hints provider=%v error=%v", p.name, err)
		return false
	}
	if updated != nil {
		p.mu.Lock()
		if ks := p.keyStateByKeyLocked(key); ks != nil && ks.OAuthInfo != nil {
			ks.OAuthInfo.CodexPrimaryResetAt = updated.CodexPrimaryResetAt
			ks.OAuthInfo.CodexSecondaryResetAt = updated.CodexSecondaryResetAt
			ks.SoftCooldownUntil = codexSoftCooldownUntilFromMillis(updated.CodexPrimaryResetAt, updated.CodexSecondaryResetAt, time.Now())
		}
		p.mu.Unlock()
	}
	return changed
}

func (p *ProviderConfig) clearCodexResetHintsForKey(key string) bool {
	return p.persistCodexResetHintsForKey(key, 0, 0)
}

// Warmup initialises LastUsed for all keys to time.Now(), simulating a first
// call so that cooldown timers start immediately.

func (p *ProviderConfig) CurrentInlineRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inlineDisplaySnap
}

func (p *ProviderConfig) CurrentPolledRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maybeReloadAuthStateLocked()
	key, _, credIdx, ok := p.codexUsagePollAuthLocked()
	_ = key
	if !ok {
		return nil
	}
	if p.polledRateLimitByCredIdx == nil {
		return nil
	}
	return p.polledRateLimitByCredIdx[credIdx]
}

func (p *ProviderConfig) UpdatePolledRateLimitSnapshotForCredentialIndex(credIdx int, snap *ratelimit.KeyRateLimitSnapshot) {
	if snap == nil {
		return
	}
	p.mu.Lock()
	if p.polledRateLimitByCredIdx == nil {
		p.polledRateLimitByCredIdx = make(map[int]*ratelimit.KeyRateLimitSnapshot)
	}
	p.polledRateLimitByCredIdx[credIdx] = snap
	if p.polledRateLimitSucceededAt == nil {
		p.polledRateLimitSucceededAt = make(map[int]time.Time)
	}
	p.polledRateLimitSucceededAt[credIdx] = time.Now()
	cb := p.onPolledUpdate
	p.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// SetOnPolledRateLimitUpdated registers a callback invoked after UpdatePolledRateLimitSnapshotForCredentialIndex
// writes a new polled snapshot. Used by the agent layer to push a RateLimitUpdatedEvent to the TUI.

func (p *ProviderConfig) SetOnPolledRateLimitUpdated(fn func()) {
	p.mu.Lock()
	p.onPolledUpdate = fn
	p.mu.Unlock()
}

func (p *ProviderConfig) usesPresetCodexRateLimitCooldown() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.oauthProfile == config.OAuthProfileOpenAICodex
}

func (p *ProviderConfig) StartCodexRateLimitPolling(fetchFn func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error)) {
	if fetchFn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.oauthProfile != config.OAuthProfileOpenAICodex {
		return
	}
	p.codexPollFetchFn = fetchFn
}

func (p *ProviderConfig) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codexPollFetchFn = nil
	p.polledRateLimitInFlight = make(map[int]bool)
	p.stopAuthStateMonitorLocked()
}

func (p *ProviderConfig) StartCodexWarmup(ctx context.Context) bool {
	if p == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.Lock()
	if p.oauthProfile != config.OAuthProfileOpenAICodex || p.codexPollFetchFn == nil {
		p.mu.Unlock()
		return false
	}
	p.maybeReloadAuthStateLocked()
	now := time.Now()
	candidates := make([]int, 0, len(p.keyStates))
	for i, ks := range p.keyStates {
		if ks == nil || ks.Invalid || ks.OAuthInfo == nil || ks.OAuthInfo.AccountID == "" {
			continue
		}
		if !p.keyStateSelectableLocked(now, ks) {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) < 2 {
		p.mu.Unlock()
		return false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a := p.keyStates[candidates[i]]
		b := p.keyStates[candidates[j]]
		aWarmupAt := int64(0)
		bWarmupAt := int64(0)
		if a != nil && a.OAuthInfo != nil {
			aWarmupAt = a.OAuthInfo.LastWarmupAt
		}
		if b != nil && b.OAuthInfo != nil {
			bWarmupAt = b.OAuthInfo.LastWarmupAt
		}
		if aWarmupAt != bWarmupAt {
			if aWarmupAt == 0 {
				return true
			}
			if bWarmupAt == 0 {
				return false
			}
			return aWarmupAt < bWarmupAt
		}
		return p.codexSmartLessLocked(now, a, b)
	})
	if p.polledRateLimitInFlight == nil {
		p.polledRateLimitInFlight = make(map[int]bool)
	}
	if p.polledRateLimitAttemptedAt == nil {
		p.polledRateLimitAttemptedAt = make(map[int]time.Time)
	}
	if p.polledRateLimitSucceededAt == nil {
		p.polledRateLimitSucceededAt = make(map[int]time.Time)
	}
	p.mu.Unlock()

	go func(providerName string, candidateIdxs []int, ctx context.Context) {
		const successTTL = time.Minute
		const failureBackoff = 30 * time.Second
		for _, slot := range candidateIdxs {
			if ctx.Err() != nil {
				return
			}
			p.mu.Lock()
			ks := p.keyStateBySlotLocked(slot)
			if ks == nil || ks.Invalid || ks.OAuthInfo == nil || ks.OAuthInfo.AccountID == "" {
				p.mu.Unlock()
				continue
			}
			credIdx := ks.OAuthInfo.CredentialIndex
			if credIdx < 0 {
				credIdx = slot
			}
			now := time.Now()
			if p.polledRateLimitInFlight[credIdx] {
				p.mu.Unlock()
				continue
			}
			if lastOK := p.polledRateLimitSucceededAt[credIdx]; !lastOK.IsZero() && now.Sub(lastOK) < successTTL {
				p.mu.Unlock()
				continue
			}
			if lastAttempt := p.polledRateLimitAttemptedAt[credIdx]; !lastAttempt.IsZero() && now.Sub(lastAttempt) < failureBackoff {
				p.mu.Unlock()
				continue
			}
			key := ks.Key
			accountID := ks.OAuthInfo.AccountID
			p.polledRateLimitInFlight[credIdx] = true
			p.polledRateLimitAttemptedAt[credIdx] = now
			p.mu.Unlock()

			snaps, err := FetchCodexUsageSnapshot(ctx, p, key, accountID)
			p.mu.Lock()
			if p.polledRateLimitInFlight != nil {
				delete(p.polledRateLimitInFlight, credIdx)
			}
			p.mu.Unlock()
			if err != nil {
				if p.handleCodexWarmupAuthFailure(ctx, key, err) {
					log.Debugf("codex warmup auth failure handled provider=%v account_id=%v error=%v", providerName, accountID, err)
				} else {
					log.Debugf("codex warmup usage probe failed provider=%v account_id=%v error=%v", providerName, accountID, err)
				}
				continue
			}
			for _, snap := range snaps {
				if snap == nil {
					continue
				}
				if snap.LimitID == "" || snap.LimitID == "codex" {
					p.UpdatePolledRateLimitSnapshotForCredentialIndex(credIdx, snap)
					p.persistAuthStateForKey(key, snap, time.Now())
					p.mu.Lock()
					if ks := p.keyStateBySlotLocked(slot); ks != nil && ks.OAuthInfo != nil {
						ks.OAuthInfo.LastWarmupAt = time.Now().UnixMilli()
					}
					p.mu.Unlock()
					break
				}
			}
		}
	}(p.name, candidates, ctx)

	return true
}

func (p *ProviderConfig) handleCodexWarmupAuthFailure(ctx context.Context, key string, err error) bool {
	var apiErr *APIError
	if !isAPIError(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusUnauthorized && apiErr.StatusCode != http.StatusForbidden {
		return false
	}
	if info := p.oauthInfoForKey(key); info != nil {
		if isAccountInvalidated(apiErr) {
			log.Warnf("codex warmup detected OAuth account invalidated, permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
			p.MarkInvalidated(key)
			return true
		}
		if isAccountDeactivated(apiErr) {
			log.Warnf("codex warmup detected OAuth account deactivated, permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
			p.MarkDeactivated(key)
			return true
		}
	}
	if refreshedKey, ok, refreshErr := p.TryRefreshOAuthKey(ctx, key); ok {
		log.Infof("OAuth token refreshed during codex warmup key_suffix=%v", keySuffix(refreshedKey))
		return true
	} else if config.IsOAuthCredentialUnrecoverableAfterAccessExpiry(refreshErr) {
		log.Warnf("codex warmup detected OAuth credential unrecoverable after access expiry, permanently removing key key_suffix=%v", keySuffix(key))
		p.MarkExpired(key)
		return true
	}
	return false
}

func (p *ProviderConfig) WakeCodexRateLimitPolling() {
	p.mu.Lock()
	if p.oauthProfile != config.OAuthProfileOpenAICodex || p.codexPollFetchFn == nil {
		p.mu.Unlock()
		return
	}
	p.maybeReloadAuthStateLocked()
	key, accountID, credIdx, ok := p.codexUsagePollAuthLocked()
	if !ok {
		p.mu.Unlock()
		return
	}
	if p.polledRateLimitInFlight == nil {
		p.polledRateLimitInFlight = make(map[int]bool)
	}
	if p.polledRateLimitAttemptedAt == nil {
		p.polledRateLimitAttemptedAt = make(map[int]time.Time)
	}
	now := time.Now()
	const successTTL = time.Minute
	const failureBackoff = 30 * time.Second
	if p.polledRateLimitInFlight[credIdx] {
		p.mu.Unlock()
		return
	}
	// Bypass the success TTL once a known reset timestamp has passed; otherwise the
	// UI may keep showing an expired window (e.g. "100%" with no timer) for up to
	// successTTL before the next natural poll trigger.
	force := false
	if p.polledRateLimitByCredIdx != nil {
		if snap := p.polledRateLimitByCredIdx[credIdx]; snap != nil {
			for _, w := range []*ratelimit.RateLimitWindow{snap.Primary, snap.Secondary} {
				if w != nil && !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
					force = true
					break
				}
			}
		}
	}
	if !force {
		if lastOK := p.polledRateLimitSucceededAt[credIdx]; !lastOK.IsZero() && now.Sub(lastOK) < successTTL {
			p.mu.Unlock()
			return
		}
	}
	if lastAttempt := p.polledRateLimitAttemptedAt[credIdx]; !lastAttempt.IsZero() && now.Sub(lastAttempt) < failureBackoff {
		p.mu.Unlock()
		return
	}
	fetchFn := p.codexPollFetchFn
	p.polledRateLimitInFlight[credIdx] = true
	p.polledRateLimitAttemptedAt[credIdx] = now
	p.mu.Unlock()

	go func(providerName string, key string, accountID string, credIdx int, fetchFn func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error)) {
		defer func() {
			p.mu.Lock()
			if p.polledRateLimitInFlight != nil {
				delete(p.polledRateLimitInFlight, credIdx)
			}
			p.mu.Unlock()
		}()
		snaps, err := fetchFn(key, accountID)
		if err != nil {
			if p.handleCodexWarmupAuthFailure(context.Background(), key, err) {
				log.Debugf("codex usage poll auth failure handled provider=%v error=%v", providerName, err)
			} else {
				log.Debugf("codex usage poll failed provider=%v error=%v", providerName, err)
			}
			return
		}
		for _, snap := range snaps {
			if snap == nil {
				continue
			}
			snap.Provider = providerName
			if snap.LimitID == "" || snap.LimitID == "codex" {
				p.UpdatePolledRateLimitSnapshotForCredentialIndex(credIdx, snap)
				p.persistAuthStateForKey(key, snap, time.Time{})
				break
			}
		}
	}(p.name, key, accountID, credIdx, fetchFn)
}

// codexUsagePollAuthLocked returns the currently selected Codex OAuth key and account id.
// It only ever returns the *current* key (no scanning other keys) so refreshes stay aligned
// with codex-rs semantics.

func (p *ProviderConfig) codexUsagePollAuthLocked() (key string, accountID string, credIdx int, ok bool) {
	if p.lastSelectedSlot < 0 {
		return "", "", 0, false
	}
	ks := p.keyStateBySlotLocked(p.lastSelectedSlot)
	if ks == nil || ks.OAuthInfo == nil || ks.Invalid {
		return "", "", 0, false
	}
	credIdx = ks.OAuthInfo.CredentialIndex
	if credIdx < 0 {
		credIdx = p.lastSelectedSlot
	}
	return ks.Key, ks.OAuthInfo.AccountID, credIdx, true
}
