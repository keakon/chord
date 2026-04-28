package ratelimit

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitWindow records a single rate-limit window snapshot.
type RateLimitWindow struct {
	UsedPct       float64   // used percentage 0-100 (-1 = unknown)
	WindowMinutes int64     // window size in minutes (-1 = unknown)
	ResetsAt      time.Time // reset time (zero = unknown)
}

// UsedPercent returns the used percentage (0-100), or -1 when unknown.
func (w RateLimitWindow) UsedPercent() float64 {
	return w.UsedPct
}

// TimeUntilReset returns the duration until the window resets (0 if unknown or already reset).
func (w RateLimitWindow) TimeUntilReset() time.Duration {
	if w.ResetsAt.IsZero() {
		return 0
	}
	d := time.Until(w.ResetsAt)
	if d < 0 {
		return 0
	}
	return d
}

// CreditsSnapshot records account credit availability metadata from official
// Codex usage endpoints / websocket frames.
type CreditsSnapshot struct {
	HasCredits bool
	Unlimited  bool
	Balance    string
}

// SnapshotSource identifies how a snapshot was obtained.
type SnapshotSource string

const (
	SnapshotSourceInlineKey   SnapshotSource = "inline_key"
	SnapshotSourcePolledUsage SnapshotSource = "polled_usage"
)

// KeyRateLimitSnapshot is a Codex rate-limit snapshot. It may be request-scoped
// (inline HTTP headers / websocket frames for a specific key) or account-scoped
// (background-polled /wham/usage snapshot).
type KeyRateLimitSnapshot struct {
	CapturedAt time.Time
	Provider   string           // provider name that produced this snapshot
	Primary    *RateLimitWindow // x-codex-primary-* / usage.primary_window
	Secondary  *RateLimitWindow // x-codex-secondary-* / usage.secondary_window

	// Optional official Codex metadata retained for parity with codex-rs.
	LimitID   string
	LimitName string
	Credits   *CreditsSnapshot
	PlanType  string
	Source    SnapshotSource
}

// ParseCodexHeaders parses Codex OAuth rate-limit HTTP response headers.
// Returns nil if no recognized rate-limit headers are present.
func ParseCodexHeaders(headers http.Header) *KeyRateLimitSnapshot {
	parseWindow := func(pctH, windowH, resetAfterH, resetAtH string) *RateLimitWindow {
		pctStr := headers.Get(pctH)
		if pctStr == "" {
			return nil
		}
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			return nil
		}
		w := &RateLimitWindow{UsedPct: pct, WindowMinutes: -1}
		if v := headers.Get(windowH); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				w.WindowMinutes = n
			}
		}
		// Official codex-rs prefers absolute reset-at timestamps when present.
		if v := headers.Get(resetAtH); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				w.ResetsAt = time.Unix(n, 0)
			}
		}
		if w.ResetsAt.IsZero() {
			if v := headers.Get(resetAfterH); v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					w.ResetsAt = time.Now().Add(time.Duration(n) * time.Second)
				}
			}
		}
		return w
	}

	s := &KeyRateLimitSnapshot{
		CapturedAt: time.Now(),
		Primary: parseWindow(
			"x-codex-primary-used-percent",
			"x-codex-primary-window-minutes",
			"x-codex-primary-reset-after-seconds",
			"x-codex-primary-reset-at",
		),
		Secondary: parseWindow(
			"x-codex-secondary-used-percent",
			"x-codex-secondary-window-minutes",
			"x-codex-secondary-reset-after-seconds",
			"x-codex-secondary-reset-at",
		),
		Credits: parseCreditsHeaders(headers),
		Source:  SnapshotSourceInlineKey,
	}

	if s.Primary == nil && s.Secondary == nil && s.Credits == nil {
		return nil
	}
	return s
}

func parseCreditsHeaders(headers http.Header) *CreditsSnapshot {
	hasCreditsStr := strings.TrimSpace(headers.Get("x-codex-credits-has-credits"))
	unlimitedStr := strings.TrimSpace(headers.Get("x-codex-credits-unlimited"))
	balance := strings.TrimSpace(headers.Get("x-codex-credits-balance"))
	if hasCreditsStr == "" || unlimitedStr == "" {
		return nil
	}
	hasCredits, ok := parseBoolish(hasCreditsStr)
	if !ok {
		return nil
	}
	unlimited, ok := parseBoolish(unlimitedStr)
	if !ok {
		return nil
	}
	return &CreditsSnapshot{HasCredits: hasCredits, Unlimited: unlimited, Balance: balance}
}

func parseBoolish(s string) (bool, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "1", "true":
		return true, true
	case "0", "false":
		return false, true
	default:
		return false, false
	}
}

