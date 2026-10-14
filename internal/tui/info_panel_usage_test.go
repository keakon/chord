package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestUsageUpdatedEventInvalidatesInfoPanelCache(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 1_000
	backend.usage.InputTokens = 1_000
	m := NewModel(backend)

	first := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(first, "1.0k") {
		t.Fatalf("initial info panel should include initial token usage, got %q", first)
	}
	backend.contextCurrent = 2_000
	backend.usage.InputTokens = 2_000
	m.cachedInfoPanelFP = m.infoPanelFingerprint(40, 24)
	m.cachedInfoPanelOut = first

	m.handleAgentEvent(agentEventMsg{event: agent.UsageUpdatedEvent{}})
	second := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(second, "2.0k") {
		t.Fatalf("info panel should refresh after UsageUpdatedEvent, got %q", second)
	}
}

func TestInfoPanelCacheIncludesRequestReductionSurface(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 42_600
	backend.contextBytes = 200_000
	backend.contextLimit = 100_000
	backend.contextMessageCount = 370
	backend.contextReduction = agent.ContextReductionStats{Messages: 130, Bytes: 300_500, CurrentBytes: 200_000, CurrentMessages: 130}
	model := NewModel(backend)

	first := model.renderInfoPanel(50, 24)
	backend.contextReduction = agent.ContextReductionStats{Messages: 130, Bytes: 300_500, CurrentBytes: 100_000, CurrentMessages: 80}

	second := terminaltext.StripANSI(model.renderInfoPanel(50, 24))
	if second == terminaltext.StripANSI(first) {
		t.Fatalf("info panel reused stale cached output after request reduction surface changed")
	}
	if !strings.Contains(second, "Bytes: 97.7 KB (↓75%)") || !strings.Contains(second, "Messages: 80") {
		t.Fatalf("info panel should refresh when request reduction surface changes, got:\n%s", second)
	}
}

func TestInfoPanelShowsContextBytesAndReducedRatio(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 42_600
	backend.contextBytes = 200_000
	backend.contextLimit = 100_000
	backend.contextMessageCount = 370
	backend.contextReduction = agent.ContextReductionStats{Messages: 130, Bytes: 300_500, CurrentBytes: 200_000, CurrentMessages: 130}
	model := NewModel(backend)

	plain := terminaltext.StripANSI(model.renderInfoPanel(50, 24))
	if !strings.Contains(plain, "Context: 42.6k (43%)") {
		t.Fatalf("info panel missing context summary, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Bytes: 195.3 KB (↓60%)") {
		t.Fatalf("info panel missing reduced bytes summary, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Messages: 130") || strings.Contains(plain, "Messages: 370") {
		t.Fatalf("info panel should show actual request message count, got:\n%s", plain)
	}
}

func TestInfoPanelHidesContextBytesWhenNoMessages(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextBytes = 200_000
	backend.contextLimit = 100_000
	backend.contextMessageCount = 0
	model := NewModel(backend)

	plain := terminaltext.StripANSI(model.renderInfoPanel(50, 24))
	if strings.Contains(plain, "Bytes:") {
		t.Fatalf("info panel should not show context bytes before any messages, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Messages: 0") {
		t.Fatalf("info panel should still show empty context message count, got:\n%s", plain)
	}
}

func TestInfoPanelShowsReducedRatioWithoutCurrentReductionSurface(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 42_600
	backend.contextBytes = 200_000
	backend.contextLimit = 100_000
	backend.contextMessageCount = 370
	backend.contextReduction = agent.ContextReductionStats{Messages: 130, Bytes: 300_500}
	model := NewModel(backend)

	plain := terminaltext.StripANSI(model.renderInfoPanel(50, 24))
	if !strings.Contains(plain, "Bytes: 195.3 KB (↓60%)") {
		t.Fatalf("info panel should show reduced byte ratio without request surface bytes, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Messages: 370") {
		t.Fatalf("info panel should fall back to restored context message count, got:\n%s", plain)
	}
}

func TestInfoPanelUsesSameReductionFormatForSidebarWidths(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 39_700
	backend.contextBytes = 674_400
	backend.contextLimit = 248_000
	backend.contextMessageCount = 122
	backend.contextReduction = agent.ContextReductionStats{Messages: 49, Bytes: 551_300, CurrentBytes: 674_400, CurrentMessages: 49}
	model := NewModel(backend)

	for _, width := range []int{28, 40} {
		plain := terminaltext.StripANSI(model.renderInfoPanel(width, 24))
		if !strings.Contains(plain, "Bytes: 658.6 KB (↓45%)") {
			t.Fatalf("info panel missing byte reduction at width %d, got:\n%s", width, plain)
		}
		if !strings.Contains(plain, "Messages: 49") || strings.Contains(plain, "Messages: 122") {
			t.Fatalf("request message count should be shown at width %d, got:\n%s", width, plain)
		}
	}
}

func TestRenderInfoPanelUsageUsesSingleColumnAndHidesZeroValues(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextLimit = 0 // isolate: only test token summary hiding zero values
	backend.usage = analytics.SessionStats{
		InputTokens:     100_000,
		CacheReadTokens: 1_020,
	}

	m := NewModel(backend)
	lines := infoPanelPlainLines(m.renderInfoPanel(44, 20))
	usageLines := infoPanelSectionLines(lines, "USAGE")

	want := []string{
		"TOKENS",
		"↑ 101.0k",
		"Cache R   1.0k (1%)",
	}
	if len(usageLines) != len(want) {
		t.Fatalf("usage lines = %#v, want %#v", usageLines, want)
	}
	for i, line := range want {
		if usageLines[i] != line {
			t.Fatalf("usage line %d = %q, want %q", i, usageLines[i], line)
		}
	}

	plain := strings.Join(lines, "\n")
	if strings.Contains(plain, "↓") {
		t.Fatalf("rendered info panel should hide zero output summary; got %q", plain)
	}
	if strings.Contains(plain, "Cache W") {
		t.Fatalf("rendered info panel should hide zero cache write line; got %q", plain)
	}
}

func TestRenderInfoPanelUsageHidesNegligibleCacheShare(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextLimit = 0 // isolate: only test token summary ratio hiding
	backend.usage = analytics.SessionStats{
		InputTokens:     123_456,
		CacheReadTokens: 1_234,
	}

	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(44, 20)), "USAGE")
	if !slices.Contains(lines, "Cache R   1.2k") {
		t.Fatalf("cache line should keep the absolute count without a ratio, got %#v", lines)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "Cache R") && strings.Contains(line, "%") {
			t.Fatalf("negligible cache share should not render a ratio, got %#v", lines)
		}
	}
}

func TestRenderInfoPanelHidesUsageWhenAllZero(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextLimit = 0 // both current and limit are zero
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(32, 20))
	if strings.Contains(plain, "USAGE") {
		t.Fatalf("rendered info panel should hide zero USAGE block; got %q", plain)
	}
}

