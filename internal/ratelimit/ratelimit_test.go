package ratelimit

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func codexHeader(values map[string]string) http.Header {
	h := http.Header{}
	for k, v := range values {
		h.Set(k, v)
	}
	return h
}

func TestParseCodexHeaders(t *testing.T) {
	resetAt := time.Date(2025, 3, 1, 12, 30, 0, 0, time.UTC).Unix()
	headers := codexHeader(map[string]string{
		"x-codex-primary-used-percent":     "72.5",
		"x-codex-primary-window-minutes":   "300",
		"x-codex-primary-reset-at":         strconv.FormatInt(resetAt, 10),
		"x-codex-secondary-used-percent":   "18",
		"x-codex-secondary-window-minutes": "10080",
		"x-codex-secondary-reset-at":       strconv.FormatInt(resetAt+3600, 10),
		"x-codex-credits-has-credits":      "true",
		"x-codex-credits-unlimited":        "0",
		"x-codex-credits-balance":          " 12.34 ",
	})
	snap := ParseCodexHeaders(headers)
	if snap == nil {
		t.Fatal("ParseCodexHeaders returned nil")
	}
	if snap.Source != SnapshotSourceInlineKey {
		t.Fatalf("Source = %q, want %q", snap.Source, SnapshotSourceInlineKey)
	}
	if snap.Primary == nil || snap.Primary.UsedPct != 72.5 || snap.Primary.WindowMinutes != 300 || snap.Primary.ResetsAt.Unix() != resetAt {
		t.Fatalf("unexpected primary window: %+v", snap.Primary)
	}
	if snap.Secondary == nil || snap.Secondary.UsedPct != 18 || snap.Secondary.WindowMinutes != 10080 || snap.Secondary.ResetsAt.Unix() != resetAt+3600 {
		t.Fatalf("unexpected secondary window: %+v", snap.Secondary)
	}
	if snap.Credits == nil || !snap.Credits.HasCredits || snap.Credits.Unlimited || snap.Credits.Balance != "12.34" {
		t.Fatalf("unexpected credits: %+v", snap.Credits)
	}
}

func TestParseCodexHeadersResetAfterAndInvalidInputs(t *testing.T) {
	t.Run("reset after fallback", func(t *testing.T) {
		before := time.Now()
		snap := ParseCodexHeaders(codexHeader(map[string]string{
			"x-codex-primary-used-percent":        "50",
			"x-codex-primary-window-minutes":      "bad",
			"x-codex-primary-reset-after-seconds": "30",
		}))
		if snap == nil || snap.Primary == nil {
			t.Fatalf("expected primary snapshot, got %+v", snap)
		}
		if snap.Primary.WindowMinutes != -1 {
			t.Fatalf("WindowMinutes = %d, want -1 for invalid header", snap.Primary.WindowMinutes)
		}
		if snap.Primary.ResetsAt.Before(before.Add(25*time.Second)) || snap.Primary.ResetsAt.After(time.Now().Add(35*time.Second)) {
			t.Fatalf("ResetsAt = %v, want about 30s from now", snap.Primary.ResetsAt)
		}
	})

	t.Run("nil for no recognized headers", func(t *testing.T) {
		if got := ParseCodexHeaders(codexHeader(map[string]string{"x-other": "1"})); got != nil {
			t.Fatalf("ParseCodexHeaders = %+v, want nil", got)
		}
	})

	t.Run("invalid percent ignored", func(t *testing.T) {
		if got := ParseCodexHeaders(codexHeader(map[string]string{"x-codex-primary-used-percent": "not-a-number"})); got != nil {
			t.Fatalf("ParseCodexHeaders = %+v, want nil", got)
		}
	})

	t.Run("credits only", func(t *testing.T) {
		snap := ParseCodexHeaders(codexHeader(map[string]string{
			"x-codex-credits-has-credits": "1",
			"x-codex-credits-unlimited":   "false",
		}))
		if snap == nil || snap.Credits == nil || !snap.Credits.HasCredits || snap.Credits.Unlimited {
			t.Fatalf("unexpected credits-only snapshot: %+v", snap)
		}
	})
}

