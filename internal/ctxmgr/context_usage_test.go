package ctxmgr

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestContinuationContextIsNotBillingInput(t *testing.T) {
	manager := NewManager(1000000, 0)
	usage := message.TokenUsage{InputTokens: 40200, OutputTokens: 70000, CacheReadTokens: 38000, InputSemanticsKnown: true, InputIncludesCacheRead: true, Context: &message.ContextTokenUsage{Known: true, PromptTokens: 100, OutputTokens: 70000}}
	manager.UpdateFromUsage(usage)
	if manager.LastInputTokens() != 100 || manager.lastTotalContextTokens != 70100 {
		t.Fatalf("context=%d/%d", manager.LastInputTokens(), manager.lastTotalContextTokens)
	}
	if manager.GetStats().InputTokens != 40200 {
		t.Fatal("billing input changed")
	}
	usage.Context.Known = false
	manager.UpdateFromUsage(usage)
	if manager.LastInputTokens() != 0 || manager.observedValid {
		t.Fatal("unknown context used for calibration")
	}
	if manager.GetStats().InputTokens != 80400 {
		t.Fatal("unknown context lost billing")
	}
	usage.Context = nil
	manager.UpdateFromUsage(usage)
	if manager.LastInputTokens() != 40200 {
		t.Fatal("ordinary semantics changed")
	}
}