func TestRenderInfoPanelShowsContextWhenOnlyLimitSet(t *testing.T) {
	backend := newInfoPanelAgent()
	// contextCurrent = 0, contextLimit = 200_000 (default)
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(plain, "USAGE") {
		t.Fatalf("rendered info panel should show USAGE when context limit > 0; got %q", plain)
	}
	if !strings.Contains(plain, "Context") {
		t.Fatalf("rendered info panel should show Context line when limit > 0; got %q", plain)
	}
}

// TestRenderInfoPanelSubAgentWithoutContextLimit simulates TUI focus on a
// SubAgent whose ctxMgr has not yet received a context limit (maxTokens=0,
// lastTotalContextTokens=0). The USAGE section should be hidden because both
// current and limit are zero.
func TestRenderInfoPanelSubAgentWithoutContextLimit(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 0
	backend.contextLimit = 0 // SubAgent ctxMgr before first successful LLM call
	backend.usage = analytics.SessionStats{
		InputTokens:  3_100_000,
		OutputTokens: 19_800,
	}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	// TOKENS sub-section should still render (cumulative usage exists).
	if !strings.Contains(plain, "USAGE") {
		t.Fatalf("rendered info panel should show USAGE when tokens exist; got %q", plain)
	}
	if strings.Contains(plain, "Context") {
		t.Fatalf("rendered info panel should hide Context line when both current and limit are 0; got %q", plain)
	}
	if !strings.Contains(plain, "TOKENS") {
		t.Fatalf("rendered info panel should still show TOKENS sub-section; got %q", plain)
	}
}

// TestRenderInfoPanelSubAgentAfterLLMCallShowsContext simulates a SubAgent that
// has received its first successful LLM response, setting both context limit
// and current usage. Context gauge should render normally.
func TestRenderInfoPanelSubAgentAfterLLMCallShowsContext(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 128_000 // set by SetMaxTokens after first LLM call
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(plain, "Context") {
		t.Fatalf("rendered info panel should show Context after SubAgent LLM call; got %q", plain)
	}
	if !strings.Contains(plain, "39%") {
		t.Fatalf("rendered info panel should show correct percentage; got %q", plain)
	}
}

func TestRenderInfoPanelShowsUsageWhenContextNonZero(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 2000
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(plain, "USAGE") {
		t.Fatalf("rendered info panel should show USAGE when context > 0; got %q", plain)
	}
}