// ParseCodexUsagePayload parses the official Codex /wham/usage (or /api/codex/usage)
// payload and returns all snapshots. The first snapshot is always the default
// "codex" bucket, matching codex-rs behavior.
func ParseCodexUsagePayload(body []byte) ([]*KeyRateLimitSnapshot, error) {
	type usageWindow struct {
		UsedPercent        float64 `json:"used_percent"`
		LimitWindowSeconds int64   `json:"limit_window_seconds"`
		ResetAfterSeconds  int64   `json:"reset_after_seconds"`
		ResetAt            int64   `json:"reset_at"`
	}
	type usageRateLimit struct {
		PrimaryWindow   *usageWindow `json:"primary_window"`
		SecondaryWindow *usageWindow `json:"secondary_window"`
	}
	type usageCredits struct {
		HasCredits bool    `json:"has_credits"`
		Unlimited  bool    `json:"unlimited"`
		Balance    *string `json:"balance"`
	}
	type additionalRateLimit struct {
		LimitName      string          `json:"limit_name"`
		MeteredFeature string          `json:"metered_feature"`
		RateLimit      *usageRateLimit `json:"rate_limit"`
	}
	type usagePayload struct {
		PlanType             string                `json:"plan_type"`
		RateLimit            *usageRateLimit       `json:"rate_limit"`
		Credits              *usageCredits         `json:"credits"`
		AdditionalRateLimits []additionalRateLimit `json:"additional_rate_limits"`
	}

	var payload usagePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	makeWindow := func(in *usageWindow) *RateLimitWindow {
		if in == nil {
			return nil
		}
		w := &RateLimitWindow{UsedPct: in.UsedPercent, WindowMinutes: -1}
		if in.LimitWindowSeconds > 0 {
			w.WindowMinutes = (in.LimitWindowSeconds + 59) / 60
		}
		if in.ResetAt > 0 {
			w.ResetsAt = time.Unix(in.ResetAt, 0)
		} else if in.ResetAfterSeconds > 0 {
			w.ResetsAt = time.Now().Add(time.Duration(in.ResetAfterSeconds) * time.Second)
		}
		return w
	}
	makeCredits := func(in *usageCredits) *CreditsSnapshot {
		if in == nil {
			return nil
		}
		out := &CreditsSnapshot{HasCredits: in.HasCredits, Unlimited: in.Unlimited}
		if in.Balance != nil {
			out.Balance = strings.TrimSpace(*in.Balance)
		}
		return out
	}
	makeSnapshot := func(limitID, limitName string, rateLimit *usageRateLimit, credits *usageCredits) *KeyRateLimitSnapshot {
		var primary, secondary *RateLimitWindow
		if rateLimit != nil {
			primary = makeWindow(rateLimit.PrimaryWindow)
			secondary = makeWindow(rateLimit.SecondaryWindow)
		}
		return &KeyRateLimitSnapshot{
			CapturedAt: time.Now(),
			Primary:    primary,
			Secondary:  secondary,
			LimitID:    strings.TrimSpace(limitID),
			LimitName:  strings.TrimSpace(limitName),
			Credits:    makeCredits(credits),
			PlanType:   strings.TrimSpace(payload.PlanType),
			Source:     SnapshotSourcePolledUsage,
		}
	}

	out := make([]*KeyRateLimitSnapshot, 0, 1+len(payload.AdditionalRateLimits))
	out = append(out, makeSnapshot("codex", "", payload.RateLimit, payload.Credits))
	for _, extra := range payload.AdditionalRateLimits {
		out = append(out, makeSnapshot(extra.MeteredFeature, extra.LimitName, extra.RateLimit, nil))
	}
	return out, nil
}

// SnapshotBlocksKeyAt reports whether the rate-limit snapshot should exclude the
// key from selection. We intentionally never return true based on x-codex-*
// used-percent alone: those values can show 100% while the server still accepts
// requests (rounding / accounting mismatch). Key rotation is driven by real
// HTTP failures (e.g. 429) and llm.ProviderConfig.MarkCooldown, not by
// preemptive client-side quota inference. Snapshots remain for UI; for 429
// cooldown when Retry-After is missing, only preset: codex consults them (see
// llm.markKeyCooldown).
func SnapshotBlocksKeyAt(snap *KeyRateLimitSnapshot, now time.Time) bool {
	_ = snap
	_ = now
	return false
}

// KeySnapshotRecoveryDuration returns how long until snapshot-based blocking
// may clear. With SnapshotBlocksKeyAt always false, this is always zero; kept
// for a stable API and for callers that pair with SnapshotBlocksKeyAt.
func KeySnapshotRecoveryDuration(snap *KeyRateLimitSnapshot, now time.Time) time.Duration {
	_ = snap
	_ = now
	return 0
}
