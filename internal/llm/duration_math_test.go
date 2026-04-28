package llm

import (
	"testing"
	"time"
)

func TestDurationFromPositiveSecondsClamped(t *testing.T) {
	if got := durationFromPositiveSecondsClamped(0, 0); got != 0 {
		t.Fatalf("durationFromPositiveSecondsClamped(0, 0) = %v, want 0", got)
	}
	if got := durationFromPositiveSecondsClamped(120, time.Minute); got != time.Minute {
		t.Fatalf("durationFromPositiveSecondsClamped(120, 1m) = %v, want 1m", got)
	}
	if got := durationFromPositiveSecondsClamped(maxSafeDurationSeconds+1, 0); got != maxTimeDuration {
		t.Fatalf("durationFromPositiveSecondsClamped(max+1, 0) = %v, want %v", got, maxTimeDuration)
	}
}

func TestSaturatingDoublingDuration(t *testing.T) {
	if got := saturatingDoublingDuration(time.Second, time.Minute, 5); got != 32*time.Second {
		t.Fatalf("saturatingDoublingDuration(1s, 1m, 5) = %v, want 32s", got)
	}
	if got := saturatingDoublingDuration(time.Second, time.Minute, 80); got != time.Minute {
		t.Fatalf("saturatingDoublingDuration(1s, 1m, 80) = %v, want 1m", got)
	}
}