func TestRenderInfoPanelShowsUsageUsesInputBudgetRatherThanTotalContextWindow(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 100_000
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(plain, "Context: 50.0k (50%)") {
		t.Fatalf("rendered info panel should show input-budget percentage; got %q", plain)
	}
}

func TestRenderInfoPanelUnknownUsageShowsPlainZero(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextLimit = 100_000
	m := NewModel(backend)

	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 24)), "USAGE")
	if len(lines) == 0 || lines[0] != "Context: 0 (0%)" {
		t.Fatalf("a session without usage should render a plain zero context line, got %#v", lines)
	}
}

func TestRenderInfoPanelFrozenEstimateIsMarkedApproximate(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 12_300
	backend.contextUsageState = ctxmgr.ContextUsageEstimated
	m := NewModel(backend)

	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 24)), "USAGE")
	if len(lines) == 0 || lines[0] != "Context: ≈12.3k (6%)" {
		t.Fatalf("a frozen estimate should be marked approximate, got %#v", lines)
	}
}

func TestRenderInfoPanelStaleReadingIsMarkedApproximate(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 12_300
	backend.contextUsageState = ctxmgr.ContextUsageStale
	m := NewModel(backend)

	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 24)), "USAGE")
	if len(lines) == 0 || lines[0] != "Context: ≈12.3k (6%)" {
		t.Fatalf("a reading kept from a previous model window should be marked approximate, got %#v", lines)
	}
}

func TestInfoPanelCacheIncludesUsageState(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 12_300
	backend.contextUsageState = ctxmgr.ContextUsageEstimated
	m := NewModel(backend)

	first := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(first, "Context: ≈12.3k") {
		t.Fatalf("frozen estimate should render the approximate marker, got %q", first)
	}
	backend.contextUsageState = ctxmgr.ContextUsageObserved
	second := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if strings.Contains(second, "≈") {
		t.Fatalf("info panel reused the approximate marker after the usage state changed, got %q", second)
	}
}

func TestRenderInfoPanelUsageGroupsContextMessagesAndCache(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 159_100
	backend.contextBytes = 86_323
	backend.contextMessageCount = 360
	backend.contextReduction = agent.ContextReductionStats{Messages: 12, Bytes: 86_323, CurrentBytes: 86_323, CurrentMessages: 12}
	backend.usage = analytics.SessionStats{
		InputTokens:     20_400_000,
		OutputTokens:    112_400,
		CacheReadTokens: 2_300_000,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(34, 24)), "USAGE")
	want := []string{
		"Context: 159.1k (80%)",
		"[■■■■■■■■■■■■■■■■■■■■□□□□□□]",
		"Bytes: 84.3 KB (↓50%)",
		"Messages: 12",
		"",
		"TOKENS",
		"↑ 22.7M  ↓ 112.4k",
		"Cache R   2.3M (10%)",
	}
	if len(usageLines) != len(want) {
		t.Fatalf("usage lines = %#v, want %#v", usageLines, want)
	}
	for i, line := range want {
		if usageLines[i] != line {
			t.Fatalf("usage line %d = %q, want %q", i, usageLines[i], line)
		}
	}
}

func TestRenderInfoPanelUsageShowsReasoningTokens(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{
		InputTokens:     10_000,
		OutputTokens:    20_000,
		ReasoningTokens: 5_600,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(36, 20)), "USAGE")
	want := []string{
		"TOKENS",
		"↑ 10.0k  ↓ 20.0k",
		"Think     5.6k",
	}
	for _, expected := range want {
		found := slices.Contains(usageLines, expected)
		if !found {
			t.Fatalf("usage lines = %#v, missing %q", usageLines, expected)
		}
	}
}

func TestRenderInfoPanelUsageCacheDetailsAlignReadAndWriteValues(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{
		InputTokens:      10_000,
		CacheReadTokens:  2_300,
		CacheWriteTokens: 640,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(36, 20)), "USAGE")
	want := []string{
		"TOKENS",
		"↑ 12.9k",
		"Cache R   2.3k (18%)",
		"Cache W   640",
	}
	for _, expected := range want {
		found := slices.Contains(usageLines, expected)
		if !found {
			t.Fatalf("usage lines = %#v, missing %q", usageLines, expected)
		}
	}
	if slices.Contains(usageLines, "Uncached  10.0k") {
		t.Fatalf("usage lines = %#v, sidebar should hide uncached bucket", usageLines)
	}
}

