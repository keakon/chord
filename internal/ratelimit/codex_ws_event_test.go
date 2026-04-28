package ratelimit

import (
	"testing"
	"time"
)

func TestParseCodexRateLimitWebSocketEvent(t *testing.T) {
	payload := []byte(`{
		"type": "codex.rate_limits",
		"rate_limits": {
			"primary": {
				"used_percent": 42,
				"window_minutes": 60,
				"reset_at": 1700000000
			},
			"secondary": null
		}
	}`)
	snap := ParseCodexRateLimitWebSocketEvent(payload)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Primary == nil {
		t.Fatal("expected primary window")
	}
	if snap.Primary.UsedPct != 42 {
		t.Fatalf("primary UsedPct = %v, want 42", snap.Primary.UsedPct)
	}
	if snap.Primary.WindowMinutes != 60 {
		t.Fatalf("primary WindowMinutes = %v, want 60", snap.Primary.WindowMinutes)
	}
	wantReset := time.Unix(1700000000, 0)
	if !snap.Primary.ResetsAt.Equal(wantReset) {
		t.Fatalf("primary ResetsAt = %v, want %v", snap.Primary.ResetsAt, wantReset)
	}
	if snap.Secondary != nil {
		t.Fatalf("secondary = %#v, want nil", snap.Secondary)
	}
}

func TestParseCodexRateLimitWebSocketEventIgnoresWrongType(t *testing.T) {
	payload := []byte(`{"type":"response.created","id":"x"}`)
	if ParseCodexRateLimitWebSocketEvent(payload) != nil {
		t.Fatal("expected nil for non codex.rate_limits type")
	}
}

func TestParseCodexRateLimitWebSocketEventBothWindows(t *testing.T) {
	payload := []byte(`{
		"type": "codex.rate_limits",
		"rate_limits": {
			"primary": {"used_percent": 10, "window_minutes": 15},
			"secondary": {"used_percent": 20.5, "window_minutes": 10080}
		}
	}`)
	snap := ParseCodexRateLimitWebSocketEvent(payload)
	if snap == nil || snap.Primary == nil || snap.Secondary == nil {
		t.Fatalf("snapshot = %#v", snap)
	}
	if snap.Secondary.UsedPct != 20.5 {
		t.Fatalf("secondary UsedPct = %v", snap.Secondary.UsedPct)
	}
}
