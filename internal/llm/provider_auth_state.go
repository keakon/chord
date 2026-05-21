package llm

import (
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

func (p *ProviderConfig) authStateKeyLocked(ks *KeyState) config.OAuthStateKey {
	if ks == nil || ks.OAuthInfo == nil {
		return config.OAuthStateKey{}
	}
	return config.OAuthStateKey{
		Provider:  p.name,
		AccountID: ks.OAuthInfo.AccountID,
		Email:     ks.OAuthInfo.Email,
		Access:    firstNonEmptyOAuthAccess(ks.OAuthInfo.Access, ks.Key),
	}
}

func (p *ProviderConfig) loadAuthStateLocked() {
	if strings.TrimSpace(p.authStatePath) == "" {
		return
	}
	mtime, err := config.ReadAuthStateMTime(p.authStatePath)
	if err != nil {
		log.Warnf("failed to stat auth state path=%v error=%v", p.authStatePath, err)
		return
	}
	if !mtime.IsZero() && !p.authStateMTime.IsZero() && !mtime.After(p.authStateMTime) {
		return
	}
	state, err := config.LoadAuthState(p.authStatePath)
	if err != nil {
		log.Warnf("failed to load auth state path=%v error=%v", p.authStatePath, err)
		return
	}
	p.authState = state
	p.authStateMTime = mtime
	p.applyAuthStateLocked(state, true)
}

func (p *ProviderConfig) applyAuthStateLocked(state config.AuthStateFile, resetPollTTL bool) {
	for _, ks := range p.keyStates {
		if ks == nil || ks.OAuthInfo == nil {
			continue
		}
		record, ok := config.FindOAuthStateRecord(state, p.authStateKeyLocked(ks))
		if !ok {
			continue
		}
		if record.AccountID != "" {
			ks.OAuthInfo.AccountID = record.AccountID
		}
		if record.Email != "" {
			ks.OAuthInfo.Email = record.Email
		}
		if record.Access != "" {
			ks.OAuthInfo.Access = record.Access
		}
		ks.OAuthInfo.StateUpdatedAt = record.UpdatedAt
		ks.OAuthInfo.LastWarmupAt = record.LastWarmupAt
		if record.Status != "" {
			ks.Invalid = !record.Status.IsValid()
			ks.OAuthInfo.Status = record.Status
		}
		ks.OAuthInfo.CodexPrimaryResetAt = record.CodexPrimaryResetAt
		ks.OAuthInfo.CodexSecondaryResetAt = record.CodexSecondaryResetAt
		ks.SoftCooldownUntil = time.Time{}
		if snap := codexSnapshotFromOAuthState(p.name, record); snap != nil {
			credIdx := ks.OAuthInfo.CredentialIndex
			if credIdx < 0 {
				continue
			}
			if p.polledRateLimitByCredIdx == nil {
				p.polledRateLimitByCredIdx = make(map[int]*ratelimit.KeyRateLimitSnapshot)
			}
			prev := p.polledRateLimitByCredIdx[credIdx]
			p.polledRateLimitByCredIdx[credIdx] = snap
			if resetPollTTL && (prev == nil || prev.CapturedAt.Before(snap.CapturedAt)) {
				if p.polledRateLimitSucceededAt == nil {
					p.polledRateLimitSucceededAt = make(map[int]time.Time)
				}
				p.polledRateLimitSucceededAt[credIdx] = time.Now()
			}
		}
	}
}

func (p *ProviderConfig) maybeReloadAuthStateLocked() {
	if strings.TrimSpace(p.authStatePath) == "" {
		return
	}
	mtime, err := config.ReadAuthStateMTime(p.authStatePath)
	if err != nil {
		return
	}
	if mtime.IsZero() || (!p.authStateMTime.IsZero() && !mtime.After(p.authStateMTime)) {
		return
	}
	state, err := config.LoadAuthState(p.authStatePath)
	if err != nil {
		log.Warnf("failed to reload auth state path=%v error=%v", p.authStatePath, err)
		return
	}
	p.authState = state
	p.authStateMTime = mtime
	p.applyAuthStateLocked(state, true)
}

func (p *ProviderConfig) persistAuthStateForKey(key string, snap *ratelimit.KeyRateLimitSnapshot, warmupAt time.Time) {
	if strings.TrimSpace(p.authStatePath) == "" || snap == nil {
		return
	}
	p.mu.Lock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil || ks.OAuthInfo == nil {
		p.mu.Unlock()
		return
	}
	stateKey := p.authStateKeyLocked(ks)
	status := config.OAuthStatusNormal
	if ks.Invalid {
		status = ks.OAuthInfo.Status
		if status.IsValid() {
			status = config.OAuthStatusExpired
		}
	}
	p.mu.Unlock()
	state, updated, changed, err := config.UpsertOAuthStateRecord(p.authStatePath, stateKey, func(record *config.OAuthStateRecord) (bool, error) {
		before := *record
		record.AccountID = firstNonEmptyOAuthAccess(stateKey.AccountID, record.AccountID)
		record.Email = firstNonEmptyOAuthAccess(stateKey.Email, record.Email)
		record.Access = firstNonEmptyOAuthAccess(stateKey.Access, record.Access)
		record.Status = status
		codexSnapshotToOAuthStateRecord(record, snap, warmupAt)
		return !config.EqualOAuthStateRecord(before, *record), nil
	})
	if err != nil {
		log.Warnf("failed to persist auth state provider=%v key_suffix=%v error=%v", p.name, keySuffix(key), err)
		return
	}
	if !changed || updated == nil {
		return
	}
	mtime, _ := config.ReadAuthStateMTime(p.authStatePath)
	p.mu.Lock()
	p.authState = state
	p.authStateMTime = mtime
	p.applyAuthStateLocked(state, false)
	p.mu.Unlock()
}

func codexSnapshotFromOAuthState(provider string, record config.OAuthStateRecord) *ratelimit.KeyRateLimitSnapshot {
	if record.CodexPrimaryWindowMin == 0 && record.CodexPrimaryResetAt == 0 && record.CodexSecondaryWindowMin == 0 && record.CodexSecondaryResetAt == 0 && record.CodexHasCredits == nil && record.CodexUnlimited == nil && strings.TrimSpace(record.CodexBalance) == "" && record.CodexPrimaryUsedPct == 0 && record.CodexSecondaryUsedPct == 0 {
		return nil
	}
	snap := &ratelimit.KeyRateLimitSnapshot{
		Provider:   provider,
		CapturedAt: time.UnixMilli(record.UpdatedAt),
		Source:     ratelimit.SnapshotSourcePolledUsage,
	}
	if record.CodexPrimaryWindowMin != 0 || record.CodexPrimaryResetAt != 0 || record.CodexPrimaryUsedPct != 0 {
		snap.Primary = &ratelimit.RateLimitWindow{UsedPct: record.CodexPrimaryUsedPct, WindowMinutes: record.CodexPrimaryWindowMin}
		if record.CodexPrimaryResetAt > 0 {
			snap.Primary.ResetsAt = time.UnixMilli(record.CodexPrimaryResetAt)
		}
	}
	if record.CodexSecondaryWindowMin != 0 || record.CodexSecondaryResetAt != 0 || record.CodexSecondaryUsedPct != 0 {
		snap.Secondary = &ratelimit.RateLimitWindow{UsedPct: record.CodexSecondaryUsedPct, WindowMinutes: record.CodexSecondaryWindowMin}
		if record.CodexSecondaryResetAt > 0 {
			snap.Secondary.ResetsAt = time.UnixMilli(record.CodexSecondaryResetAt)
		}
	}
	if record.CodexHasCredits != nil || record.CodexUnlimited != nil || strings.TrimSpace(record.CodexBalance) != "" {
		credits := &ratelimit.CreditsSnapshot{Balance: strings.TrimSpace(record.CodexBalance)}
		if record.CodexHasCredits != nil {
			credits.HasCredits = *record.CodexHasCredits
		}
		if record.CodexUnlimited != nil {
			credits.Unlimited = *record.CodexUnlimited
		}
		snap.Credits = credits
	}
	if snap.CapturedAt.IsZero() {
		snap.CapturedAt = time.Now()
	}
	return snap
}

func codexSnapshotToOAuthStateRecord(record *config.OAuthStateRecord, snap *ratelimit.KeyRateLimitSnapshot, warmupAt time.Time) {
	if record == nil || snap == nil {
		return
	}
	if !snap.CapturedAt.IsZero() {
		record.UpdatedAt = snap.CapturedAt.UnixMilli()
	} else {
		record.UpdatedAt = time.Now().UnixMilli()
	}
	if !warmupAt.IsZero() {
		record.LastWarmupAt = warmupAt.UnixMilli()
	}
	if snap.Primary != nil {
		record.CodexPrimaryUsedPct = snap.Primary.UsedPercent()
		record.CodexPrimaryWindowMin = snap.Primary.WindowMinutes
		if !snap.Primary.ResetsAt.IsZero() {
			record.CodexPrimaryResetAt = snap.Primary.ResetsAt.UnixMilli()
		} else {
			record.CodexPrimaryResetAt = 0
		}
	}
	if snap.Secondary != nil {
		record.CodexSecondaryUsedPct = snap.Secondary.UsedPercent()
		record.CodexSecondaryWindowMin = snap.Secondary.WindowMinutes
		if !snap.Secondary.ResetsAt.IsZero() {
			record.CodexSecondaryResetAt = snap.Secondary.ResetsAt.UnixMilli()
		} else {
			record.CodexSecondaryResetAt = 0
		}
	}
	if snap.Credits != nil {
		hasCredits := snap.Credits.HasCredits
		unlimited := snap.Credits.Unlimited
		record.CodexHasCredits = &hasCredits
		record.CodexUnlimited = &unlimited
		record.CodexBalance = strings.TrimSpace(snap.Credits.Balance)
	}
}