func TestRenderInfoPanelUsageCacheReadPercentIncludesCacheWrites(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{
		InputTokens:      10_000,
		CacheReadTokens:  5_000,
		CacheWriteTokens: 10_000,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(36, 20)), "USAGE")
	if !slices.Contains(usageLines, "Cache R   5.0k (20%)") {
		t.Fatalf("usage lines = %#v, want cache read percent over uncached input, cache reads, and cache writes", usageLines)
	}
	if slices.Contains(usageLines, "Cache R   5.0k (25%)") {
		t.Fatalf("usage lines = %#v, cache read percent should not omit cache reads from full input", usageLines)
	}
}

func TestRenderInfoPanelTimeBlockHiddenWhenAllBucketsZero(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	out := infoPanelPlainLines(m.renderInfoPanel(42, 24))
	if slices.Contains(out, "TIME") {
		t.Fatalf("TIME section should be hidden when all buckets are zero, got:\n%s", out)
	}
}

func TestRenderInfoPanelTimeBlockShowsBucketsAndPercent(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Model:      72 * time.Second,
		Tool:       38 * time.Second,
		Cooldown:   12 * time.Second,
		UserWait:   4 * time.Second,
		Compaction: 8 * time.Second,
	}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(60, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Model", "1m12s", "Tools", "38s", "Cooldown", "12s", "User wait", "4s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("TIME should contain %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "compaction") || strings.Contains(joined, ",") {
		t.Fatalf("TIME should display model time without a separate compaction note, got:\n%s", joined)
	}
	// All four displayed percentages must sum to 100, and every non-zero
	// bucket must show at least 1%.
	if got := percentSum(joined); got != 100 {
		t.Fatalf("TIME percentages sum to %d, want 100:\n%s", got, joined)
	}
	if min := percentFloor(joined); min < 1 {
		t.Fatalf("some non-zero bucket shows %d%%, want >= 1%%:\n%s", min, joined)
	}
}

func TestRenderInfoPanelTimeBlockDropsUnitsInsteadOfEllipsis(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{Model: 1*time.Hour + 41*time.Minute + 25*time.Second}
	m := NewModel(backend)

	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(21, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "...") || strings.Contains(joined, "…") {
		t.Fatalf("TIME should not use an ellipsis, got:\n%s", joined)
	}
	if !strings.Contains(joined, "1h41m (100%)") {
		t.Fatalf("TIME should hide seconds when the full duration does not fit, got:\n%s", joined)
	}
}

func TestRenderInfoPanelTimeBlockDoesNotDisplayCompactionSeparately(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Model:      1*time.Hour + 41*time.Minute + 25*time.Second,
		Compaction: 8 * time.Second,
	}
	m := NewModel(backend)

	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(30, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "...") || strings.Contains(joined, "…") {
		t.Fatalf("TIME should not use an ellipsis, got:\n%s", joined)
	}
	if !strings.Contains(joined, "1h41m25s (100%)") {
		t.Fatalf("TIME should keep the complete model duration, got:\n%s", joined)
	}
	if strings.Contains(joined, "incl. compaction") {
		t.Fatalf("TIME should not display a separate compaction note, got:\n%s", joined)
	}
	if strings.Contains(joined, ",") {
		t.Fatalf("TIME should not leave a compaction-note separator behind, got:\n%s", joined)
	}
}

func TestFormatWalltimeValueUsesPrimaryDuration(t *testing.T) {
	got := formatWalltimeValue(40, "Model", 1*time.Hour+41*time.Minute+25*time.Second, 89)
	if got != "1h41m25s (89%)" {
		t.Fatalf("formatWalltimeValue() = %q, want primary value", got)
	}
}

func TestRenderInfoPanelTimeBlockAllBucketsSumToHundred(t *testing.T) {
	// Rounding must never push the displayed percentages off 100, including
	// when Cooldown and User wait participate in the share.
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Model:    30 * time.Second,
		Tool:     30 * time.Second,
		Cooldown: 30 * time.Second,
		UserWait: 30 * time.Second,
	}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(60, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if got := percentSum(joined); got != 100 {
		t.Fatalf("TIME percentages sum to %d, want 100:\n%s", got, joined)
	}
}

func TestRenderInfoPanelTimeBlockSkewNeverGoesNegative(t *testing.T) {
	// One bucket dominates while the other three sit at ~1s. The old rounding
	// forced each non-last bucket to at least 1% and let the final bucket
	// absorb the rest, which could push the last bucket negative. Largest
	// remainder must keep every displayed share >= 0 and sum to exactly 100.
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Model:    1000 * time.Second,
		Tool:     1 * time.Second,
		Cooldown: 1 * time.Second,
		UserWait: 1 * time.Second,
	}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(60, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if got := percentSum(joined); got != 100 {
		t.Fatalf("TIME percentages sum to %d, want 100:\n%s", got, joined)
	}
	if strings.Contains(joined, "-1%") || strings.Contains(joined, "-") {
		t.Fatalf("TIME rows must never show a negative share, got:\n%s", joined)
	}
}

