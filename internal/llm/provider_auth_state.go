package llm

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
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
	if strings.TrimSpace(ks.OAuthInfo.AccountUserID) == "" && strings.TrimSpace(ks.OAuthInfo.RefreshSHA256) == "" {
		return config.OAuthStateKey{}
	}
	return config.OAuthStateKey{
		Provider:      p.name,
		AccountUserID: ks.OAuthInfo.AccountUserID,
		AccountID:     ks.OAuthInfo.AccountID,
		RefreshSHA256: ks.OAuthInfo.RefreshSHA256,
		Email:         ks.OAuthInfo.Email,
	}
}

const missingAuthStateDigest = "<missing>"

func authStateStat(path string) (time.Time, int64, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, 0, missingAuthStateDigest, nil
		}
		return time.Time{}, 0, "", fmt.Errorf("stat auth state: %w", err)
	}
	return info.ModTime(), info.Size(), "", nil
}

func (p *ProviderConfig) loadAuthStateLocked() {
	if strings.TrimSpace(p.authStatePath) == "" {
		return
	}
	state, mtime, size, digest, err := loadAuthStateSnapshot(p.authStatePath)
	if err != nil {
		log.Warnf("failed to load auth state path=%v error=%v", p.authStatePath, err)
		return
	}
	if digest == p.authStateDigest {
		return
	}
	p.authState = state
	p.authStateMTime = mtime
	p.authStateSize = size
	p.authStateDigest = digest
	p.applyAuthStateLocked(state, true)
}

func loadAuthStateSnapshot(path string) (config.AuthStateFile, time.Time, int64, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(config.AuthStateFile), time.Time{}, 0, missingAuthStateDigest, nil
		}
		return nil, time.Time{}, 0, "", fmt.Errorf("read auth state: %w", err)
	}
	state, err := config.ParseAuthState(data)
	if err != nil {
		return nil, time.Time{}, 0, "", err
	}
	mtime, err := config.ReadAuthStateMTime(path)
	if err != nil {
		return nil, time.Time{}, 0, "", err
	}
	return state, mtime, int64(len(data)), fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (p *ProviderConfig) applyAuthStateLocked(state config.AuthStateFile, resetPollTTL bool) {
	p.applyAuthStateLockedWithOptions(state, resetPollTTL, true)
}

func (p *ProviderConfig) applyLoadedAuthStateLocked(resetPollTTL bool) {
	if len(p.authState) == 0 {
		return
	}
	p.applyAuthStateLockedWithOptions(p.authState, resetPollTTL, false)
}

