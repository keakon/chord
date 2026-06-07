package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

// OAuthRefresher handles on-demand OAuth token refresh.
type OAuthRefresher struct {
	tokenURL       string
	clientID       string
	authConfigPath string
	authStatePath  string
	authConfigMu   *sync.Mutex
	authConfig     *config.AuthConfig
	httpClient     *http.Client // used for token refresh requests; may use proxy
	providerName   string
}

func (r *OAuthRefresher) persistCredentialStatus(match config.OAuthCredentialMatch, status config.OAuthCredentialStatus) error {
	if r == nil || r.authConfig == nil || r.authConfigMu == nil {
		return nil
	}
	updated, _, err := r.mutateCredential(match, func(cred *config.OAuthCredential) (bool, error) {
		if cred.Status == status {
			return false, nil
		}
		cred.Status = status
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("persist oauth credential status: %w", err)
	}
	if updated != nil {
		if err := r.persistOAuthStateStatus(updated, status); err != nil {
			return err
		}
	}
	return nil
}

func (r *OAuthRefresher) persistOAuthStateStatus(cred *config.OAuthCredential, status config.OAuthCredentialStatus) error {
	if r == nil || strings.TrimSpace(r.authStatePath) == "" || cred == nil {
		return nil
	}
	key := config.OAuthStateKey{Provider: r.providerName, AccountUserID: cred.AccountUserID, AccountID: cred.AccountID, Email: cred.Email}
	if strings.TrimSpace(key.AccountUserID) == "" && strings.TrimSpace(cred.Refresh) != "" {
		key.RefreshSHA256 = config.OAuthRefreshStateKey(cred.Refresh)
	}
	if strings.TrimSpace(key.AccountUserID) == "" && strings.TrimSpace(key.RefreshSHA256) == "" {
		return nil
	}
	_, _, _, err := config.UpsertOAuthStateRecord(r.authStatePath, key, func(record *config.OAuthStateRecord) (bool, error) {
		before := *record
		if strings.TrimSpace(key.AccountUserID) != "" {
			record.AccountUserID = key.AccountUserID
			record.AccountID = key.AccountID
		} else {
			record.RefreshSHA256 = key.RefreshSHA256
		}
		record.Email = key.Email
		record.Expires = cred.Expires
		record.Status = status
		record.UpdatedAt = time.Now().UnixMilli()
		return !config.EqualOAuthStateRecord(before, *record), nil
	})
	if err != nil {
		return fmt.Errorf("persist oauth state status: %w", err)
	}
	return nil
}

func (r *OAuthRefresher) persistCodexResetHints(match config.OAuthCredentialMatch, primaryResetAt, secondaryResetAt int64) (*config.OAuthCredential, bool, error) {
	if r == nil || r.authConfig == nil || r.authConfigMu == nil {
		return nil, false, nil
	}
	return r.mutateCredential(match, func(cred *config.OAuthCredential) (bool, error) {
		changed := false
		if cred.CodexPrimaryResetAt != primaryResetAt {
			cred.CodexPrimaryResetAt = primaryResetAt
			changed = true
		}
		if cred.CodexSecondaryResetAt != secondaryResetAt {
			cred.CodexSecondaryResetAt = secondaryResetAt
			changed = true
		}
		return changed, nil
	})
}

func (r *OAuthRefresher) mutateCredential(
	match config.OAuthCredentialMatch,
	mutate func(*config.OAuthCredential) (bool, error),
) (*config.OAuthCredential, bool, error) {
	if r == nil || r.authConfig == nil || r.authConfigMu == nil {
		return nil, false, nil
	}
	if mutate == nil {
		return nil, false, fmt.Errorf("oauth credential mutate func is nil")
	}
	if r.authConfigPath == "" {
		return r.mutateCredentialInMemory(match, mutate)
	}
	auth, updated, changed, err := config.UpdateOAuthCredentialInFile(r.authConfigPath, r.providerName, match, mutate)
	if err != nil {
		return nil, false, err
	}
	r.authConfigMu.Lock()
	// The YAML round-trip strips the "status" field (status is stored in the
	// separate state file, not auth.yaml). Restore status from the state file
	// so the in-memory runtime config stays consistent. For the mutated
	// credential whose status was just changed, also apply the new status
	// directly since the state file may not have been updated yet (the caller
	// writes the state file after mutateCredential returns).
	if r.authStatePath != "" {
		if state, stateErr := config.LoadAuthState(r.authStatePath); stateErr == nil {
			auth = config.MergeAuthConfigWithState(auth, state)
		}
	}
	if updated != nil && updated.Status != "" {
		creds := auth[r.providerName]
		for i := range creds {
			if creds[i].OAuth == nil {
				continue
			}
			if creds[i].OAuth.AccountUserID == updated.AccountUserID || creds[i].OAuth.Access == updated.Access {
				creds[i].OAuth.Status = updated.Status
				break
			}
		}
	}
	*r.authConfig = auth
	r.authConfigMu.Unlock()
	return updated, changed, nil
}

func (r *OAuthRefresher) mutateCredentialInMemory(
	match config.OAuthCredentialMatch,
	mutate func(*config.OAuthCredential) (bool, error),
) (*config.OAuthCredential, bool, error) {
	r.authConfigMu.Lock()
	defer r.authConfigMu.Unlock()

	if match.AccountUserID == "" && match.AccountID == "" && match.Access == "" && match.CredentialIndex == nil {
		return nil, false, fmt.Errorf("oauth credential selector is required for provider %q", r.providerName)
	}
	creds := (*r.authConfig)[r.providerName]
	matchIdx := -1
	accessFallbackIdx := -1
	credentialIndexFallbackIdx := -1
	for i := range creds {
		if creds[i].OAuth == nil {
			continue
		}
		oauth := creds[i].OAuth
		if match.AccountUserID != "" && oauth.AccountUserID == match.AccountUserID {
			if match.Access != "" && oauth.Access == match.Access {
				matchIdx = i
				break
			}
			if match.CredentialIndex != nil && i == *match.CredentialIndex {
				matchIdx = i
				break
			}
			if matchIdx < 0 {
				matchIdx = i
			}
			continue
		}
		if match.AccountID != "" && oauth.AccountID == match.AccountID {
			if match.Access != "" && oauth.Access == match.Access {
				matchIdx = i
				break
			}
			if match.CredentialIndex != nil && i == *match.CredentialIndex {
				matchIdx = i
				break
			}
			if matchIdx < 0 {
				matchIdx = i
			}
			continue
		}
		if accessFallbackIdx < 0 && match.Access != "" && oauth.Access == match.Access {
			accessFallbackIdx = i
		}
		if credentialIndexFallbackIdx < 0 && match.CredentialIndex != nil && i == *match.CredentialIndex {
			credentialIndexFallbackIdx = i
		}
	}
	if matchIdx < 0 {
		if accessFallbackIdx >= 0 {
			matchIdx = accessFallbackIdx
		} else if credentialIndexFallbackIdx >= 0 {
			matchIdx = credentialIndexFallbackIdx
		}
	}
	if matchIdx >= 0 {
		updated := *creds[matchIdx].OAuth
		changed, err := mutate(&updated)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return &updated, false, nil
		}
		creds[matchIdx].OAuth = &updated
		return &updated, true, nil
	}
	return nil, false, fmt.Errorf("oauth credential not found for provider %q", r.providerName)
}