func TestRenderInfoPanelTimeBlockSubSecondToolHidden(t *testing.T) {
	// A sub-second bucket is not shown and does not participate in the
	// percentage split, so the displayed buckets still sum to 100.
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Model:    90 * time.Second,
		Tool:     300 * time.Millisecond,
		Cooldown: 10 * time.Second,
	}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(60, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Tools") {
		t.Fatalf("sub-second Tools bucket must be hidden, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Model") || !strings.Contains(joined, "Cooldown") {
		t.Fatalf("TIME should still show the remaining buckets, got:\n%s", joined)
	}
	if got := percentSum(joined); got != 100 {
		t.Fatalf("TIME percentages sum to %d, want 100:\n%s", got, joined)
	}
}

func TestRenderInfoPanelTimeBlockDoesNotDisplaySubsecondCompaction(t *testing.T) {
	// Compaction is included in the model total internally and is not rendered
	// as a separate TIME item, even when the Model bucket is hidden.
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Model:      400 * time.Millisecond,
		Tool:       30 * time.Second,
		Compaction: 400 * time.Millisecond,
	}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(60, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Model") {
		t.Fatalf("sub-second Model bucket must be hidden, got:\n%s", joined)
	}
	if strings.Contains(joined, "compaction") || strings.Contains(joined, ",") {
		t.Fatalf("TIME should not display compaction separately, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Tools") {
		t.Fatalf("TIME should still show the Tools bucket, got:\n%s", joined)
	}
}

func TestRenderInfoPanelTimeBlockHiddenWhenAllBucketsSubSecond(t *testing.T) {
	// When every bucket is sub-second there is nothing worth showing: the
	// whole TIME section is hidden.
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{
		Tool:     300 * time.Millisecond,
		UserWait: 400 * time.Millisecond,
	}
	m := NewModel(backend)
	out := infoPanelPlainLines(m.renderInfoPanel(60, 24))
	if slices.Contains(out, "TIME") {
		t.Fatalf("TIME section should be hidden when all buckets are sub-second, got:\n%s", out)
	}
}

func TestRenderInfoPanelTimeBlockHidesZeroBuckets(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{Model: 90 * time.Second}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(42, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Model") || !strings.Contains(joined, "1m30s") {
		t.Fatalf("TIME should show the non-zero bucket, got:\n%s", joined)
	}
	for _, hidden := range []string{"Tools", "Cooldown", "User wait", "Percent"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("zero bucket %q should be hidden, got:\n%s", hidden, joined)
		}
	}
}

func TestInfoPanelFingerprintChangesWhenWalltimeBucketChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	first := m.infoPanelFingerprint(42, 24)
	backend.walltimeStats = analytics.WalltimeStats{Tool: 3 * time.Second}
	second := m.infoPanelFingerprint(42, 24)
	if first == second {
		t.Fatalf("fingerprint must change when a walltime bucket changes")
	}
	backend.walltimeStats = analytics.WalltimeStats{Tool: 3 * time.Second, Cooldown: 1 * time.Second}
	third := m.infoPanelFingerprint(42, 24)
	if third == second {
		t.Fatalf("fingerprint must change when a second bucket appears")
	}
}

func TestRenderInfoPanelTimeBlockCooldownOnlyShowsFullPercent(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{Cooldown: 45 * time.Second}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(42, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Cooldown") || !strings.Contains(joined, "45s (100%)") {
		t.Fatalf("cooldown-only TIME should show the bucket at 100%%, got:\n%s", joined)
	}
}

func TestRenderInfoPanelTimeBlockModelZeroWithToolShowsToolOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.walltimeStats = analytics.WalltimeStats{Tool: 30 * time.Second}
	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(42, 24)), "TIME")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Tools") || !strings.Contains(joined, "30s") {
		t.Fatalf("TIME should show the tool bucket, got:\n%s", joined)
	}
	if strings.Contains(joined, "Model") {
		t.Fatalf("TIME must not render an empty Model row when model time is zero, got:\n%s", joined)
	}
}