func TestParseBoolish(t *testing.T) {
	tests := []struct {
		in   string
		want bool
		ok   bool
	}{
		{in: "1", want: true, ok: true},
		{in: "true", want: true, ok: true},
		{in: " 0 ", want: false, ok: true},
		{in: "FALSE", want: false, ok: true},
		{in: "yes", want: false, ok: false},
	}
	for _, tc := range tests {
		got, ok := parseBoolish(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseBoolish(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseCodexUsagePayload(t *testing.T) {
	resetAt := time.Date(2025, 3, 1, 12, 30, 0, 0, time.UTC).Unix()
	body := []byte(`{
		"plan_type": "plus",
		"credits": {"has_credits": true, "unlimited": false, "balance": " 42 "},
		"rate_limit": {
			"primary_window": {"used_percent": 75, "limit_window_seconds": 18000, "reset_at": ` + strconv.FormatInt(resetAt, 10) + `},
			"secondary_window": {"used_percent": 25, "limit_window_seconds": 604800, "reset_after_seconds": 60}
		},
		"additional_rate_limits": [
			{"metered_feature": "feature-a", "limit_name": "Feature A", "rate_limit": {"primary_window": {"used_percent": 5, "limit_window_seconds": 3600}}}
		]
	}`)
	snaps, err := ParseCodexUsagePayload(body)
	if err != nil {
		t.Fatalf("ParseCodexUsagePayload: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("len(snaps) = %d, want 2", len(snaps))
	}
	base := snaps[0]
	if base.LimitID != "codex" || base.PlanType != "plus" || base.Source != SnapshotSourcePolledUsage {
		t.Fatalf("unexpected base metadata: %+v", base)
	}
	if base.Primary == nil || base.Primary.UsedPct != 75 || base.Primary.WindowMinutes != 300 || base.Primary.ResetsAt.Unix() != resetAt {
		t.Fatalf("unexpected base primary: %+v", base.Primary)
	}
	if base.Secondary == nil || base.Secondary.UsedPct != 25 || base.Secondary.WindowMinutes != 10080 || base.Secondary.ResetsAt.IsZero() {
		t.Fatalf("unexpected base secondary: %+v", base.Secondary)
	}
	if base.Credits == nil || !base.Credits.HasCredits || base.Credits.Unlimited || base.Credits.Balance != "42" {
		t.Fatalf("unexpected base credits: %+v", base.Credits)
	}
	extra := snaps[1]
	if extra.LimitID != "feature-a" || extra.LimitName != "Feature A" || extra.Credits != nil {
		t.Fatalf("unexpected extra snapshot: %+v", extra)
	}
	if extra.Primary == nil || extra.Primary.WindowMinutes != 60 || extra.Primary.UsedPct != 5 {
		t.Fatalf("unexpected extra primary: %+v", extra.Primary)
	}
}

func TestParseCodexUsagePayloadInvalidJSON(t *testing.T) {
	if _, err := ParseCodexUsagePayload([]byte(`{`)); err == nil {
		t.Fatal("ParseCodexUsagePayload invalid JSON error = nil")
	}
}

func TestRateLimitWindowAccessors(t *testing.T) {
	w := RateLimitWindow{UsedPct: 12.5, ResetsAt: time.Now().Add(50 * time.Millisecond)}
	if got := w.UsedPercent(); got != 12.5 {
		t.Fatalf("UsedPercent = %v, want 12.5", got)
	}
	if got := w.TimeUntilReset(); got <= 0 {
		t.Fatalf("TimeUntilReset = %v, want positive", got)
	}
	if got := (RateLimitWindow{}).TimeUntilReset(); got != 0 {
		t.Fatalf("zero TimeUntilReset = %v, want 0", got)
	}
	if got := (RateLimitWindow{ResetsAt: time.Now().Add(-time.Second)}).TimeUntilReset(); got != 0 {
		t.Fatalf("past TimeUntilReset = %v, want 0", got)
	}
}

func TestSnapshotBlocksKeyAt_doesNotBlockOnPercentHeaders(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	snap := &KeyRateLimitSnapshot{
		Primary: &RateLimitWindow{
			UsedPct:  100,
			ResetsAt: now.Add(5 * time.Minute),
		},
		Secondary: &RateLimitWindow{
			UsedPct:  100,
			ResetsAt: now.Add(10 * time.Minute),
		},
	}
	if SnapshotBlocksKeyAt(snap, now) {
		t.Fatal("used-percent headers must not block key selection without a real API error")
	}
}

func TestKeySnapshotRecoveryDuration_zeroWithoutSnapshotBlocking(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	snap := &KeyRateLimitSnapshot{
		Primary:   &RateLimitWindow{UsedPct: 100, ResetsAt: now.Add(2 * time.Minute)},
		Secondary: &RateLimitWindow{UsedPct: 100, ResetsAt: now.Add(10 * time.Minute)},
	}
	if d := KeySnapshotRecoveryDuration(snap, now); d != 0 {
		t.Fatalf("recovery = %v, want 0 when snapshot does not drive blocking", d)
	}
}
