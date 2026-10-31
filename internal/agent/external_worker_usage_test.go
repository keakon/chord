package agent

import (
	"testing"

	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
)

func TestExternalUsageTrackerDeduplicatesRepeatedResponses(t *testing.T) {
	tracker := newExternalUsageTracker()
	// The CLI repeats one response's usage while it streams and in later
	// complete events; the tracker must keep the highest value per message id.
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 6, OutputTokens: 100, CacheReadInputTokens: 8273, CacheCreationInputTokens: 2728})
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 2, OutputTokens: 40, CacheReadInputTokens: 1089, CacheCreationInputTokens: 2393})

	got := tracker.snapshot()
	want := message.TokenUsage{
		InputTokens:      6 + 8273,
		OutputTokens:     100,
		CacheReadTokens:  8273,
		CacheWriteTokens: 2728,
	}
	if got.Tokens != want {
		t.Fatalf("deduplicated usage = %+v, want %+v", got.Tokens, want)
	}
	if got.TotalTokens() != want.InputTokens+want.OutputTokens+want.CacheWriteTokens {
		t.Fatalf("total tokens = %d, want %d", got.TotalTokens(), want.InputTokens+want.OutputTokens+want.CacheWriteTokens)
	}
}

func TestExternalUsageTrackerSumsAcrossResponsesAndTurns(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 10, OutputTokens: 20})
	tracker.observeMessage("msg_2", externalclaude.Usage{InputTokens: 30, OutputTokens: 40, CacheCreationInputTokens: 5})
	tracker.observeMessage("msg_3", externalclaude.Usage{InputTokens: 1, OutputTokens: 2, CacheReadInputTokens: 500})

	got := tracker.snapshot()
	// msg_3's input normalizes to 1 + 500 cache-read tokens.
	if got.Tokens.InputTokens != 541 || got.Tokens.OutputTokens != 62 || got.Tokens.CacheReadTokens != 500 || got.Tokens.CacheWriteTokens != 5 {
		t.Fatalf("summed usage = %+v", got.Tokens)
	}
}

func TestExternalUsageTrackerCountsUnkeyedResponsesSeparately(t *testing.T) {
	tracker := newExternalUsageTracker()
	// Responses without a message id cannot be deduplicated; each one must
	// add to the total instead of collapsing into a single empty key.
	tracker.observeMessage("", externalclaude.Usage{InputTokens: 10, OutputTokens: 20})
	tracker.observeMessage("  ", externalclaude.Usage{InputTokens: 5, OutputTokens: 7})
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 1, OutputTokens: 2})

	got := tracker.snapshot()
	if got.Tokens.InputTokens != 16 || got.Tokens.OutputTokens != 29 {
		t.Fatalf("usage = %+v, want input 16 and output 29", got.Tokens)
	}
}

func TestExternalUsageTrackerCostReplacesWithLatestCumulativeReport(t *testing.T) {
	tracker := newExternalUsageTracker()
	if got := tracker.snapshot(); !got.Zero() {
		t.Fatalf("empty tracker usage = %+v, want zero", got)
	}
	// Each result carries its Claude session's running total, so the latest
	// report supersedes the earlier one instead of adding to it.
	tracker.observeResultCost(0.0005549)
	tracker.observeResultCost(0.0006528)

	got := tracker.snapshot()
	if !got.CostKnown || got.CostUSD != 0.0006528 {
		t.Fatalf("cost = %v known=%v, want 0.0006528 known=true", got.CostUSD, got.CostKnown)
	}
	if got.Zero() {
		t.Fatal("cost-only usage must not be zero")
	}
}

func TestExternalUsageTrackerSeedContinuesAcrossAttempts(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.seed(AgentUsage{
		Tokens:    message.TokenUsage{InputTokens: 100, OutputTokens: 50, CacheWriteTokens: 10},
		CostUSD:   0.5,
		CostKnown: true,
	})
	tracker.observeMessage("msg_new", externalclaude.Usage{InputTokens: 7, OutputTokens: 3})
	// The resumed Claude session reports its own (larger) running total.
	tracker.observeResultCost(0.75)

	got := tracker.snapshot()
	if got.Tokens.InputTokens != 107 || got.Tokens.OutputTokens != 53 || got.Tokens.CacheWriteTokens != 10 {
		t.Fatalf("seeded+new usage = %+v", got.Tokens)
	}
	if !got.CostKnown || got.CostUSD != 0.75 {
		t.Fatalf("seeded cost = %v known=%v, want 0.75 known=true", got.CostUSD, got.CostKnown)
	}
}

// TestExternalUsageTrackerIgnoresZeroedCostReport pins that a zero report does
// not wipe an accumulated cost. Claude Code documents zeroed totals on crash and
// startup-error results, and a result without total_cost_usd decodes as zero;
// the session total itself only ever grows (a resumed session continues from the
// total its transcript saved), so a zero is not a measurement.
func TestExternalUsageTrackerIgnoresZeroedCostReport(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.observeResultCost(0.42)
	tracker.observeResultCost(0)

	got := tracker.snapshot()
	if !got.CostKnown || got.CostUSD != 0.42 {
		t.Fatalf("cost = %v known=%v, want 0.42 known=true", got.CostUSD, got.CostKnown)
	}
}

func TestExternalUsageTrackerSeedIgnoresEmptySnapshot(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.observeResultCost(0.25)
	tracker.seed(AgentUsage{})

	if got := tracker.snapshot(); !got.CostKnown || got.CostUSD != 0.25 {
		t.Fatalf("cost = %v known=%v, want 0.25 known=true", got.CostUSD, got.CostKnown)
	}
}