func TestRenderInfoPanelUsageRefreshesSavedStatsWhenFingerprintChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 39_700
	backend.contextBytes = 674_400
	backend.contextMessageCount = 122

	m := NewModel(backend)
	first := infoPanelPlainLines(m.renderInfoPanel(40, 24))
	if slices.Contains(first, "Bytes: 658.6 KB (↓45%)") || slices.Contains(first, "Messages: 122 (") {
		t.Fatalf("initial usage render should not show reduction, got:\n%s", first)
	}

	backend.contextReduction = agent.ContextReductionStats{Messages: 49, Bytes: 551_300, CurrentBytes: 674_400, CurrentMessages: 49, TokensSaved: 152_000, ReusedStable: true}
	second := infoPanelPlainLines(m.renderInfoPanel(40, 24))
	if !slices.Contains(second, "Bytes: 658.6 KB (↓45%)") {
		t.Fatalf("saved bytes should render after reduction stats change, got:\n%s", second)
	}
	if !slices.Contains(second, "Messages: 49") || slices.Contains(second, "Messages: 122") {
		t.Fatalf("messages should render request count when reduction stats are available, got:\n%s", second)
	}
}

func TestRenderInfoPanelUsageShowsRequestSurfaceWhenAvailable(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 39_700
	backend.contextLimit = 200_000
	backend.contextBytes = 153_600
	backend.contextMessageCount = 122
	backend.contextReduction = agent.ContextReductionStats{
		Bytes:           81_920,
		Messages:        37,
		CurrentBytes:    73_728,
		CurrentMessages: 85,
	}

	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(42, 24)), "USAGE")
	if !slices.Contains(lines, "Bytes: 72.0 KB (↓53%)") {
		t.Fatalf("bytes should show actual request surface and reduction percentage, got %#v", lines)
	}
	if !slices.Contains(lines, "Messages: 85") {
		t.Fatalf("messages should show actual request message count, got %#v", lines)
	}
	if slices.Contains(lines, "Bytes: 150.0 KB") || slices.Contains(lines, "Messages: 122") || slices.Contains(lines, "Req:") {
		t.Fatalf("raw context size and separate Req line should not render when request surface is available, got %#v", lines)
	}
}

func TestRenderInfoPanelUsageHidesNegligibleByteReduction(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 39_700
	backend.contextLimit = 200_000
	backend.contextBytes = 153_600
	backend.contextMessageCount = 122
	backend.contextReduction = agent.ContextReductionStats{
		Bytes:           500,
		Messages:        37,
		CurrentBytes:    100_000,
		CurrentMessages: 85,
	}

	m := NewModel(backend)
	lines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(42, 24)), "USAGE")
	if !slices.Contains(lines, "Bytes: 97.7 KB") {
		t.Fatalf("bytes should omit negligible reduction ratio, got %#v", lines)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "Bytes:") && strings.Contains(line, "↓") {
			t.Fatalf("negligible byte reduction should not render a ratio, got %#v", lines)
		}
	}
}

func TestRenderInfoPanelUsageStandardWidthShowsCurrentStableLayout(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 211_000
	backend.contextLimit = 1_000_000
	backend.contextMessageCount = 452
	backend.usage = analytics.SessionStats{
		InputTokens:     29_900_000,
		OutputTokens:    143_100,
		CacheReadTokens: 3_500_000,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(34, 24)), "USAGE")
	want := []string{
		"Context: 211.0k (21%)",
		"[■■■■■□□□□□□□□□□□□□□□□□□□□□]",
		"Messages: 452",
		"",
		"TOKENS",
		"↑ 33.4M  ↓ 143.1k",
		"Cache R   3.5M (10%)",
	}
	if len(usageLines) != len(want) {
		t.Fatalf("usage lines = %#v, want %#v", usageLines, want)
	}
	for i, line := range want {
		if usageLines[i] != line {
			t.Fatalf("usage line %d = %q, want %q", i, usageLines[i], line)
		}
	}
}

func TestRenderInfoPanelUsageStandardWidthShowsCostOnSeparateLine(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{
		InputTokens:   2_400_000,
		OutputTokens:  88_000,
		EstimatedCost: 1.2345,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "USAGE")
	wantSummary := "↑ 2.4M  ↓ 88.0k"
	wantCost := "$ 1.2345"
	summaryIdx := -1
	costIdx := -1
	for i, line := range usageLines {
		if line == wantSummary {
			summaryIdx = i
		}
		if line == wantCost {
			costIdx = i
		}
	}
	if summaryIdx < 0 {
		t.Fatalf("usage section should contain summary line %q; got %#v", wantSummary, usageLines)
	}
	if costIdx < 0 {
		t.Fatalf("usage section should contain cost line %q; got %#v", wantCost, usageLines)
	}
	if costIdx != summaryIdx+1 {
		t.Fatalf("cost line should be immediately after summary; got summary=%d cost=%d", summaryIdx, costIdx)
	}
}

