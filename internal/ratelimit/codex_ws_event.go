package ratelimit

import (
	"encoding/json"
	"strings"
	"time"
)

// ParseCodexRateLimitWebSocketEvent parses a Codex Responses WebSocket JSON frame with
// type "codex.rate_limits" (same shape as codex-rs parse_rate_limit_event).
// Returns nil if the payload is not a recognized rate-limit event or contains no usable data.
func ParseCodexRateLimitWebSocketEvent(payload []byte) *KeyRateLimitSnapshot {
	var root struct {
		Type             string `json:"type"`
		PlanType         string `json:"plan_type"`
		MeteredLimitName string `json:"metered_limit_name"`
		LimitName        string `json:"limit_name"`
		Credits          *struct {
			HasCredits bool    `json:"has_credits"`
			Unlimited  bool    `json:"unlimited"`
			Balance    *string `json:"balance"`
		} `json:"credits"`
		RateLimits *struct {
			Primary   *codexRateLimitEventWindow `json:"primary"`
			Secondary *codexRateLimitEventWindow `json:"secondary"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil
	}
	if root.Type != "codex.rate_limits" {
		return nil
	}
	primary := mapCodexRateLimitEventWindow(root.RateLimits.Primary)
	secondary := mapCodexRateLimitEventWindow(root.RateLimits.Secondary)
	var credits *CreditsSnapshot
	if root.Credits != nil {
		credits = &CreditsSnapshot{HasCredits: root.Credits.HasCredits, Unlimited: root.Credits.Unlimited}
		if root.Credits.Balance != nil {
			credits.Balance = strings.TrimSpace(*root.Credits.Balance)
		}
	}
	if primary == nil && secondary == nil && credits == nil {
		return nil
	}
	limitID := strings.TrimSpace(root.MeteredLimitName)
	if limitID == "" {
		limitID = strings.TrimSpace(root.LimitName)
	}
	if limitID == "" {
		limitID = "codex"
	}
	return &KeyRateLimitSnapshot{
		CapturedAt: time.Now(),
		Primary:    primary,
		Secondary:  secondary,
		Credits:    credits,
		PlanType:   strings.TrimSpace(root.PlanType),
		LimitID:    limitID,
		Source:     SnapshotSourceInlineKey,
	}
}

type codexRateLimitEventWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes *int64  `json:"window_minutes"`
	ResetAt       *int64  `json:"reset_at"`
}

func mapCodexRateLimitEventWindow(w *codexRateLimitEventWindow) *RateLimitWindow {
	if w == nil {
		return nil
	}
	out := &RateLimitWindow{
		UsedPct:       w.UsedPercent,
		WindowMinutes: -1,
	}
	if w.WindowMinutes != nil {
		out.WindowMinutes = *w.WindowMinutes
	}
	if w.ResetAt != nil && *w.ResetAt > 0 {
		out.ResetsAt = time.Unix(*w.ResetAt, 0)
	}
	return out
}
