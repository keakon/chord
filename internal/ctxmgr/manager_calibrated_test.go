package ctxmgr

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// TestEstimateMessagesTokensCalibratedFallback verifies that without any usage
// sample the calibrated estimator falls back to the plain bytes/3 estimate.
func TestEstimateMessagesTokensCalibratedFallback(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 3000)}
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 1000 {
		t.Fatalf("calibrated fallback = %d, want 1000", got)
	}
	if got := m.EstimateBytesForTokensCalibrated(1000); got != 3000 {
		t.Fatalf("tokens->bytes fallback = %d, want 3000", got)
	}
	// nil receiver also falls back.
	if got := (*Manager)(nil).EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 1000 {
		t.Fatalf("nil-calibrated = %d, want 1000", got)
	}
}

// TestEstimateMessagesTokensCalibratedFromUsage verifies that a usage sample
// calibrates the estimate to the reported tokens/bytes ratio (600 tokens over
// 3000 bytes => ratio 0.2 => 600 tokens for 3000 bytes).
func TestEstimateMessagesTokensCalibratedFromUsage(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 3000)}
	m.Append(msg)
	// Report a completion where the prompt (3000 bytes) cost 600 tokens.
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 600})
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 600 {
		t.Fatalf("calibrated = %d, want 600", got)
	}
	if got := m.EstimateBytesForTokensCalibrated(600); got != 3000 {
		t.Fatalf("tokens->bytes = %d, want 3000", got)
	}
}

// TestEstimateMessagesTokensCalibratedAllocsGuard pins the cached-ratio read
// path: after a sample arrives, estimating per message must not allocate
// (previously each call re-sorted a fresh ratio slice).
func TestEstimateMessagesTokensCalibratedAllocsGuard(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 3000)}
	m.Append(msg)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 600})
	msgs := []message.Message{msg}
	allocs := testing.AllocsPerRun(100, func() {
		if got := m.EstimateMessagesTokensCalibrated(msgs); got != 600 {
			t.Fatalf("calibrated = %d, want 600", got)
		}
	})
	if allocs > 0 {
		t.Fatalf("calibrated estimate allocs = %.0f, want 0", allocs)
	}
}

// TestEstimateMessagesTokensCalibratedSurvivesRestore pins the deliberate
// window lifetime: RestoreMessages clears the stale-size fields but keeps the
// calibration window, so a restored session keeps its last valid calibration
// instead of cold-starting.
func TestEstimateMessagesTokensCalibratedSurvivesRestore(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 3000)}
	m.Append(msg)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 600})
	m.RestoreMessages([]message.Message{msg})
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 600 {
		t.Fatalf("calibrated after restore = %d, want 600", got)
	}
}

// TestEstimateMessagesTokensCalibratedUsesMedian verifies the calibration is
// robust against a single outlier sample: with ratios 0.5, 0.5 and 0.05 the
// median (0.5) wins, not the mean (0.35).
func TestEstimateMessagesTokensCalibratedUsesMedian(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 2000)}
	m.Append(msg)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 1000}) // ratio 0.5
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 1000}) // ratio 0.5
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 100})  // ratio 0.05 outlier
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 1000 {
		t.Fatalf("median-calibrated = %d, want 1000", got)
	}
}

// TestEstimateMessagesTokensCalibratedSurvivesClear verifies the calibration
// window survives ClearLastTokenUsage (model switch / context rewrite): the
// ratio stays valid even though the stale size fields for compaction
// triggering are reset.
func TestEstimateMessagesTokensCalibratedSurvivesClear(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 3000)}
	m.Append(msg)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 600})
	m.ClearLastTokenUsage()
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 600 {
		t.Fatalf("calibrated after clear = %d, want 600", got)
	}
}

// TestEstimateMessagesTokensCalibratedWindowBounded verifies the window is
// bounded: after many samples the estimate still reflects the recent ratio.
func TestEstimateMessagesTokensCalibratedWindowBounded(t *testing.T) {
	m := NewManager(8192, 0)
	msg := message.Message{Content: strings.Repeat("x", 1000)}
	m.Append(msg)
	for range 20 {
		m.UpdateFromUsage(message.TokenUsage{InputTokens: 250}) // ratio 0.25
	}
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 250 {
		t.Fatalf("window-calibrated = %d, want 250", got)
	}
}

// TestEstimateMessagesTokensCalibratedClampsOutliers verifies out-of-band
// samples are clamped into the sanity band rather than discarded. Discarding
// them empties a window of image-heavy requests and falls back to bytes/3,
// which is ~6.7x the lower bound and over-contracts every derived budget.
func TestEstimateMessagesTokensCalibratedClampsOutliers(t *testing.T) {
	// Every sample below the lower bound: 1 token per 40 bytes (ratio 0.025).
	m := NewManager(200000, 0)
	msg := message.Message{Content: strings.Repeat("x", 40000)}
	m.Append(msg)
	for range 3 {
		m.UpdateFromUsage(message.TokenUsage{InputTokens: 1000})
	}
	// Clamped to calibrationRatioMin (0.05): 40000 * 0.05 = 2000 tokens.
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 2000 {
		t.Fatalf("all-outlier window = %d, want 2000 (clamped to the lower bound, not bytes/3)", got)
	}

	// A sample above the upper bound clamps down to 1.0 rather than vanishing.
	high := NewManager(8192, 0)
	small := message.Message{Content: strings.Repeat("x", 100)}
	high.Append(small)
	high.UpdateFromUsage(message.TokenUsage{InputTokens: 5000}) // ratio 50
	if got := high.EstimateMessagesTokensCalibrated([]message.Message{small}); got != 100 {
		t.Fatalf("above-bound sample = %d, want 100 (clamped to ratio 1.0)", got)
	}
}

// TestEstimateMessagesTokensCalibratedMixedWindowClamps verifies the median is
// taken over clamped ratios: two below-bound samples plus one in-band sample
// must not let the in-band sample become the median on its own.
func TestEstimateMessagesTokensCalibratedMixedWindowClamps(t *testing.T) {
	m := NewManager(200000, 0)
	msg := message.Message{Content: strings.Repeat("x", 30000)}
	m.Append(msg)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 300})   // ratio 0.01 -> 0.05
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 600})   // ratio 0.02 -> 0.05
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 10000}) // ratio 0.3333 in band
	// Clamped ratios sorted: 0.05, 0.05, 0.3333 -> median 0.05.
	if got := m.EstimateMessagesTokensCalibrated([]message.Message{msg}); got != 1500 {
		t.Fatalf("mixed window = %d, want 1500 (median of clamped ratios)", got)
	}
}