func TestRenderInfoPanelUsageShowsReasoningTokensSeparately(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{
		InputTokens:     1_200_000,
		OutputTokens:    45_000,
		ReasoningTokens: 350_000,
		EstimatedCost:   0.8765,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "USAGE")

	// Should have: TOKENS, summary line, Think line, cost line
	wantSummary := "↑ 1.2M  ↓ 45.0k"
	wantThinking := "Think     350.0k"
	wantCost := "$ 0.8765"

	summaryIdx := -1
	thinkingIdx := -1
	costIdx := -1
	for i, line := range usageLines {
		if line == wantSummary {
			summaryIdx = i
		}
		if line == wantThinking {
			thinkingIdx = i
		}
		if line == wantCost {
			costIdx = i
		}
	}

	if summaryIdx < 0 {
		t.Fatalf("usage section should contain summary line %q; got %#v", wantSummary, usageLines)
	}
	if thinkingIdx < 0 {
		t.Fatalf("usage section should contain thinking line %q; got %#v", wantThinking, usageLines)
	}
	if costIdx < 0 {
		t.Fatalf("usage section should contain cost line %q; got %#v", wantCost, usageLines)
	}
	if thinkingIdx != summaryIdx+1 {
		t.Fatalf("thinking line should be immediately after summary; got summary=%d thinking=%d", summaryIdx, thinkingIdx)
	}
	if costIdx != thinkingIdx+1 {
		t.Fatalf("cost line should be immediately after thinking; got thinking=%d cost=%d", thinkingIdx, costIdx)
	}
}

// percentSum extracts every "(NN%)" occurrence from the joined TIME section
// text and returns their sum, for asserting the displayed percentages always
// total 100.
func percentSum(text string) int {
	sum := 0
	for _, n := range percentValues(text) {
		sum += n
	}
	return sum
}

// percentFloor returns the smallest "(NN%)" value in the joined section text,
// for asserting every non-zero bucket shows at least 1%.
func percentFloor(text string) int {
	min := 100
	for _, n := range percentValues(text) {
		if n < min {
			min = n
		}
	}
	return min
}

func percentValues(text string) []int {
	var out []int
	rest := text
	for {
		idx := strings.Index(rest, "(")
		if idx < 0 {
			return out
		}
		end := strings.Index(rest[idx:], "%")
		if end < 0 {
			return out
		}
		token := rest[idx+1 : idx+end]
		if n, err := strconv.Atoi(token); err == nil {
			out = append(out, n)
		}
		rest = rest[idx+end+1:]
	}
}

func TestInfoPanelUsesSingleUsageLayoutAcrossWidths(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextBytes = 86_000
	backend.contextLimit = 128_000
	backend.contextMessageCount = 42
	backend.usage = analytics.SessionStats{
		InputTokens:     500_000,
		OutputTokens:    10_000,
		CacheReadTokens: 20_000,
	}

	m := NewModel(backend)
	for _, width := range []int{23, 30, 32} {
		plain := terminaltext.StripANSI(m.renderInfoPanel(width, 20))
		for _, want := range []string{"Context", "Bytes", "Messages", "TOKENS", "Cache R"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("info panel width %d missing %q in single layout, got %q", width, want, plain)
			}
		}
		if strings.Contains(plain, "Reduced") {
			t.Fatalf("info panel should not render legacy Reduced label at width %d, got %q", width, plain)
		}
	}
}

func TestContextUsageSeverityFor(t *testing.T) {
	// No pressure lines (SubAgent/parked): fixed 50/80 fallback.
	if got := contextUsageSeverityFor(0.40, 0, 0); got != contextUsageNormal {
		t.Fatalf("severity(40%%, 0, 0) = %v, want normal", got)
	}
	if got := contextUsageSeverityFor(0.50, 0, 0); got != contextUsageWarning {
		t.Fatalf("severity(50%%, 0, 0) = %v, want warning at fallback 50%%", got)
	}
	if got := contextUsageSeverityFor(0.79, 0, 0); got != contextUsageWarning {
		t.Fatalf("severity(79%%, 0, 0) = %v, want warning", got)
	}
	if got := contextUsageSeverityFor(0.80, 0, 0); got != contextUsageCritical {
		t.Fatalf("severity(80%%, 0, 0) = %v, want critical at fallback 80%%", got)
	}

	// Configured lines: warning at the reminder, critical at the threshold.
	if got := contextUsageSeverityFor(0.599, 0.6, 0.8); got != contextUsageNormal {
		t.Fatalf("severity(59.9%%, 0.6, 0.8) = %v, want normal", got)
	}
	if got := contextUsageSeverityFor(0.6, 0.6, 0.8); got != contextUsageWarning {
		t.Fatalf("severity(60%%, 0.6, 0.8) = %v, want warning at reminder", got)
	}
	if got := contextUsageSeverityFor(0.799, 0.6, 0.8); got != contextUsageWarning {
		t.Fatalf("severity(79.9%%, 0.6, 0.8) = %v, want warning below threshold", got)
	}
	if got := contextUsageSeverityFor(0.8, 0.6, 0.8); got != contextUsageCritical {
		t.Fatalf("severity(80%%, 0.6, 0.8) = %v, want critical at threshold", got)
	}

	// Reminder configured at/above the threshold never shows warning: the
	// threshold crossing is critical ("whichever line comes first").
	if got := contextUsageSeverityFor(0.75, 0.8, 0.7); got != contextUsageCritical {
		t.Fatalf("severity(75%%, 0.8, 0.7) = %v, want critical (not warning)", got)
	}

	// Threshold 0 (auto-compaction disabled): fixed 80% stays as an
	// oversize-risk guard while the reminder drives warning.
	if got := contextUsageSeverityFor(0.5, 0.4, 0); got != contextUsageWarning {
		t.Fatalf("severity(50%%, 0.4, 0) = %v, want warning at reminder", got)
	}
	if got := contextUsageSeverityFor(0.85, 0.4, 0); got != contextUsageCritical {
		t.Fatalf("severity(85%%, 0.4, 0) = %v, want critical via fixed 80%% guard", got)
	}
}