func firstNonEmptyOAuthAccess(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (p *ProviderConfig) stopAuthStateMonitorLocked() {
	if p.authStateMonitor == nil {
		return
	}
	p.authStateMonitor.stop()
	p.authStateMonitor = nil
}

func (p *ProviderConfig) SetOAuthRefresher(tokenURL, clientID, authConfigPath, authStatePath string, authConfig *config.AuthConfig, authConfigMu *sync.Mutex, oauthKeys map[string]OAuthKeySetup, proxyURL string) {
	if tokenURL == "" {
		return
	}
	var httpClient *http.Client
	if proxyURL != "" {
		var clientErr error
		httpClient, clientErr = NewHTTPClientWithProxy(proxyURL, 30*time.Second)
		if clientErr != nil {
			log.Warnf("failed to create OAuth refresh HTTP client with proxy, using default proxy=%v error=%v", proxyURL, clientErr)
		}
	}
	p.oauthRefresher = &OAuthRefresher{
		tokenURL:       tokenURL,
		clientID:       clientID,
		authConfigPath: authConfigPath,
		authStatePath:  authStatePath,
		authConfig:     authConfig,
		authConfigMu:   authConfigMu,
		httpClient:     httpClient,
		providerName:   p.name,
	}
	p.authStatePath = authStatePath
	p.stopAuthStateMonitorLocked()
	if authStatePath != "" {
		p.authStateMonitor = newAuthStateMonitor(authStatePath, p.reloadAuthStateFromMonitor)
		p.authStateMonitor.start()
	}
	p.effectiveProxyURL = proxyURL
	for keySlot, ks := range p.keyStates {
		setup, ok := oauthKeys[ks.Key]
		if !ok && ks.Key == "" && authConfig != nil {
			if creds := (*authConfig)[p.name]; keySlot < len(creds) && creds[keySlot].OAuth != nil && creds[keySlot].OAuth.Refresh != "" {
				setup, ok = oauthKeys[config.OAuthRefreshStateKey(creds[keySlot].OAuth.Refresh)]
			}
		}
		if !ok {
			continue
		}
		ks.OAuthInfo = &OAuthKeyInfo{
			Expires:               setup.Expires,
			CredentialIndex:       setup.CredentialIndex,
			AccountUserID:         setup.AccountUserID,
			AccountID:             setup.AccountID,
			Email:                 setup.Email,
			Access:                firstNonEmptyOAuthAccess(setup.Access, ks.Key),
			RefreshSHA256:         setup.RefreshSHA256,
			Status:                setup.Status,
			CodexPrimaryResetAt:   setup.CodexPrimaryResetAt,
			CodexSecondaryResetAt: setup.CodexSecondaryResetAt,
			StateUpdatedAt:        setup.StateUpdatedAt,
			LastWarmupAt:          setup.LastWarmupAt,
		}
		if setup.RateLimit != nil {
			if p.polledRateLimitByCredIdx == nil {
				p.polledRateLimitByCredIdx = make(map[int]*ratelimit.KeyRateLimitSnapshot)
			}
			p.polledRateLimitByCredIdx[setup.CredentialIndex] = setup.RateLimit
			if p.polledRateLimitSucceededAt == nil {
				p.polledRateLimitSucceededAt = make(map[int]time.Time)
			}
			p.polledRateLimitSucceededAt[setup.CredentialIndex] = time.Now()
		}
		if until := codexSoftCooldownUntilFromMillis(setup.CodexPrimaryResetAt, setup.CodexSecondaryResetAt, time.Now()); !until.IsZero() {
			ks.SoftCooldownUntil = until
		}
		ks.Invalid = !setup.Status.IsValid()
		if ks.Invalid {
			ks.Recovering = false
			ks.CooldownEnd = time.Time{}
			ks.ExhaustedUntil = time.Time{}
		}
	}
	p.loadAuthStateLocked()
}

func (p *ProviderConfig) TryRefreshOAuthKey(ctx context.Context, key string) (string, bool, error) {
	if p.oauthRefresher == nil {
		return "", false, nil
	}
	p.mu.Lock()
	var oauthKS *KeyState
	for _, ks := range p.keyStates {
		if ks.Key == key && ks.OAuthInfo != nil {
			oauthKS = ks
			break
		}
	}
	if oauthKS == nil {
		p.mu.Unlock()
		return "", false, nil
	}
	// refreshOAuthKey expects p.mu to be held and temporarily releases it.
	err := p.refreshOAuthKey(ctx, oauthKS)
	refreshedKey := oauthKS.Key
	p.mu.Unlock()
	if err != nil {
		log.Warnf("OAuth token refresh on auth error failed provider=%v error=%v", p.name, err)
		return "", false, err
	}
	return refreshedKey, true, nil
}

// Preset returns the provider preset name.

func (p *ProviderConfig) MarkInvalidated(key string) {
	p.markInvalid(key, config.OAuthStatusInvalidated)
}

// MarkDeactivated permanently marks an OAuth key as unusable for this session
// and persists that state back to auth.yaml when possible. Unlike MarkCooldown,
// this key will never be selected again and is excluded from the total key count
// shown in the sidebar.

func (p *ProviderConfig) MarkDeactivated(key string) {
	p.markInvalid(key, config.OAuthStatusDeactivated)
}

// MarkExpired permanently marks an OAuth key as expired because its access token
// is no longer usable and the credential cannot recover. The state is persisted
// back to auth.yaml when possible. This key will never be selected again and is
// excluded from the total key count shown in the sidebar.

func (p *ProviderConfig) MarkExpired(key string) {
	p.markInvalid(key, config.OAuthStatusExpired)
}

type invalidOAuthCredentialPersist struct {
	refresher *OAuthRefresher
	match     config.OAuthCredentialMatch
	status    config.OAuthCredentialStatus
}

// markInvalid is the shared implementation for marking a key as permanently invalid.

func (p *ProviderConfig) markInvalid(key string, status config.OAuthCredentialStatus) {
	if key == "" {
		return
	}
	p.mu.Lock()
	persist := p.markInvalidKeyStateLocked(p.keyStateByKeyLocked(key), status)
	p.mu.Unlock()
	p.persistInvalidOAuthCredential(persist)
}

func (p *ProviderConfig) markInvalidKeyStateLocked(ks *KeyState, status config.OAuthCredentialStatus) invalidOAuthCredentialPersist {
	if ks == nil {
		return invalidOAuthCredentialPersist{}
	}
	ks.Invalid = true
	ks.Recovering = false
	ks.CooldownEnd = time.Time{}
	ks.ExhaustedUntil = time.Time{}
	match := config.OAuthCredentialMatch{Access: ks.Key}
	if ks.OAuthInfo != nil {
		ks.OAuthInfo.Status = status
		credentialIndex := ks.OAuthInfo.CredentialIndex
		match.AccountUserID = ks.OAuthInfo.AccountUserID
		match.AccountID = ks.OAuthInfo.AccountID
		match.RefreshSHA256 = ks.OAuthInfo.RefreshSHA256
		match.CredentialIndex = &credentialIndex
	}
	return invalidOAuthCredentialPersist{refresher: p.oauthRefresher, match: match, status: status}
}

func (p *ProviderConfig) persistInvalidOAuthCredential(persist invalidOAuthCredentialPersist) {
	if persist.refresher == nil || (persist.match.AccountUserID == "" && persist.match.AccountID == "" && persist.match.Access == "" && persist.match.RefreshSHA256 == "" && persist.match.CredentialIndex == nil) {
		return
	}
	if err := persist.refresher.persistCredentialStatus(persist.match, persist.status); err != nil {
		log.Warnf("failed to persist invalid OAuth credential status provider=%v status=%v error=%v", p.name, persist.status, err)
	}
}

// ConfirmedKeyCount is retained as a semantic alias for HealthyKeyCount.

func (p *ProviderConfig) oauthInfoForKey(key string) *OAuthKeyInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ks := range p.keyStates {
		if ks.Key == key && ks.OAuthInfo != nil {
			copyInfo := *ks.OAuthInfo
			return &copyInfo
		}
	}
	return nil
}