func (p *ProviderConfig) applyAuthStateLockedWithOptions(state config.AuthStateFile, resetPollTTL bool, clearMissing bool) {
	for _, ks := range p.keyStates {
		if ks == nil || ks.OAuthInfo == nil {
			continue
		}
		credIdx := ks.OAuthInfo.CredentialIndex
		if credIdx < 0 {
			credIdx = p.keySlotByStateLocked(ks)
		}
		record, ok := config.FindOAuthStateRecord(state, p.authStateKeyLocked(ks))
		if !ok {
			if clearMissing {
				p.clearPolledRateLimitForCredLocked(credIdx)
			}
			continue
		}
		if record.AccountUserID != "" {
			ks.OAuthInfo.AccountUserID = record.AccountUserID
		}
		if record.AccountID != "" {
			ks.OAuthInfo.AccountID = record.AccountID
		}
		if record.Email != "" {
			ks.OAuthInfo.Email = record.Email
		}
		ks.OAuthInfo.StateUpdatedAt = record.UpdatedAt
		ks.OAuthInfo.LastWarmupAt = record.LastWarmupAt
		if record.Status != "" {
			ks.Invalid = !record.Status.IsValid()
			ks.OAuthInfo.Status = record.Status
		}
		if record.Expires != 0 {
			ks.OAuthInfo.Expires = record.Expires
		}
		if !resetPollTTL {
			continue
		}
		ks.OAuthInfo.CodexPrimaryResetAt = record.CodexPrimaryResetAt
		ks.OAuthInfo.CodexSecondaryResetAt = record.CodexSecondaryResetAt
		ks.SoftCooldownUntil = time.Time{}
		snap := codexSnapshotFromOAuthState(p.name, record)
		if snap == nil {
			if clearMissing {
				p.clearPolledRateLimitForCredLocked(credIdx)
			}
			continue
		}
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

func (p *ProviderConfig) keySlotByStateLocked(target *KeyState) int {
	for i, ks := range p.keyStates {
		if ks == target {
			return i
		}
	}
	return -1
}

func (p *ProviderConfig) clearPolledRateLimitForCredLocked(credIdx int) {
	if credIdx < 0 {
		return
	}
	delete(p.polledRateLimitByCredIdx, credIdx)
	delete(p.polledRateLimitSucceededAt, credIdx)
}

func (p *ProviderConfig) maybeReloadAuthStateLocked() bool {
	return p.maybeReloadAuthStateLockedWithDigestCheck(false)
}

func (p *ProviderConfig) maybeReloadAuthStateLockedWithDigestCheck(forceDigestCheck bool) bool {
	if strings.TrimSpace(p.authStatePath) == "" {
		return false
	}
	mtime, size, statDigest, err := authStateStat(p.authStatePath)
	if err != nil {
		log.Warnf("failed to stat auth state path=%v error=%v", p.authStatePath, err)
		return false
	}
	if statDigest == missingAuthStateDigest {
		if p.authStateDigest == missingAuthStateDigest {
			return false
		}
		p.authState = make(config.AuthStateFile)
		p.authStateMTime = time.Time{}
		p.authStateSize = 0
		p.authStateDigest = missingAuthStateDigest
		p.applyAuthStateLocked(p.authState, true)
		return true
	}
	if !forceDigestCheck && p.authStateDigest != "" && !p.authStateMTime.IsZero() && p.authStateMTime.Equal(mtime) && p.authStateSize == size {
		return false
	}
	state, loadedMTime, loadedSize, digest, err := loadAuthStateSnapshot(p.authStatePath)
	if err != nil {
		log.Warnf("failed to reload auth state path=%v error=%v", p.authStatePath, err)
		return false
	}
	if digest == p.authStateDigest {
		p.authStateMTime = loadedMTime
		p.authStateSize = loadedSize
		return false
	}
	p.authState = state
	p.authStateMTime = loadedMTime
	p.authStateSize = loadedSize
	p.authStateDigest = digest
	p.applyAuthStateLocked(state, true)
	return true
}

func (p *ProviderConfig) currentPolledRateLimitSnapshotLocked() *ratelimit.KeyRateLimitSnapshot {
	if p.lastSelectedSlot < 0 || p.polledRateLimitByCredIdx == nil {
		return nil
	}
	ks := p.keyStateBySlotLocked(p.lastSelectedSlot)
	if ks == nil || ks.OAuthInfo == nil {
		return nil
	}
	credIdx := ks.OAuthInfo.CredentialIndex
	if credIdx < 0 {
		credIdx = p.lastSelectedSlot
	}
	return p.polledRateLimitByCredIdx[credIdx]
}

func codexPolledSnapshotEquivalent(a, b *ratelimit.KeyRateLimitSnapshot) bool {
	if a == nil || b == nil {
		return a == b
	}
	if !a.CapturedAt.Equal(b.CapturedAt) || a.LimitID != b.LimitID || a.LimitName != b.LimitName || a.PlanType != b.PlanType || a.Provider != b.Provider || a.Source != b.Source {
		return false
	}
	if !codexRateLimitWindowEquivalent(a.Primary, b.Primary) || !codexRateLimitWindowEquivalent(a.Secondary, b.Secondary) {
		return false
	}
	return codexCreditsSnapshotEquivalent(a.Credits, b.Credits)
}

func codexRateLimitWindowEquivalent(a, b *ratelimit.RateLimitWindow) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.UsedPercent() == b.UsedPercent() && a.WindowMinutes == b.WindowMinutes && a.ResetsAt.Equal(b.ResetsAt)
}

func codexCreditsSnapshotEquivalent(a, b *ratelimit.CreditsSnapshot) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.HasCredits == b.HasCredits && a.Unlimited == b.Unlimited && strings.TrimSpace(a.Balance) == strings.TrimSpace(b.Balance)
}

func (p *ProviderConfig) reloadAuthStateFromMonitor() {
	p.mu.Lock()
	before := p.currentPolledRateLimitSnapshotLocked()
	changed := p.maybeReloadAuthStateLockedWithDigestCheck(true)
	after := p.currentPolledRateLimitSnapshotLocked()
	currentChanged := changed && !codexPolledSnapshotEquivalent(before, after)
	cb := p.onPolledUpdate
	p.mu.Unlock()
	if currentChanged && cb != nil {
		cb()
	}
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
	expires := ks.OAuthInfo.Expires
	status := config.OAuthStatusNormal
	if ks.Invalid {
		status = ks.OAuthInfo.Status
		if status.IsValid() {
			status = config.OAuthStatusExpired
		}
	}
	p.mu.Unlock()
	if strings.TrimSpace(stateKey.AccountUserID) == "" && strings.TrimSpace(stateKey.RefreshSHA256) == "" {
		return
	}
	state, updated, changed, err := config.UpsertOAuthStateRecord(p.authStatePath, stateKey, func(record *config.OAuthStateRecord) (bool, error) {
		before := *record
		if stateKey.AccountUserID != "" {
			record.AccountUserID = firstNonEmptyOAuthAccess(stateKey.AccountUserID, record.AccountUserID)
			record.AccountID = firstNonEmptyOAuthAccess(stateKey.AccountID, record.AccountID)
		} else {
			record.RefreshSHA256 = firstNonEmptyOAuthAccess(stateKey.RefreshSHA256, record.RefreshSHA256)
		}
		record.Expires = expires
		record.Status = status
		codexSnapshotToOAuthStateRecord(record, snap, warmupAt)
		return !config.EqualOAuthStateRecord(before, *record), nil
	})
	if err != nil {
		log.Warnf("failed to persist auth state provider=%v key_id=%v error=%v", p.name, keyLogID(key), err)
		return
	}
	if !changed || updated == nil {
		return
	}
	mtime, _ := config.ReadAuthStateMTime(p.authStatePath)
	var size int64
	if info, statErr := os.Stat(p.authStatePath); statErr == nil {
		size = info.Size()
	}
	digest := ""
	if _, _, loadedSize, loadedDigest, loadErr := loadAuthStateSnapshot(p.authStatePath); loadErr == nil {
		size = loadedSize
		digest = loadedDigest
	}
	p.mu.Lock()
	p.authState = state
	p.authStateMTime = mtime
	p.authStateSize = size
	p.authStateDigest = digest
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