func TestAgentUsageZero(t *testing.T) {
	if !(AgentUsage{}).Zero() {
		t.Fatal("empty usage must be zero")
	}
	if (AgentUsage{CostKnown: true}).Zero() {
		t.Fatal("cost-known usage must not be zero")
	}
	if (AgentUsage{Tokens: message.TokenUsage{InputTokens: 1}}).Zero() {
		t.Fatal("token-bearing usage must not be zero")
	}
}

func TestExternalUsageTrackerUsesResultUsageForPlaceholderOutput(t *testing.T) {
	tracker := newExternalUsageTracker()
	// Per-step output_tokens is the placeholder reported at message_start; the
	// result carries the turn's real output total.
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 10, OutputTokens: 1, CacheReadInputTokens: 500, CacheCreationInputTokens: 7})
	tracker.observeResultUsage(&externalclaude.Usage{InputTokens: 10, OutputTokens: 300, CacheReadInputTokens: 500, CacheCreationInputTokens: 7}, nil)

	got := tracker.snapshot()
	want := message.TokenUsage{InputTokens: 510, OutputTokens: 300, CacheReadTokens: 500, CacheWriteTokens: 7}
	if got.Tokens != want {
		t.Fatalf("result usage = %+v, want %+v", got.Tokens, want)
	}
}

func TestExternalUsageTrackerModelUsageSupersedesSeedAndResultTotals(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.seed(AgentUsage{
		Tokens:    message.TokenUsage{InputTokens: 100, OutputTokens: 50, CacheWriteTokens: 9},
		CostUSD:   0.5,
		CostKnown: true,
	})
	tracker.observeMessage("msg_old", externalclaude.Usage{InputTokens: 1, OutputTokens: 1})
	tracker.observeResultUsage(&externalclaude.Usage{InputTokens: 1, OutputTokens: 1}, nil)
	tracker.observeResultUsage(&externalclaude.Usage{InputTokens: 10, OutputTokens: 20}, map[string]externalclaude.ModelUsage{
		"sample/main-model": {InputTokens: 10, OutputTokens: 300, CacheReadInputTokens: 500, CacheCreationInputTokens: 7},
		"sample/sub-model":  {InputTokens: 2, OutputTokens: 40, CacheReadInputTokens: 20, CacheCreationInputTokens: 3},
	})

	got := tracker.snapshot()
	// modelUsage is the session-cumulative whole-tree total, so the seeded
	// baseline and earlier result-only totals are already included and must not
	// be added again.
	want := message.TokenUsage{InputTokens: 532, OutputTokens: 340, CacheReadTokens: 520, CacheWriteTokens: 10}
	if got.Tokens != want {
		t.Fatalf("model usage = %+v, want %+v", got.Tokens, want)
	}
	if !got.CostKnown || got.CostUSD != 0.5 {
		t.Fatalf("cost = %v known=%v, want seeded 0.5 known=true", got.CostUSD, got.CostKnown)
	}
}

func TestExternalUsageTrackerSumsResultUsageAcrossTurns(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 10, OutputTokens: 1})
	tracker.observeResultUsage(&externalclaude.Usage{InputTokens: 10, OutputTokens: 100}, nil)
	tracker.observeMessage("msg_2", externalclaude.Usage{InputTokens: 20, OutputTokens: 2})
	tracker.observeResultUsage(&externalclaude.Usage{InputTokens: 20, OutputTokens: 200}, nil)

	got := tracker.snapshot()
	// Each result's usage covers only its turn, so the turn totals add while
	// the assistant placeholders are discarded.
	if got.Tokens.InputTokens != 30 || got.Tokens.OutputTokens != 300 {
		t.Fatalf("turn usage = %+v, want input 30 and output 300", got.Tokens)
	}
}

func TestExternalUsageTrackerZeroedResultKeepsMessageFallback(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.observeMessage("msg_1", externalclaude.Usage{InputTokens: 10, OutputTokens: 1, CacheReadInputTokens: 5})
	// A crashed session can zero both result fields; the assistant messages are
	// then the only token evidence.
	tracker.observeResultUsage(&externalclaude.Usage{}, map[string]externalclaude.ModelUsage{"sample/main-model": {}})

	got := tracker.snapshot()
	if got.Tokens.InputTokens != 15 || got.Tokens.OutputTokens != 1 || got.Tokens.CacheReadTokens != 5 {
		t.Fatalf("fallback usage = %+v, want input 15, output 1, cache read 5", got.Tokens)
	}
}

func TestExternalUsageTrackerAddsCurrentTurnToModelUsage(t *testing.T) {
	tracker := newExternalUsageTracker()
	tracker.observeResultUsage(nil, map[string]externalclaude.ModelUsage{
		"sample/main-model": {InputTokens: 100, OutputTokens: 200},
	})
	tracker.observeMessage("msg_next", externalclaude.Usage{InputTokens: 10, OutputTokens: 1, CacheReadInputTokens: 5})
	tracker.observeResultUsage(&externalclaude.Usage{InputTokens: 10, OutputTokens: 30, CacheReadInputTokens: 5}, nil)

	got := tracker.snapshot()
	// The current turn's result replaces its placeholder messages and adds to
	// the previous session-cumulative snapshot.
	if got.Tokens.InputTokens != 115 || got.Tokens.OutputTokens != 230 || got.Tokens.CacheReadTokens != 5 {
		t.Fatalf("current turn usage = %+v, want input 115, output 230, cache read 5", got.Tokens)
	}
}