func (p *ProviderConfig) isOpenAIOAuthKey(key string) bool {
	info := p.oauthInfoForKey(key)
	if info == nil {
		return false
	}
	return p.oauthProfile == config.OAuthProfileOpenAICodex
}

// usesPresetCodexRateLimitCooldown reports whether this provider is configured
// with preset: codex (official ChatGPT/Codex OAuth). Only these providers may
// use x-codex-* rate-limit snapshots when choosing 429 cooldown after Retry-After
// is absent or zero; all other providers fall back to the default duration.

// refreshOAuthKey refreshes the OAuth token for ks.
// Must be called with p.mu held; it temporarily releases p.mu during the HTTP call.

func (p *ProviderConfig) refreshOAuthKey(ctx context.Context, ks *KeyState) error {
	if p.oauthRefresher == nil {
		return nil
	}
	r := p.oauthRefresher
	credIdx := ks.OAuthInfo.CredentialIndex

	// Read current credential under authConfigMu.
	r.authConfigMu.Lock()
	creds := (*r.authConfig)[p.name]
	if credIdx < 0 || credIdx >= len(creds) || creds[credIdx].OAuth == nil {
		r.authConfigMu.Unlock()
		return fmt.Errorf("invalid OAuth credential index %d for provider %q", credIdx, p.name)
	}
	credCopy := *creds[credIdx].OAuth
	r.authConfigMu.Unlock()
	if strings.TrimSpace(credCopy.Refresh) == "" {
		return &config.OAuthRefreshError{Code: "missing_refresh_token", Message: "refresh token is empty"}
	}

	// Release p.mu during the network call to avoid blocking other key selections.
	p.mu.Unlock()
	refreshFn := config.RefreshOAuthToken
	if p.oauthProfile == config.OAuthProfileOpenAICodex {
		refreshFn = config.RefreshOpenAICodexOAuthToken
	}
	newCred, err := refreshFn(ctx, r.httpClient, r.tokenURL, r.clientID, &credCopy)
	p.mu.Lock()

	if err != nil {
		log.Warnf("OAuth token refresh failed provider=%v error=%v", p.name, err)
		return err
	}
	refreshedAccountID := ""
	if strings.TrimSpace(newCred.Access) != "" {
		refreshedAccountID = config.ExtractOAuthAccountIDFromToken(newCred.Access)
	}
	refreshedAccountUserID := ""
	if strings.TrimSpace(newCred.Access) != "" {
		refreshedAccountUserID = config.ExtractOAuthAccountUserIDFromToken(newCred.Access)
	}
	if refreshedAccountUserID == "" {
		return fmt.Errorf("refreshed OAuth access token missing account_user_id provider=%v", p.name)
	}
	if refreshedAccountID == "" {
		return fmt.Errorf("refreshed OAuth access token missing account_id provider=%v", p.name)
	}
	if persistedAccountUserID := strings.TrimSpace(newCred.AccountUserID); persistedAccountUserID != "" && persistedAccountUserID != refreshedAccountUserID {
		return fmt.Errorf("refreshed OAuth access token account_user_id mismatch provider=%v", p.name)
	}
	if persistedAccountID := strings.TrimSpace(newCred.AccountID); persistedAccountID != "" && persistedAccountID != refreshedAccountID {
		return fmt.Errorf("refreshed OAuth access token account_id mismatch provider=%v", p.name)
	}
	newCred.AccountID = refreshedAccountID

	credentialIndex := credIdx
	match := config.OAuthCredentialMatch{AccountUserID: credCopy.AccountUserID, Access: credCopy.Access, CredentialIndex: &credentialIndex}
	preservedPrimaryResetAt := credCopy.CodexPrimaryResetAt
	preservedSecondaryResetAt := credCopy.CodexSecondaryResetAt
	persistedCred, _, persistErr := r.mutateCredential(match, func(cred *config.OAuthCredential) (bool, error) {
		newCopy := *newCred
		newCopy.CodexPrimaryResetAt = cred.CodexPrimaryResetAt
		if newCopy.CodexPrimaryResetAt == 0 {
			newCopy.CodexPrimaryResetAt = preservedPrimaryResetAt
		}
		newCopy.CodexSecondaryResetAt = cred.CodexSecondaryResetAt
		if newCopy.CodexSecondaryResetAt == 0 {
			newCopy.CodexSecondaryResetAt = preservedSecondaryResetAt
		}
		*cred = newCopy
		return true, nil
	})
	if persistErr != nil {
		log.Warnf("failed to persist refreshed OAuth token provider=%v error=%v", p.name, persistErr)
		persistedCred = newCred
	}
	if persistedCred == nil {
		persistedCred = newCred
	}

	// Update in-memory key state.
	ks.Key = persistedCred.Access
	ks.OAuthInfo.Expires = persistedCred.Expires
	if persistedCred.Access != "" {
		ks.OAuthInfo.Expires = config.ExtractOAuthExpiresAtFromToken(persistedCred.Access)
		if ks.OAuthInfo.Expires == 0 {
			ks.OAuthInfo.Expires = persistedCred.Expires
		}
	}
	ks.OAuthInfo.CodexPrimaryResetAt = persistedCred.CodexPrimaryResetAt
	ks.OAuthInfo.CodexSecondaryResetAt = persistedCred.CodexSecondaryResetAt
	ks.SoftCooldownUntil = codexSoftCooldownUntilFromMillis(persistedCred.CodexPrimaryResetAt, persistedCred.CodexSecondaryResetAt, time.Now())
	ks.OAuthInfo.AccountUserID = refreshedAccountUserID
	if persistedCred.AccountID != "" {
		ks.OAuthInfo.AccountID = persistedCred.AccountID
		ks.OAuthInfo.RefreshSHA256 = ""
	}
	if persistedCred.Email != "" {
		ks.OAuthInfo.Email = persistedCred.Email
	}
	if r.authStatePath != "" {
		stateCred := *persistedCred
		stateCred.AccountUserID = refreshedAccountUserID
		if err := r.persistOAuthStateStatus(&stateCred, config.OAuthStatusNormal); err != nil {
			log.Warnf("failed to persist refreshed OAuth state provider=%v error=%v", p.name, err)
		}
	}
	log.Infof("OAuth token refreshed provider=%v", p.name)

	return nil
}
