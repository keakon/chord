package llm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

func classifyFallbackReason(err error) string {
	if err == nil {
		return "error"
	}
	if _, ok := errors.AsType[*AllKeysCoolingError](err); ok {
		return "429"
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		if IsContextLengthExceeded(apiErr) {
			return "context_length_exceeded"
		}
		switch {
		case apiErr.StatusCode == 402:
			return "402"
		case apiErr.StatusCode == 429:
			return "429"
		case apiErr.StatusCode >= 500:
			return "5xx"
		case apiErr.StatusCode == 413:
			return "context_length_exceeded"
		default:
			return "error"
		}
	}
	if isTimeoutLikeError(err) {
		return "timeout"
	}
	return "error"
}

// keySuffix returns the last 4 characters of a key for safe log output.
func keySuffix(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "..." + key[len(key)-4:]
}

func confirmedCodexUsageLimitError(apiErr *APIError) bool {
	if apiErr == nil {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(apiErr.Code))
	msg := strings.ToLower(strings.TrimSpace(apiErr.Message))
	return code == "usage_limit_reached" ||
		code == "usage_limit_exceeded" ||
		code == "insufficient_quota" ||
		code == "insufficient_user_quota" ||
		strings.Contains(msg, "usage limit has been reached") ||
		strings.Contains(msg, "you've hit your usage limit") ||
		strings.Contains(msg, "you have hit your usage limit") ||
		strings.Contains(msg, "exceeded your current quota") ||
		strings.Contains(msg, "insufficient_quota")
}

// confirmedCodexQuotaExhausted reports whether a 429 or explicit Codex usage-limit error
// has one or more exhausted rate-limit windows with a future reset time.
// When confirmed, callers mark the key unavailable until the later reset instead of
// applying a short generic cooldown.
func confirmedCodexQuotaExhausted(provider *ProviderConfig, key string, apiErr *APIError, now time.Time) (primaryResetAt, secondaryResetAt int64, until time.Time, ok bool) {
	if provider == nil || apiErr == nil || (apiErr.StatusCode != 429 && !confirmedCodexUsageLimitError(apiErr)) {
		return 0, 0, time.Time{}, false
	}
	if !provider.usesPresetCodexRateLimitCooldown() || !provider.isOpenAIOAuthKey(key) {
		return 0, 0, time.Time{}, false
	}
	snap := provider.KeySnapshot(key)
	if snap == nil {
		return 0, 0, time.Time{}, false
	}
	primaryResetAt = codexWindowResetMillis(snap.Primary, now)
	secondaryResetAt = codexWindowResetMillis(snap.Secondary, now)
	if primaryResetAt > 0 {
		until = time.UnixMilli(primaryResetAt)
	}
	if secondaryResetAt > 0 {
		reset := time.UnixMilli(secondaryResetAt)
		if until.IsZero() || reset.After(until) {
			until = reset
		}
	}
	if until.IsZero() {
		return 0, 0, time.Time{}, false
	}
	return primaryResetAt, secondaryResetAt, until, true
}

// markKeyCooldownResult describes any key-state changes derived from an API
// error. refreshedKey is populated when OAuth refresh succeeds and mutates the
// in-memory credential slot to a new access token; callers must use that key
// for any subsequent temporary-unavailable marking, otherwise the refreshed slot
// would remain selectable under on_failure sticky rotation.
type markKeyCooldownResult struct {
	cooldownApplied      bool
	oauthRefreshed       bool
	refreshedKey         string
	expiredAccountID     string // non-empty when a codex OAuth refresh token was marked expired
	expiredEmail         string // email from the expired key's JWT, if available
	invalidatedAccountID string // non-empty when a codex OAuth key was invalidated (401/403)
	invalidatedEmail     string // email from the invalidated key's JWT, if available
	deactivatedAccountID string // non-empty when a codex OAuth key was deactivated (401/403)
	deactivatedEmail     string // email from the deactivated key's JWT, if available
}