// TestRenderInfoPanelContextColorFollowsPressureLines verifies the wiring from
// ContextPressureLinesForModelRef through contextValueStyle into the rendered
// panel: the Context value turns warning at the reminder line and critical at
// the auto-compaction threshold (compared to the same rendering that stays
// normal below both lines).
func TestRenderInfoPanelContextColorFollowsPressureLines(t *testing.T) {
	renderContextLine := func(percent float64) string {
		backend := newInfoPanelAgent()
		backend.sessionControlAgent.providerModelRef = "test-model"
		backend.contextLimit = 100_000
		backend.contextCurrent = int(percent * 100_000)
		backend.contextReminder = 0.6
		backend.contextThreshold = 0.8
		m := NewModel(backend)
		rendered := m.renderInfoPanel(44, 24)
		for line := range strings.SplitSeq(rendered, "\n") {
			if strings.Contains(line, "Context") {
				return line
			}
		}
		t.Fatalf("no Context line in rendered panel")
		return ""
	}

	line := renderContextLine(0.5)
	if strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelWarningFg)) {
		t.Fatalf("Context at 50%% should be below reminder 0.6 (normal): %q", line)
	}

	line = renderContextLine(0.7)
	if !strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelWarningFg)) {
		t.Fatalf("Context at 70%% should be warning (>= reminder 0.6): %q", line)
	}
	if strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelCriticalFg)) {
		t.Fatalf("Context at 70%% should not be critical (below threshold 0.8): %q", line)
	}

	line = renderContextLine(0.9)
	if !strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelCriticalFg)) {
		t.Fatalf("Context at 90%% should be critical (>= threshold 0.8): %q", line)
	}
}

// Context colors follow the committed budget while MODEL previews a pending
// switch. They change only when the budget identity changes too.
func TestRenderInfoPanelContextColorKeepsBudgetDuringPendingModelSwitch(t *testing.T) {
	renderContextLine := func(percent float64) string {
		backend := newInfoPanelAgent()
		backend.sessionControlAgent.providerModelRef = "old-model"
		backend.runningModelRef = "old-model"
		backend.nextRequestModelRef = "new-model"
		backend.contextLimit = 100_000
		backend.contextCurrent = int(percent * 100_000)
		// The running/selected model ("old-model") has a spacious 0.8
		// threshold; the pending "new-model" compacts early at 0.6.
		backend.contextLinesForRef = func(ref string) (float64, float64) {
			if ref == "new-model" {
				return 0.5, 0.6
			}
			return 0.6, 0.8
		}
		m := NewModel(backend)
		rendered := m.renderInfoPanel(44, 24)
		for line := range strings.SplitSeq(rendered, "\n") {
			if strings.Contains(line, "Context") {
				return line
			}
		}
		t.Fatalf("no Context line in rendered panel")
		return ""
	}

	line := renderContextLine(0.55)
	if strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelWarningFg)) || strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelCriticalFg)) {
		t.Fatalf("Context at 55%% should use the committed model's 0.6 reminder: %q", line)
	}
	line = renderContextLine(0.62)
	if !strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelWarningFg)) || strings.Contains(line, contextFGSGR(currentStyles.Palette.InfoPanelCriticalFg)) {
		t.Fatalf("Context at 62%% should use the committed model's 0.8 threshold: %q", line)
	}
}
