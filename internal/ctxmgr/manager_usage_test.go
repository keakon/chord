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