// isAccountDeactivated reports whether the API error indicates a permanently
// deactivated account (as opposed to a temporary auth failure or proxy error).
func isAccountDeactivated(apiErr *APIError) bool {
	if apiErr.Code == "account_deactivated" {
		return true
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "deactivated") ||
		strings.Contains(msg, "account has been disabled")
}

// isAccountInvalidated reports whether the API error indicates an invalidated
// account that typically requires re-auth (as opposed to a banned/deactivated account).
func isAccountInvalidated(apiErr *APIError) bool {
	code := strings.ToLower(strings.TrimSpace(apiErr.Code))
	if code == "account_invalidated" || code == "token_invalidated" || code == "token_revoked" {
		return true
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "invalidated") ||
		strings.Contains(msg, "revoked") ||
		strings.Contains(msg, "could not parse your authentication token")
}

// applyCodexQuotaOrCooldown marks the key unavailable until the confirmed
// Codex reset window when available, otherwise applies a generic cooldown.
// defaultCooldown is used when the error has no Retry-After hint; maxCooldown
// caps the resulting cooldown (0 disables the cap). Returns
// markKeyCooldownResult so callers can return directly.
func applyCodexQuotaOrCooldown(provider *ProviderConfig, key string, apiErr *APIError, defaultCooldown, maxCooldown time.Duration, quotaLogPrefix, cooldownLogPrefix string) markKeyCooldownResult {
	now := time.Now()
	if primaryResetAt, secondaryResetAt, until, ok := confirmedCodexQuotaExhausted(provider, key, apiErr, now); ok {
		log.Warnf("%s, marking unavailable until reset key_suffix=%v until=%v", quotaLogPrefix, keySuffix(key), until)
		provider.MarkQuotaExhaustedUntil(key, until)
		_ = provider.persistCodexResetHintsForKey(key, primaryResetAt, secondaryResetAt)
		return markKeyCooldownResult{cooldownApplied: true}
	}
	cooldown := apiErr.RetryAfter
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	if maxCooldown > 0 && cooldown > maxCooldown {
		cooldown = maxCooldown
	}
	log.Warnf("%s, marking cooldown key_suffix=%v cooldown=%v", cooldownLogPrefix, keySuffix(key), cooldown)
	provider.MarkCooldown(key, cooldown)
	return markKeyCooldownResult{cooldownApplied: true}
}

