package ctxmgr

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// The observed baseline is a post-response snapshot: content appended after it
// must not raise the gauge or the trigger, because only provider usage may move
// the effective reading.
func TestObservedBaselineDoesNotGrowWithAppends(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})

	if got := m.ContextUsageState(); got != ContextUsageObserved {
		t.Fatalf("ContextUsageState() = %v, want observed", got)
	}
	if got := m.EffectiveContextTokens(); got != 400 {
		t.Fatalf("EffectiveContextTokens() = %d, want the observed 400", got)
	}
	if m.ShouldAutoCompact() {
		t.Fatal("400 tokens under an 800-token line must not trigger")
	}

	m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 2000)})

	if got := m.EffectiveContextTokens(); got != 400 {
		t.Fatalf("EffectiveContextTokens() after appends = %d, want the observed 400 unchanged", got)
	}
	if m.ShouldAutoCompact() {
		t.Fatal("local bytes appended after the sample must not trigger by themselves")
	}
}

// A model/window change retires the observation for the trigger frame but must
// not blank the gauge: the reading describes conversation content that did not
// change, so the display keeps it (marked stale) until the new window reports
// usage — a switch, or a failing request after it, leaves the gauge intact.
func TestInvalidateSizeObservationKeepsStaleReadingForGauge(t *testing.T) {
	m := NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 900000})
	if !m.ShouldAutoCompact() {
		t.Fatal("precondition: 900000 tokens must cross the 0.65 line")
	}

	m.InvalidateSizeObservation()

	if got := m.ContextUsageState(); got != ContextUsageStale {
		t.Fatalf("ContextUsageState() = %v, want stale", got)
	}
	if got := m.EffectiveContextTokens(); got != 900000 {
		t.Fatalf("EffectiveContextTokens() = %d, want the retired reading 900000 kept for the gauge", got)
	}
	decision := m.AutoCompactDecision()
	if got := decision.UsageState; got != ContextUsageUnknown {
		t.Fatalf("trigger UsageState = %v, want unknown for a stale reading", got)
	}
	if got := decision.EffectiveInputTokens; got != 0 {
		t.Fatalf("trigger EffectiveInputTokens = %d, want 0 for a stale reading", got)
	}
	if decision.ShouldCompact {
		t.Fatal("a stale reading must not arm compaction against the new window's line")
	}

	// Fresh usage from the new window supersedes the stale reading in both
	// frames.
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 800000})
	if got := m.ContextUsageState(); got != ContextUsageObserved {
		t.Fatalf("ContextUsageState() after fresh usage = %v, want observed", got)
	}
	if got := m.EffectiveContextTokens(); got != 800000 {
		t.Fatalf("EffectiveContextTokens() after fresh usage = %d, want 800000", got)
	}
	if !m.ShouldAutoCompact() {
		t.Fatal("fresh usage of 800000 must cross the 0.65 line again")
	}
}

// The frozen estimate is retired the same way: a model change keeps it on the
// gauge as a stale reading while the trigger waits for the new window's usage.
func TestInvalidateSizeObservationKeepsFrozenEstimateAsStale(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})
	m.Append(message.Message{Role: message.RoleTool, Content: strings.Repeat("b", 150)})
	m.UpdateFromUsage(message.TokenUsage{})
	if got := m.EffectiveContextTokens(); got != 1000 {
		t.Fatalf("precondition: frozen estimate = %d, want 1000", got)
	}

	m.InvalidateSizeObservation()

	if got := m.ContextUsageState(); got != ContextUsageStale {
		t.Fatalf("ContextUsageState() = %v, want stale", got)
	}
	if got := m.EffectiveContextTokens(); got != 1000 {
		t.Fatalf("EffectiveContextTokens() = %d, want the frozen estimate 1000 kept for the gauge", got)
	}
	if decision := m.AutoCompactDecision(); decision.ShouldCompact || decision.EffectiveInputTokens != 0 {
		t.Fatalf("stale frozen estimate must not trigger, got %+v", decision)
	}
}

// A durable context rewrite drops the stale reading too: the messages it
// measured no longer exist, so the gauge falls back to unknown until the next
// response reports usage.
func TestClearLastTokenUsageDropsStaleReading(t *testing.T) {
	m := NewManagerWithInputBudget(1000000, 1000000, 0, 0.65)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 900000})
	m.InvalidateSizeObservation()

	m.ClearLastTokenUsage()

	if got := m.ContextUsageState(); got != ContextUsageUnknown {
		t.Fatalf("ContextUsageState() = %v, want unknown after the observation is dropped", got)
	}
	if got := m.EffectiveContextTokens(); got != 0 {
		t.Fatalf("EffectiveContextTokens() = %d, want 0 after the observation is dropped", got)
	}
}

func TestRestoreContextReadingIsDisplayOnly(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 900})
	m.RestoreContextReading(950)
	if got := m.ContextUsageState(); got != ContextUsageStale {
		t.Fatalf("state=%v", got)
	}
	if got := m.EffectiveContextTokens(); got != 950 {
		t.Fatalf("reading=%d", got)
	}
	if d := m.AutoCompactDecision(); d.ShouldCompact || d.EffectiveInputTokens != 0 {
		t.Fatalf("decision=%+v", d)
	}
	m.RestoreContextReading(0)
	if m.ContextUsageState() != ContextUsageUnknown || m.EffectiveContextTokens() != 0 {
		t.Fatal("zero restore retained a sample")
	}
}

// Without a calibration sample a missing-usage response records unknown: there
// is nothing to estimate from, so the gauge displays 0 and the trigger stays
// off even though the durable context is far larger than the line.
func TestMissingUsageWithoutSamplesStaysUnknown(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 40000)}})
	m.UpdateFromUsage(message.TokenUsage{})

	decision := m.AutoCompactDecision()
	if got := decision.UsageState; got != ContextUsageUnknown {
		t.Fatalf("UsageState = %v, want unknown", got)
	}
	if got := decision.EffectiveInputTokens; got != 0 {
		t.Fatalf("EffectiveInputTokens = %d, want 0 for unknown usage", got)
	}
	if got := m.EffectiveContextTokens(); got != 0 {
		t.Fatalf("EffectiveContextTokens() = %d, want 0 for unknown usage", got)
	}
	if got := decision.EstimatedInputTokens; got != 0 {
		t.Fatalf("EstimatedInputTokens = %d, want 0 without a calibration sample", got)
	}
	if decision.ShouldCompact {
		t.Fatal("unknown usage must never trigger automatic compaction")
	}
}
