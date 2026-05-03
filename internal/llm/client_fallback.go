package llm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ratelimit"
)

func classifyFallbackReason(err error) string {
	if err == nil {
		return "error"
	}
	if _, ok := errors.AsType[*AllKeysCoolingError](err); ok {
		return "429"
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		switch {
		case apiErr.StatusCode == 429:
			return "429"
		case apiErr.StatusCode >= 500:
			return "5xx"
		case apiErr.StatusCode == 413:
			return "context"
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

// confirmedCodexQuotaExhausted reports whether a 429 with Codex OAuth preset
// has both rate-limit windows at 100% usage, indicating confirmed quota exhaustion.
// When confirmed, callers mark the key unavailable until window reset instead of
// applying a short generic cooldown.
func confirmedCodexQuotaExhausted(provider *ProviderConfig, key string, apiErr *APIError, now time.Time) (time.Time, bool) {
	if provider == nil || apiErr == nil || apiErr.StatusCode != 429 {
		return time.Time{}, false
	}
	if !provider.usesPresetCodexRateLimitCooldown() || !provider.isOpenAIOAuthKey(key) {
		return time.Time{}, false
	}
	snap := provider.KeySnapshot(key)
	if snap == nil {
		return time.Time{}, false
	}
	var until time.Time
	consider := func(w *ratelimit.RateLimitWindow) {
		if w == nil || w.ResetsAt.IsZero() || !w.ResetsAt.After(now) {
			return
		}
		if w.UsedPercent() < 100 {
			return
		}
		if until.IsZero() || w.ResetsAt.After(until) {
			until = w.ResetsAt
		}
	}
	consider(snap.Primary)
	consider(snap.Secondary)
	if until.IsZero() {
		return time.Time{}, false
	}
	return until, true
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
	deactivatedAccountID string // non-empty when a codex OAuth key was put into cooldown due to 401/403
	deactivatedEmail     string // email from the deactivated key's JWT, if available
}

// isAccountDeactivated reports whether the API error indicates a permanently
// deactivated account (as opposed to a temporary auth failure or proxy error).
func isAccountDeactivated(apiErr *APIError) bool {
	if apiErr.Code == "account_deactivated" {
		return true
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "deactivated") || strings.Contains(msg, "account has been disabled")
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
	switch apiErr.StatusCode {
	case 429:
		now := time.Now()
		if until, ok := confirmedCodexQuotaExhausted(provider, key, apiErr, now); ok {
			log.Warnf("API key quota exhausted, marking unavailable until reset key_suffix=%v until=%v", keySuffix(key), until)
			provider.MarkQuotaExhaustedUntil(key, until)
			return markKeyCooldownResult{cooldownApplied: true}
		}
		cooldown := apiErr.RetryAfter
		if cooldown <= 0 {
			cooldown = time.Second
		}
		if cooldown > time.Minute {
			cooldown = time.Minute
		}
		log.Warnf("API key rate limited, marking cooldown key_suffix=%v cooldown=%v", keySuffix(key), cooldown)
		provider.MarkCooldown(key, cooldown)
		return markKeyCooldownResult{cooldownApplied: true}
	case 401:
		// For OAuth keys, a 401 usually means the token expired. Attempt a
		// refresh so the next key-loop iteration can reuse the credential slot
		// with the fresh token.
		if refreshedKey, ok, refreshErr := provider.TryRefreshOAuthKey(ctx, key); ok {
			log.Infof("OAuth token refreshed after 401, key ready for retry key_suffix=%v", keySuffix(key))
			return markKeyCooldownResult{oauthRefreshed: true, refreshedKey: refreshedKey}
		} else if config.IsRefreshTokenInvalid(refreshErr) {
			log.Warnf("OAuth refresh token invalid, permanently removing key key_suffix=%v", keySuffix(key))
			provider.MarkExpired(key)
			return markKeyCooldownResult{cooldownApplied: true}
		}
		if info := provider.oauthInfoForKey(key); info != nil && isAccountDeactivated(apiErr) {
			log.Warnf("OAuth account deactivated, permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
			provider.MarkDeactivated(key)
			return markKeyCooldownResult{
				cooldownApplied:      true,
				deactivatedAccountID: info.AccountID,
				deactivatedEmail:     info.Email,
			}
		}
		log.Warnf("API key authentication failed, marking cooldown key_suffix=%v", keySuffix(key))
		provider.MarkCooldown(key, time.Minute)
		return markKeyCooldownResult{cooldownApplied: true}
	case 403:
		// Same rationale as 401: OAuth token may have been revoked/expired.
		if refreshedKey, ok, refreshErr := provider.TryRefreshOAuthKey(ctx, key); ok {
			log.Infof("OAuth token refreshed after 403, key ready for retry key_suffix=%v", keySuffix(key))
			return markKeyCooldownResult{oauthRefreshed: true, refreshedKey: refreshedKey}
		} else if config.IsRefreshTokenInvalid(refreshErr) {
			log.Warnf("OAuth refresh token invalid, permanently removing key key_suffix=%v", keySuffix(key))
			provider.MarkExpired(key)
			return markKeyCooldownResult{cooldownApplied: true}
		}
		if info := provider.oauthInfoForKey(key); info != nil && isAccountDeactivated(apiErr) {
			log.Warnf("OAuth account deactivated (403), permanently removing key key_suffix=%v code=%v", keySuffix(key), apiErr.Code)
			provider.MarkDeactivated(key)
			return markKeyCooldownResult{
				cooldownApplied:      true,
				deactivatedAccountID: info.AccountID,
				deactivatedEmail:     info.Email,
			}
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