// markKeyCooldown checks the error and puts the key into cooldown if the
// error indicates a per-key problem (rate limit, auth failure, permission
// denied). Returns cooldownApplied when MarkCooldown ran with d>0, and
// oauthRefreshed when a 401/403 was handled by a successful token refresh.
// For OAuth keys that receive a 401/403, it first attempts to refresh the
// token; if refresh succeeds no cooldown is applied and refreshedKey contains
// the new access token now stored in the credential slot.
func markKeyCooldown(ctx context.Context, provider *ProviderConfig, key string, err error) markKeyCooldownResult {
	var apiErr *APIError
	if !isAPIError(err, &apiErr) {
		return markKeyCooldownResult{}
	}
	if provider != nil && provider.usesPresetCodexRateLimitCooldown() && confirmedCodexUsageLimitError(apiErr) {
		return applyCodexQuotaOrCooldown(provider, key, apiErr, time.Minute, 0,
			"Codex usage limit reached", "Codex usage limit reached")
	}
	switch apiErr.StatusCode {
	case 400:
		if providerUsesOfficialAPI(provider) || isRequestOrParamError(apiErr.Message) {
			return markKeyCooldownResult{}
		}
		cooldown := apiErr.RetryAfter
		if cooldown <= 0 {
			cooldown = time.Minute
		}
		if cooldown > time.Minute {
			cooldown = time.Minute
		}
		log.Warnf("compatible API key returned 400, marking cooldown key_suffix=%v cooldown=%v", keySuffix(key), cooldown)
		provider.MarkCooldown(key, cooldown)
		return markKeyCooldownResult{cooldownApplied: true}
	case 402, 429:
		return applyCodexQuotaOrCooldown(provider, key, apiErr, time.Second, time.Minute,
			"API key quota exhausted", "API key temporarily unavailable")
	case 401:
		if info := provider.oauthInfoForKey(key); info != nil {
			if isAccountInvalidated(apiErr) {
				log.Warnf("OAuth account invalidated, permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
				provider.MarkInvalidated(key)
				return markKeyCooldownResult{
					cooldownApplied:      true,
					invalidatedAccountID: info.AccountID,
					invalidatedEmail:     info.Email,
				}
			}
			if isAccountDeactivated(apiErr) {
				log.Warnf("OAuth account deactivated, permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
				provider.MarkDeactivated(key)
				return markKeyCooldownResult{
					cooldownApplied:      true,
					deactivatedAccountID: info.AccountID,
					deactivatedEmail:     info.Email,
				}
			}
		}
		// For OAuth keys, a 401 usually means the token expired. Attempt a
		// refresh so the next key-loop iteration can reuse the credential slot
		// with the fresh token.
		if refreshedKey, ok, refreshErr := provider.TryRefreshOAuthKey(ctx, key); ok {
			log.Infof("OAuth token refreshed after 401, key ready for retry key_suffix=%v", keySuffix(key))
			return markKeyCooldownResult{oauthRefreshed: true, refreshedKey: refreshedKey}
		} else if config.IsOAuthCredentialUnrecoverableAfterAccessExpiry(refreshErr) {
			info := provider.oauthInfoForKey(key)
			log.Warnf("OAuth credential unrecoverable after access expiry, permanently removing key key_suffix=%v", keySuffix(key))
			provider.MarkExpired(key)
			result := markKeyCooldownResult{cooldownApplied: true}
			if info != nil {
				result.expiredAccountID = info.AccountID
				result.expiredEmail = info.Email
			}
			return result
		}
		log.Warnf("API key authentication failed, marking cooldown key_suffix=%v", keySuffix(key))
		provider.MarkCooldown(key, time.Minute)
		return markKeyCooldownResult{cooldownApplied: true}
	case 403:
		if info := provider.oauthInfoForKey(key); info != nil {
			if isAccountInvalidated(apiErr) {
				log.Warnf("OAuth account invalidated (403), permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
				provider.MarkInvalidated(key)
				return markKeyCooldownResult{
					cooldownApplied:      true,
					invalidatedAccountID: info.AccountID,
					invalidatedEmail:     info.Email,
				}
			}
			if isAccountDeactivated(apiErr) {
				log.Warnf("OAuth account deactivated (403), permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
				provider.MarkDeactivated(key)
				return markKeyCooldownResult{
					cooldownApplied:      true,
					deactivatedAccountID: info.AccountID,
					deactivatedEmail:     info.Email,
				}
			}
		}
		// Same rationale as 401: OAuth token may have been revoked/expired.
		if refreshedKey, ok, refreshErr := provider.TryRefreshOAuthKey(ctx, key); ok {
			log.Infof("OAuth token refreshed after 403, key ready for retry key_suffix=%v", keySuffix(key))
			return markKeyCooldownResult{oauthRefreshed: true, refreshedKey: refreshedKey}
		} else if config.IsOAuthCredentialUnrecoverableAfterAccessExpiry(refreshErr) {
			info := provider.oauthInfoForKey(key)
			log.Warnf("OAuth credential unrecoverable after access expiry, permanently removing key key_suffix=%v", keySuffix(key))
			provider.MarkExpired(key)
			result := markKeyCooldownResult{cooldownApplied: true}
			if info != nil {
				result.expiredAccountID = info.AccountID
				result.expiredEmail = info.Email
			}
			return result
		}
		log.Warnf("API key permission denied, marking cooldown key_suffix=%v", keySuffix(key))
		provider.MarkCooldown(key, time.Minute)
		return markKeyCooldownResult{cooldownApplied: true}
	default:
		return markKeyCooldownResult{}
	}
}

// isAPIError extracts an APIError from err, returning true if found.
func isAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}
