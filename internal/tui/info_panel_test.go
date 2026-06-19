package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/config"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

type infoPanelAgent struct {
	*sessionControlAgent
	usage               analytics.SessionStats
	runningModelRef     string
	nextRequestModelRef string
	contextCurrent      int
	contextBytes        int
	contextLimit        int
	contextMessageCount int
	contextReduction    agent.ContextReductionStats
	todos               []tools.TodoItem
	rateLimitSnapshot   *ratelimit.KeyRateLimitSnapshot
	lspRows             []agent.LSPServerDisplay
	mcpRows             []agent.MCPServerDisplay
	availableSkills     []*skill.Meta
	invokedSkills       []*skill.Meta
	keysConfirmed       int
	keysTotal           int
	wakeRateLimitCalls  int
}

func newInfoPanelAgent() *infoPanelAgent {
	return &infoPanelAgent{
		sessionControlAgent: &sessionControlAgent{},
		runningModelRef:     "test-model",
		contextLimit:        200_000,
	}
}

func (a *infoPanelAgent) RunningModelRef() string {
	return a.runningModelRef
}

func (a *infoPanelAgent) NextRequestModelRef() string {
	return a.nextRequestModelRef
}

func (a *infoPanelAgent) RunningVariant() string { return "" }

func (a *infoPanelAgent) GetUsageStats() analytics.SessionStats {
	return a.usage
}

func (a *infoPanelAgent) GetSidebarUsageStats() analytics.SessionStats {
	return a.usage
}

func (a *infoPanelAgent) GetContextStats() (current, limit int) {
	return a.contextCurrent, a.contextLimit
}

func (a *infoPanelAgent) GetContextMessageCount() int {
	return a.contextMessageCount
}

func (a *infoPanelAgent) GetContextBytes() int {
	return a.contextBytes
}

func (a *infoPanelAgent) GetContextReductionStats() agent.ContextReductionStats {
	return a.contextReduction
}

func (a *infoPanelAgent) GetTodos() []tools.TodoItem {
	return append([]tools.TodoItem(nil), a.todos...)
}

func (a *infoPanelAgent) InvokedSkills() []*skill.Meta {
	return append([]*skill.Meta(nil), a.invokedSkills...)
}

func (a *infoPanelAgent) ListSkills() []*skill.Meta {
	return append([]*skill.Meta(nil), a.availableSkills...)
}

func (a *infoPanelAgent) CurrentRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	return a.rateLimitSnapshot
}

func (a *infoPanelAgent) WakeCodexRateLimitPolling() {
	a.wakeRateLimitCalls++
}

func (a *infoPanelAgent) ServiceTier() config.ServiceTier {
	if a.sessionControlAgent != nil && a.sessionControlAgent.serviceTierEnabled {
		return config.ServiceTierFast
	}
	return a.sessionControlAgent.ServiceTier()
}

func (a *infoPanelAgent) EffectiveServiceTier() config.ServiceTier {
	if a.sessionControlAgent != nil {
		return a.sessionControlAgent.EffectiveServiceTier()
	}
	return config.ServiceTierStandard
}

func (a *infoPanelAgent) KeyStats() (confirmed, total int) {
	return a.keysConfirmed, a.keysTotal
}

func (a *infoPanelAgent) LSPServerList() []agent.LSPServerDisplay {
	return append([]agent.LSPServerDisplay(nil), a.lspRows...)
}

func (a *infoPanelAgent) MCPServerList() []agent.MCPServerDisplay {
	return append([]agent.MCPServerDisplay(nil), a.mcpRows...)
}

func TestUsageUpdatedEventInvalidatesInfoPanelCache(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 1_000
	backend.usage.InputTokens = 1_000
	m := NewModel(backend)

	first := stripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(first, "1.0k") {
		t.Fatalf("initial info panel should include initial token usage, got %q", first)
	}
	backend.contextCurrent = 2_000
	backend.usage.InputTokens = 2_000
	m.cachedInfoPanelFP = m.infoPanelFingerprint(40, 24)
	m.cachedInfoPanelOut = first

	m.handleAgentEvent(agentEventMsg{event: agent.UsageUpdatedEvent{}})
	second := stripANSI(m.renderInfoPanel(40, 24))
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

	second := stripANSI(model.renderInfoPanel(50, 24))
	if second == stripANSI(first) {
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

	plain := stripANSI(model.renderInfoPanel(50, 24))
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

	plain := stripANSI(model.renderInfoPanel(50, 24))
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

	plain := stripANSI(model.renderInfoPanel(50, 24))
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
		plain := stripANSI(model.renderInfoPanel(width, 24))
		if !strings.Contains(plain, "Bytes: 658.6 KB (↓45%)") {
			t.Fatalf("info panel missing byte reduction at width %d, got:\n%s", width, plain)
		}
		if !strings.Contains(plain, "Messages: 49") || strings.Contains(plain, "Messages: 122") {
			t.Fatalf("request message count should be shown at width %d, got:\n%s", width, plain)
		}
	}
}

func TestKeyPoolTickWakesCodexRateLimitPollingWhileAgentActive(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{CapturedAt: time.Now().Add(-codexActiveRateLimitPollInterval)}
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	updated, _ := m.Update(keyPoolTickMsg{gen: m.keyPoolTickGen})
	m = *(updated.(*Model))

	if backend.wakeRateLimitCalls != 1 {
		t.Fatalf("WakeCodexRateLimitPolling calls = %d, want 1", backend.wakeRateLimitCalls)
	}

	backend.rateLimitSnapshot.CapturedAt = time.Now()
	_, _ = m.Update(keyPoolTickMsg{gen: m.keyPoolTickGen})
	if backend.wakeRateLimitCalls != 1 {
		t.Fatalf("WakeCodexRateLimitPolling calls with fresh snapshot = %d, want 1", backend.wakeRateLimitCalls)
	}
}

func TestRenderInfoPanelCollapsibleHeaderUsesExtraSpaceAfterSummaryDot(t *testing.T) {
	got := strings.TrimSpace(stripANSI(renderInfoPanelCollapsibleHeader(24, true, "LSP", "3")))
	if got != "▼ LSP · 3" {
		t.Fatalf("collapsible header = %q, want %q", got, "▼ LSP · 3")
	}
}

func TestRenderInfoPanelAgentsUsesTaskSummaryLabelPolicy(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "update prompt tests", AgentDefName: "coder"}}, "main", "builder")

	plain := stripANSI(m.renderInfoPanel(40, 20))
	section := strings.Join(infoPanelSectionLines(infoPanelPlainLines(plain), "AGENTS"), "\n")
	if !strings.Contains(section, "builder") {
		t.Fatalf("info panel AGENTS should show main role, got %q", section)
	}
	if !strings.Contains(section, "update prompt tests") {
		t.Fatalf("info panel AGENTS should show task description, got %q", section)
	}
	if strings.Contains(section, "coder") {
		t.Fatalf("info panel AGENTS should prefer task description over agent short name, got %q", section)
	}
}

func TestRenderInfoPanelShowsInvokedSkills(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Discovered: true},
		{Name: "py-expert", Description: "Python development expert", Discovered: true},
	}
	backend.invokedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true, Invoked: true}}
	m := NewModel(backend)

	rendered := m.renderInfoPanel(40, 24)
	plain := stripANSI(rendered)
	section := infoPanelSectionLines(infoPanelPlainLines(plain), "▼ SKILLS")
	joined := strings.Join(section, "\n")
	if !strings.Contains(plain, "▼ SKILLS") {
		t.Fatalf("skills header missing in %q", plain)
	}
	for _, want := range []string{"go-expert", "py-expert"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("skills section missing %q in %q", want, joined)
		}
	}
	wantInvoked := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelSuccessFg)).Render("go-expert")
	if !strings.Contains(rendered, wantInvoked) {
		t.Fatalf("invoked skill should use success color; want %q in %q", wantInvoked, rendered)
	}
	wantAvailable := InfoPanelDim.Render("py-expert")
	if !strings.Contains(rendered, wantAvailable) {
		t.Fatalf("available skill should use dim color; want %q in %q", wantAvailable, rendered)
	}
}

func TestRenderInfoPanelSkipsInvokedSkillMissingFromAvailableList(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true}}
	backend.invokedSkills = []*skill.Meta{{Name: "missing-skill", Invoked: true}, {Name: "go-expert", Description: "Go language development expert", Discovered: true, Invoked: true}}
	m := NewModel(backend)

	rendered := m.renderInfoPanel(40, 24)
	plain := stripANSI(rendered)
	section := strings.Join(infoPanelSectionLines(infoPanelPlainLines(plain), "▼ SKILLS"), "\n")
	if strings.Contains(section, "missing-skill") {
		t.Fatalf("skills section should omit invoked skills missing from available list, got %q", section)
	}
	wantInvoked := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelSuccessFg)).Render("go-expert")
	if !strings.Contains(rendered, wantInvoked) {
		t.Fatalf("visible invoked skill should remain success-colored; want %q in %q", wantInvoked, rendered)
	}
}

func TestRenderInfoPanelShowsKeyPoolConfirmedCount(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "openai/test"
	backend.keysConfirmed = 3
	backend.keysTotal = 5
	m := NewModel(backend)

	plain := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "Keys: 3/5") {
		t.Fatalf("rendered info panel should show confirmed/total; got %q", plain)
	}
	if strings.Contains(plain, "Recovering") {
		t.Fatalf("rendered info panel should not show Recovering line; got %q", plain)
	}
}

func TestRenderInfoPanelPoolUsesValueColor(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.poolNamesByFocus = map[string][]string{"": {"thinking", "non-thinking"}}
	backend.currentPoolByFocus = map[string]string{"": "thinking"}
	backend.keysConfirmed = 1
	backend.keysTotal = 3
	m := NewModel(backend)

	rendered := m.renderInfoPanel(40, 20)
	want := InfoPanelValue.Render("Pool: thinking")
	if !strings.Contains(rendered, want) {
		t.Fatalf("Pool line should use InfoPanelValue; rendered=%q want fragment=%q", rendered, want)
	}
	warn := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelKeyWarnFg)).Render("Pool: thinking")
	if strings.Contains(rendered, warn) {
		t.Fatalf("Pool line should not use key warning color; rendered=%q warn fragment=%q", rendered, warn)
	}
}

func TestKeyPoolHealthSeverityThresholds(t *testing.T) {
	tests := []struct {
		name    string
		healthy int
		total   int
		want    keyPoolSeverity
	}{
		{name: "zero total defaults normal", healthy: 0, total: 0, want: keyPoolSeverityNormal},
		{name: "two of two healthy is normal", healthy: 2, total: 2, want: keyPoolSeverityNormal},
		{name: "one of two healthy is warn", healthy: 1, total: 2, want: keyPoolSeverityWarn},
		{name: "zero of two healthy is critical", healthy: 0, total: 2, want: keyPoolSeverityCritical},
		{name: "two of three healthy is normal", healthy: 2, total: 3, want: keyPoolSeverityNormal},
		{name: "one of three healthy is warn", healthy: 1, total: 3, want: keyPoolSeverityWarn},
		{name: "zero of three healthy is critical", healthy: 0, total: 3, want: keyPoolSeverityCritical},
		{name: "three of four healthy is normal", healthy: 3, total: 4, want: keyPoolSeverityNormal},
		{name: "two of four healthy is warn", healthy: 2, total: 4, want: keyPoolSeverityWarn},
		{name: "one of four healthy is critical", healthy: 1, total: 4, want: keyPoolSeverityCritical},
		{name: "zero of four healthy is critical", healthy: 0, total: 4, want: keyPoolSeverityCritical},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyPoolHealthSeverity(tc.healthy, tc.total); got != tc.want {
				t.Fatalf("keyPoolHealthSeverity(%d, %d) = %v, want %v", tc.healthy, tc.total, got, tc.want)
			}
		})
	}
}

func TestRenderInfoPanelInsertsBlankLineBetweenSections(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.keysConfirmed = 2
	backend.keysTotal = 2
	backend.contextCurrent = 2_000
	backend.contextMessageCount = 12
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}}
	backend.todos = []tools.TodoItem{{ID: "1", Content: "Investigate spacing", Status: "in_progress"}}

	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)

	lines := infoPanelPlainLines(m.renderInfoPanel(40, 24))
	var titles []string
	for _, line := range lines {
		if isInfoPanelSectionTitle(line) {
			titles = append(titles, line)
		}
	}
	for _, title := range titles[1:] {
		idx := -1
		for i, line := range lines {
			if line == title {
				idx = i
				break
			}
		}
		if idx < 2 {
			t.Fatalf("section %q missing or too close to start: %#v", title, lines)
		}
		if lines[idx-1] != "" {
			t.Fatalf("section %q should have a blank line before it; lines=%#v", title, lines)
		}
		if lines[idx-2] == "" {
			t.Fatalf("section %q should have exactly one blank line before it; lines=%#v", title, lines)
		}
	}
}

func TestRenderInfoPanelUsageUsesSingleColumnAndHidesZeroValues(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextLimit = 0 // isolate: only test token summary hiding zero values
	backend.usage = analytics.SessionStats{
		InputTokens:     123_456,
		CacheReadTokens: 1_234,
	}

	m := NewModel(backend)
	lines := infoPanelPlainLines(m.renderInfoPanel(44, 20))
	usageLines := infoPanelSectionLines(lines, "USAGE")

	want := []string{
		"TOKENS",
		"↑ 123.5k",
		"Cache R  1.2k (1%)",
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

func TestRenderInfoPanelHidesUsageWhenAllZero(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextLimit = 0 // both current and limit are zero
	m := NewModel(backend)

	plain := stripANSI(m.renderInfoPanel(32, 20))
	if strings.Contains(plain, "USAGE") {
		t.Fatalf("rendered info panel should hide zero USAGE block; got %q", plain)
	}
}

func TestRenderInfoPanelShowsContextWhenOnlyLimitSet(t *testing.T) {
	backend := newInfoPanelAgent()
	// contextCurrent = 0, contextLimit = 200_000 (default)
	m := NewModel(backend)

	plain := stripANSI(m.renderInfoPanel(40, 24))
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

	plain := stripANSI(m.renderInfoPanel(40, 24))
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

	plain := stripANSI(m.renderInfoPanel(40, 24))
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

	plain := stripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(plain, "USAGE") {
		t.Fatalf("rendered info panel should show USAGE when context > 0; got %q", plain)
	}
}

func TestRenderInfoPanelShowsUsageUsesInputBudgetRatherThanTotalContextWindow(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 100_000
	m := NewModel(backend)

	plain := stripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(plain, "Context: 50.0k (50%)") {
		t.Fatalf("rendered info panel should show input-budget percentage; got %q", plain)
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
		"↑ 20.4M  ↓ 112.4k",
		"Cache R  2.3M (11%)",
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
		"Think    5.6k",
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
		"↑ 10.0k",
		"Cache R  2.3k (22%)",
		"Cache W  640",
	}
	for _, expected := range want {
		found := slices.Contains(usageLines, expected)
		if !found {
			t.Fatalf("usage lines = %#v, missing %q", usageLines, expected)
		}
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
	if !slices.Contains(usageLines, "Cache R  5.0k (25%)") {
		t.Fatalf("usage lines = %#v, want cache read percent over input plus cache writes", usageLines)
	}
	if slices.Contains(usageLines, "Cache R  5.0k (50%)") {
		t.Fatalf("usage lines = %#v, cache read percent should not ignore cache writes", usageLines)
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
		"↑ 29.9M  ↓ 143.1k",
		"Cache R  3.5M (12%)",
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
	wantThinking := "Think    350.0k"
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

func TestRenderInfoPanelTodosUseSingleLineEllipsis(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{
		ID:      "1",
		Content: "Review sidebar todo wrapping\nSecond line should not be visible 👩‍💻👨‍👩‍👧‍👦",
		Status:  "in_progress",
	}}

	m := NewModel(backend)
	rendered := m.renderInfoPanel(20, 20)
	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "TODOS")
	if len(section) != 1 {
		t.Fatalf("todo section lines = %#v, want one line", section)
	}
	if strings.Contains(section[0], "Second line") {
		t.Fatalf("todo line should stay single-line, got %q", section[0])
	}
	if !strings.Contains(section[0], "…") {
		t.Fatalf("todo line should use ellipsis when truncated, got %q", section[0])
	}
	if got := ansi.StringWidth(section[0]); got > 18 {
		t.Fatalf("todo display width = %d, want <= 18; line=%q", got, section[0])
	}
	if utf8.ValidString(section[0]) == false {
		t.Fatalf("todo line should remain valid UTF-8, got %q", section[0])
	}
}

func TestRenderInfoPanelTodosPreserveInputOrder(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{
		{ID: "2", Content: "Phase 2", Status: "in_progress"},
		{ID: "3", Content: "Phase 3", Status: "pending"},
		{ID: "run", Content: "Run verification checks", Status: "pending"},
		{ID: "1", Content: "Phase 1", Status: "completed"},
	}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "TODOS")
	want := []string{
		"▶ Phase 2",
		"○ Phase 3",
		"○ Run verification checks",
		"✓ Phase 1",
	}
	if len(section) != len(want) {
		t.Fatalf("todo section lines = %#v, want %#v", section, want)
	}
	for i, line := range want {
		if section[i] != line {
			t.Fatalf("todo line %d = %q, want %q (section=%#v)", i, section[i], line, section)
		}
	}
}

func TestRenderInfoPanelCollapsedTodosShowsHeaderOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "Investigate spacing", Status: "in_progress"}}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionTodos] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "▶ TODOS")
	if len(section) != 0 {
		t.Fatalf("collapsed TODOS section should not render body lines, got %#v", section)
	}
}

func TestInfoPanelGitBlockHiddenWhenNoRepo(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	plain := stripANSI(m.renderInfoPanel(48, 24))
	if strings.Contains(plain, "GIT") {
		t.Fatalf("git block should be hidden when no git info is present, got:\n%s", plain)
	}
}

func TestInfoPanelGitBlockCollapsedByDefault(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitStatusInfo{Present: true, Branch: "main", Ahead: 14, ChangedFiles: 2, StagedFiles: 1, Stashes: 3}
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▶ GIT")
	if len(section) != 0 {
		t.Fatalf("collapsed git section should not render body lines, got %#v", section)
	}
	plain := stripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(plain, "▶ GIT") || !strings.Contains(plain, "main ↑14 +1 !2 *3") {
		t.Fatalf("git summary missing from collapsed header, got:\n%s", plain)
	}
}

func TestInfoPanelGitBlockExpanded(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitStatusInfo{Present: true, Branch: "main", WorktreeName: "fix-ui", Ahead: 2, Behind: 1, ChangedFiles: 3, StagedFiles: 1, Stashes: 2}
	m.toggleInfoPanelSection(infoPanelSectionGit)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT")
	joined := strings.Join(section, "\n")
	for _, want := range []string{"Branch: main", "Worktree: fix-ui", "Changes: 3 files", "Staged: 1 files", "Stash: 2 entries", "Sync: ↑2 ↓1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded git section missing %q in:\n%s", want, joined)
		}
	}
	plain := stripANSI(m.renderInfoPanel(48, 24))
	if strings.Contains(plain, "main@fix-ui ↑2 ↓1 !3") {
		t.Fatalf("expanded git header should omit summary, got:\n%s", plain)
	}
	rawSection := infoPanelSectionLines(infoPanelRawLines(m.renderInfoPanel(48, 24)), "▼ GIT")
	for _, line := range rawSection {
		if strings.HasPrefix(line, "Branch:") || strings.HasPrefix(line, "Worktree:") || strings.HasPrefix(line, "Changes:") || strings.HasPrefix(line, "Staged:") || strings.HasPrefix(line, "Stash:") || strings.HasPrefix(line, "Sync:") {
			t.Fatalf("expanded git content should be indented, got raw section %#v", rawSection)
		}
	}
}

func TestInfoPanelGitBlockExpandedHidesZeroNumericRows(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitStatusInfo{Present: true, Branch: "main", ChangedFiles: 2}
	m.toggleInfoPanelSection(infoPanelSectionGit)

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT")
	joined := strings.Join(section, "\n")
	for _, want := range []string{"Branch: main", "Changes: 2 files"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded git section missing %q in:\n%s", want, joined)
		}
	}
	for _, hidden := range []string{"Staged:", "Stash:", "Sync:", "0 files", "0 entries", "↑0", "↓0"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("expanded git section should hide zero value %q in:\n%s", hidden, joined)
		}
	}
}

func TestInfoPanelGitBlockExpandedShowsOneSidedSync(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitStatusInfo{Present: true, Branch: "main", Ahead: 2}
	m.toggleInfoPanelSection(infoPanelSectionGit)

	joined := strings.Join(infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT"), "\n")
	if !strings.Contains(joined, "Sync: ↑2") {
		t.Fatalf("expanded git section should show non-zero ahead count, got:\n%s", joined)
	}
	if strings.Contains(joined, "↓0") {
		t.Fatalf("expanded git section should hide zero behind count, got:\n%s", joined)
	}
}

func TestRenderInfoPanelCollapsedLSPShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}, {Name: "pyright", Pending: true}}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionLSP] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "▶ LSP")
	if len(section) != 0 {
		t.Fatalf("collapsed LSP section should not render body lines, got %#v", section)
	}
	plain := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "▶ LSP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed LSP header should include count, got %q", plain)
	}
}

func TestRenderInfoPanelCollapsedSkillsShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Discovered: true},
		{Name: "py-expert", Description: "Python development expert", Discovered: true},
	}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "▶ SKILLS")
	if len(section) != 0 {
		t.Fatalf("collapsed SKILLS section should not render body lines, got %#v", section)
	}
	plain := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "▶ SKILLS") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed SKILLS header should include count, got %q", plain)
	}
}

func TestRenderInfoPanelCollapsedMCPShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true}, {Name: "browser", Pending: true}}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionMCP] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "▶ MCP")
	if len(section) != 0 {
		t.Fatalf("collapsed MCP section should not render body lines, got %#v", section)
	}
	plain := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "▶ MCP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed MCP header should include count, got %q", plain)
	}
}

func TestRenderInfoPanelEnvironmentDotsPreserveInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Pending: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true}}

	m := NewModel(backend)
	rendered := m.renderInfoPanel(32, 20)

	pendingWant := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg)).Render("●")
	if !strings.Contains(rendered, pendingWant) {
		t.Fatalf("pending LSP dot should include info-panel background; want segment %q in %q", pendingWant, rendered)
	}
	pendingBad := "\x1b[38;5;240m●\x1b[m"
	if strings.Contains(rendered, pendingBad) {
		t.Fatalf("pending LSP dot should not be rendered without background; got %q in %q", pendingBad, rendered)
	}

	okWant := GaugeFull.Render("●")
	if !strings.Contains(rendered, okWant) {
		t.Fatalf("OK MCP dot should include info-panel background; want segment %q in %q", okWant, rendered)
	}

	lspSection := infoPanelSectionLines(infoPanelPlainLines(rendered), "LSP")
	if len(lspSection) == 0 || !strings.HasPrefix(lspSection[0], "● gopls") {
		t.Fatalf("LSP rows should remain visible in plain section extraction; got %#v", lspSection)
	}
	mcpSection := infoPanelSectionLines(infoPanelRawLines(rendered), "▼ MCP")
	if len(mcpSection) == 0 || mcpSection[0] != "   ● exa" {
		t.Fatalf("MCP rows should use collapsible content indent; got %#v", mcpSection)
	}
}

func TestRenderInfoPanelEditedFilesStatsPreserveInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)

	rendered := m.renderInfoPanel(48, 24)

	wantAdd := InfoPanelEditAddedStyle.Render("+2")
	wantSep := InfoPanelDim.Render(" ")
	wantRem := InfoPanelEditRemovedStyle.Render("-1")
	if !strings.Contains(rendered, wantAdd) {
		t.Fatalf("CHANGED FILES +N should use info-panel background; want segment %q in output", wantAdd)
	}
	if !strings.Contains(rendered, wantSep) {
		t.Fatalf("CHANGED FILES separator space should use info-panel background; want segment %q in output", wantSep)
	}
	if !strings.Contains(rendered, wantRem) {
		t.Fatalf("CHANGED FILES -N should use info-panel background; want segment %q in output", wantRem)
	}
	if !strings.Contains(rendered, wantAdd+wantSep+wantRem) {
		t.Fatalf("CHANGED FILES +N/-N sequence should preserve panel background across separator; want substring %q in output", wantAdd+wantSep+wantRem)
	}
	badAdd := SidebarAddedStyle.Render("+2")
	badRem := SidebarRemovedStyle.Render("-1")
	if strings.Contains(rendered, badAdd) {
		t.Fatalf("CHANGED FILES should not use sidebar-only add style (no panel bg); got bare segment %q", badAdd)
	}
	if strings.Contains(rendered, badRem) {
		t.Fatalf("CHANGED FILES should not use sidebar-only remove style (no panel bg); got bare segment %q", badRem)
	}

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ CHANGED FILES")
	if len(section) == 0 || !strings.HasPrefix(section[0], "foo.go +2 -1") {
		t.Fatalf("CHANGED FILES rows should remain visible in plain section extraction; got %#v", section)
	}
}

func TestRenderInfoPanelEditedFilesPrioritizesStatsWhenNarrow(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/app_update_input_row.go", 14, 4)

	block := stripANSI(m.buildInfoPanelFilesBlock(8))
	if !strings.Contains(block, "+14 -4") {
		t.Fatalf("narrow CHANGED FILES row should keep full stats, got %q", block)
	}
}

func TestRenderInfoPanelChangedFilesDeletedFileUsesStrikethroughWithoutStats(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileDelete("main", "/tmp/obsolete.go")

	rendered := m.renderInfoPanel(48, 24)
	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ CHANGED FILES")
	if len(section) == 0 || !strings.HasPrefix(section[0], "obsolete.go") {
		t.Fatalf("deleted changed-file row missing; section=%#v", section)
	}
	if strings.Contains(section[0], "-1") || strings.Contains(section[0], "+0") {
		t.Fatalf("deleted changed-file row should not show fake stats: %#v", section)
	}
	if !strings.Contains(rendered, "\x1b[9m") && !strings.Contains(rendered, ";9m") {
		t.Fatalf("deleted changed-file row should render with strikethrough, got %q", rendered)
	}
}

func TestRenderInfoPanelEditedFilesLimitsVisibleRows(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	for i := 1; i <= infoPanelEditedFilesLimit+3; i++ {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%02d.go", i), i, 0)
	}

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 40)), "▼ CHANGED FILES")
	if got, want := len(section), infoPanelEditedFilesLimit+1; got != want {
		t.Fatalf("CHANGED FILES rows = %d, want %d; section=%#v", got, want, section)
	}
	if !strings.HasPrefix(section[0], "file-01.go +1") {
		t.Fatalf("first visible edited file = %q, want file-01.go", section[0])
	}
	lastFile := fmt.Sprintf("file-%02d.go +%d", infoPanelEditedFilesLimit, infoPanelEditedFilesLimit)
	if !strings.HasPrefix(section[infoPanelEditedFilesLimit-1], lastFile) {
		t.Fatalf("last visible edited file = %q, want prefix %q", section[infoPanelEditedFilesLimit-1], lastFile)
	}
	if got, want := section[infoPanelEditedFilesLimit], "… and 3 more"; got != want {
		t.Fatalf("overflow row = %q, want %q", got, want)
	}
	if strings.Contains(strings.Join(section, "\n"), "file-21.go") {
		t.Fatalf("CHANGED FILES should hide files beyond limit; section=%#v", section)
	}
}

func TestRenderInfoPanelCollapsedEditedFilesShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)
	m.sidebar.AddFileEdit("main", "/tmp/bar.go", 1, 0)
	m.infoPanelCollapsedSections[infoPanelSectionFiles] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▶ CHANGED FILES")
	if len(section) != 0 {
		t.Fatalf("collapsed CHANGED FILES section should not render body lines, got %#v", section)
	}
}

func TestRenderInfoPanelAgentsPreserveInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.sidebar.UpdateActivity("main", "Streaming")
	rendered := m.renderInfoPanel(48, 24)

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ AGENTS")
	if len(section) < 2 {
		t.Fatalf("AGENTS section = %#v, want rows", section)
	}
	if section[0] != "● builder" {
		t.Fatalf("AGENTS main row = %q, want %q", section[0], "● builder")
	}
	if section[1] != "○ ship tests" {
		t.Fatalf("AGENTS sub-agent row = %q, want %q", section[1], "○ ship tests")
	}

	if !strings.Contains(rendered, InfoPanelAgentFocusedStyle.Render("● builder")) {
		t.Fatalf("AGENTS focused line should use info-panel-aware style; want styled content in output")
	}
	entryPrefix := InfoPanelAgentEntryStyle.Render("○ ship tests")
	if !strings.Contains(rendered, entryPrefix) {
		t.Fatalf("AGENTS sub-agent line should preserve info-panel background with collapsible content indent; want %q in output", entryPrefix)
	}
}

func TestRenderInfoPanelAgentsShowCompactingActivityWithoutStatusBarIconDuplication(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.sidebar.UpdateActivity("main", "Compacting context...")
	rendered := m.renderInfoPanel(56, 24)

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ AGENTS")
	if len(section) < 1 {
		t.Fatalf("AGENTS section = %#v, want main row", section)
	}
	if section[0] != "● builder" {
		t.Fatalf("AGENTS main row = %q, want %q", section[0], "● builder")
	}
	if strings.Contains(section[0], "↺") {
		t.Fatalf("AGENTS row should not duplicate status-bar icon, got %q", section[1])
	}
}

func TestRenderInfoPanelAgentsRefreshWhenFocusChangesWithoutModelChange(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")

	before := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ AGENTS")
	if len(before) < 2 {
		t.Fatalf("AGENTS section before focus switch = %#v, want rows", before)
	}
	if before[0] != "● builder" {
		t.Fatalf("AGENTS main row before focus switch = %q, want %q", before[0], "● builder")
	}
	if before[1] != "○ ship tests" {
		t.Fatalf("AGENTS sub-agent row before focus switch = %q, want %q", before[1], "○ ship tests")
	}

	m.setFocusedAgent("agent-1")
	after := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ AGENTS")
	if len(after) < 2 {
		t.Fatalf("AGENTS section after focus switch = %#v, want rows", after)
	}
	if after[0] != "○ builder" {
		t.Fatalf("AGENTS main row after focus switch = %q, want %q", after[0], "○ builder")
	}
	if after[1] != "● ship tests" {
		t.Fatalf("AGENTS sub-agent row after focus switch = %q, want %q", after[1], "● ship tests")
	}
}

func TestRenderInfoPanelCollapsedAgentsShowsHeaderOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.infoPanelCollapsedSections[infoPanelSectionAgents] = true

	plain := stripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(plain, "▶ AGENTS") || !strings.Contains(plain, "0/1") {
		t.Fatalf("collapsed AGENTS header should include compact count, got %q", plain)
	}
	section := infoPanelSectionLines(infoPanelPlainLines(plain), "▶ AGENTS")
	if len(section) != 0 {
		t.Fatalf("collapsed AGENTS section should not render body lines, got %#v", section)
	}
}

func TestRenderInfoPanelPendingAgentPlaceholderUsesIconOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddPendingTask()

	plain := stripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(plain, "▼ AGENTS") || !strings.Contains(plain, "0/1") {
		t.Fatalf("AGENTS header should include pending placeholder in total, got %q", plain)
	}
	section := infoPanelSectionLines(infoPanelPlainLines(plain), "▼ AGENTS")
	if len(section) < 2 {
		t.Fatalf("AGENTS section = %#v, want main row + pending placeholder", section)
	}
	if section[0] != "● builder" {
		t.Fatalf("AGENTS main row = %q, want %q", section[0], "● builder")
	}
	if section[1] != "◌" {
		t.Fatalf("AGENTS pending row = %q, want %q", section[1], "◌")
	}
	if strings.Contains(strings.Join(section, "\n"), "launching...") {
		t.Fatalf("AGENTS pending row should not show launching text, got %#v", section)
	}
}

func TestRenderInfoPanelAgentsApplyConfiguredColorToNonFocusedRows(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", Color: "196"}}, "main", "builder")

	rendered := m.renderInfoPanel(48, 24)
	want := infoPanelAgentRowStyle(SidebarEntry{ID: "agent-1", Color: "196"}, false).Render("○ agent-1")
	if !strings.Contains(rendered, want) {
		t.Fatalf("AGENTS non-focused row should use configured color; want %q in %q", want, rendered)
	}
}

func TestRenderInfoPanelAgentsRefreshWhenSelectedRefVariantChanges(t *testing.T) {
	// Variant info is no longer rendered as a sub-line in the info panel
	// (it's shown in the pool selector label instead). This test now
	// verifies that the agent row refreshes correctly when SelectedRef changes.
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{
		InstanceID:  "agent-1",
		TaskDesc:    "ship tests",
		SelectedRef: "openai/gpt-5.5@high",
	}}, "main", "builder")

	before := stripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(before, "ship tests") {
		t.Fatalf("info panel before variant change = %q, want ship tests", before)
	}

	m.sidebar.Update([]agent.SubAgentInfo{{
		InstanceID:  "agent-1",
		TaskDesc:    "ship tests",
		SelectedRef: "openai/gpt-5.5@low",
	}}, "main", "builder")
	after := stripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(after, "ship tests") {
		t.Fatalf("info panel after variant change = %q, want ship tests", after)
	}
}

func TestRenderInfoPanelLSPRowsShowInlineDiagnostics(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true, Errors: 2, Warnings: 1}}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "LSP")
	joined := strings.Join(section, "\n")
	if strings.Contains(joined, "LSP:") {
		t.Fatalf("LSP section should not contain a colon-suffixed subheader; got %q", joined)
	}
	if !strings.Contains(joined, "gopls 2 E, 1 W") {
		t.Fatalf("LSP row should include inline diagnostics suffix; got %q", joined)
	}
}

func TestRenderInfoPanelEnvironmentDotsUseThemeInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Pending: true}}

	m := NewModel(backend)
	rendered := m.renderInfoPanel(32, 20)
	want := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg)).Render("●")
	if !strings.Contains(rendered, want) {
		t.Fatalf("pending LSP dot should use themed info-panel background; want %q in %q", want, rendered)
	}
}

func TestRenderInfoPanelIdleEnvironmentRowsUseDimNotCriticalStyles(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Idle: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Idle: true}}

	m := NewModel(backend)
	rendered := m.renderInfoPanel(40, 20)
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "gopls") || !strings.Contains(plain, "exa") {
		t.Fatalf("idle environment rows missing from info panel, got %q", plain)
	}
	if strings.Contains(rendered, InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg)).Render(" gopls")) {
		t.Fatalf("idle LSP row should not use critical label style, got %q", rendered)
	}
	if strings.Contains(rendered, InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg)).Render(" exa")) {
		t.Fatalf("idle MCP row should not use critical label style, got %q", rendered)
	}
}

func TestRenderInfoPanelLSPErrorHidesRawMessageAndRefreshesWhenStatusChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Err: "dial tcp", OK: false}}

	m := NewModel(backend)
	before := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(before, "gopls") {
		t.Fatalf("info panel before LSP error change = %q, want server name", before)
	}
	if strings.Contains(before, "dial tcp") {
		t.Fatalf("info panel should not show raw LSP error text, got %q", before)
	}

	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Err: "context deadline exceeded", OK: false}}
	after := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(after, "gopls") {
		t.Fatalf("info panel after LSP error change = %q, want server name", after)
	}
	if strings.Contains(after, "context deadline") || strings.Contains(after, "dial tcp") {
		t.Fatalf("info panel should continue hiding raw LSP error text, got %q", after)
	}
}

func TestRenderInfoPanelMCPErrorRefreshesWhenMessageChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "codex_apps", Err: "timeout", OK: false}}

	m := NewModel(backend)
	before := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(before, "codex_apps") {
		t.Fatalf("info panel before MCP error change = %q, want server name", before)
	}
	if strings.Contains(before, "timeout") {
		t.Fatalf("info panel should not show raw MCP error text, got %q", before)
	}

	backend.mcpRows = []agent.MCPServerDisplay{{Name: "codex_apps", Err: "connection refused", OK: false}}
	after := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(after, "codex_apps") {
		t.Fatalf("info panel after MCP error change = %q, want server name", after)
	}
	if strings.Contains(after, "connection refused") || strings.Contains(after, "timeout") {
		t.Fatalf("info panel should continue hiding raw MCP error text, got %q", after)
	}
}

func TestRenderInfoPanelMCPRetryingUsesWarningLabel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Pending: true, Retrying: true, Attempt: 2, MaxAttempts: 3}}

	m := NewModel(backend)
	got := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(got, "exa") {
		t.Fatalf("retrying MCP row = %q, want server name", got)
	}

	rendered := m.renderInfoPanel(40, 20)
	wantWarning := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelWarningFg)).Render(" exa")
	if !strings.Contains(rendered, wantWarning) {
		t.Fatalf("retrying MCP row should use warning label style; want %q in %q", wantWarning, rendered)
	}
	if strings.Contains(rendered, InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg)).Render(" exa")) {
		t.Fatalf("retrying MCP row should not use critical red label style; got %q", rendered)
	}
}

func TestRenderInfoPanelMCPPendingUsesDimLabel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Pending: true, Retrying: false}}

	m := NewModel(backend)
	rendered := m.renderInfoPanel(40, 20)
	wantDim := InfoPanelDim.Render(" exa")
	if !strings.Contains(rendered, wantDim) {
		t.Fatalf("pending MCP row should use dim label style; want %q in %q", wantDim, rendered)
	}
	wantWarning := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelWarningFg)).Render(" exa")
	if strings.Contains(rendered, wantWarning) {
		t.Fatalf("pending MCP row should not use warning label style; got %q", rendered)
	}
}

func TestRenderLSPDiagSummaryUsesInfoPanelBackgroundOnAllSegments(t *testing.T) {
	got := renderLSPDiagSummary("3 E, 1 W")
	wantError := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelDiagErrorFg)).Render("3 E")
	wantWarn := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelDiagWarnFg)).Render("1 W")
	wantSep := InfoPanelDim.Render(", ")

	if !strings.Contains(got, wantError) {
		t.Fatalf("error segment should include info-panel background; want %q in %q", wantError, got)
	}
	if !strings.Contains(got, wantWarn) {
		t.Fatalf("warn segment should include info-panel background; want %q in %q", wantWarn, got)
	}
	if !strings.Contains(got, wantSep) {
		t.Fatalf("separator should include info-panel background; want %q in %q", wantSep, got)
	}
	badError := "\x1b[38;5;196m3 E\x1b[m"
	if strings.Contains(got, badError) {
		t.Fatalf("diagnostic error segment should not be rendered without background; got %q in %q", badError, got)
	}
}

func TestFormatLSPServerDiagSuffix(t *testing.T) {
	cases := []struct {
		name     string
		errors   int
		warnings int
		want     string
	}{
		{name: "none", want: ""},
		{name: "errors only", errors: 2, want: "2 E"},
		{name: "warnings only", warnings: 3, want: "3 W"},
		{name: "both", errors: 1, warnings: 2, want: "1 E, 2 W"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLSPServerDiagSuffix(tc.errors, tc.warnings); got != tc.want {
				t.Fatalf("formatLSPServerDiagSuffix(%d, %d) = %q, want %q", tc.errors, tc.warnings, got, tc.want)
			}
		})
	}
}

func TestRenderLSPDiagSummaryFollowsThemeInfoPanelBackground(t *testing.T) {
	got := renderLSPDiagSummary("3 E, 1 W")
	wantError := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelDiagErrorFg)).Render("3 E")
	if !strings.Contains(got, wantError) {
		t.Fatalf("diagnostic summary should use themed info-panel background; want %q in %q", wantError, got)
	}
}

func TestToolResultStatusFromRestoredContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    agent.ToolResultStatus
	}{
		{name: "empty", content: "", want: agent.ToolResultStatusSuccess},
		{name: "cancelled", content: "Cancelled", want: agent.ToolResultStatusCancelled},
		{name: "error", content: "Error: exit code 1", want: agent.ToolResultStatusError},
		{name: "success", content: "written successfully", want: agent.ToolResultStatusSuccess},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolResultStatusFromRestoredContent(tc.content); got != tc.want {
				t.Fatalf("toolResultStatusFromRestoredContent(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestRenderInfoPanelRateLimitHidesPrimaryWithNoMeaningfulData(t *testing.T) {
	cases := []struct {
		name   string
		window ratelimit.RateLimitWindow
	}{
		{
			name:   "zero without reset",
			window: ratelimit.RateLimitWindow{UsedPct: 0, WindowMinutes: 5 * 60},
		},
		{
			name:   "unknown with reset",
			window: ratelimit.RateLimitWindow{UsedPct: -1, WindowMinutes: 5 * 60, ResetsAt: time.Now().Add(time.Hour)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newInfoPanelAgent()
			window := tc.window
			backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{Primary: &window}

			m := NewModel(backend)
			section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "RATE LIMIT")
			if len(section) != 0 {
				t.Fatalf("rate limit section = %#v, want hidden", section)
			}
		})
	}
}

func TestRenderInfoPanelRateLimitHidesSecondaryWhenZeroAndDropsPrimaryLabel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{
			UsedPct:       42,
			WindowMinutes: 5 * 60,
			ResetsAt:      time.Now().Add(2*time.Hour + 30*time.Minute),
		},
		Secondary: &ratelimit.RateLimitWindow{
			UsedPct:       0,
			WindowMinutes: 7 * 24 * 60,
			ResetsAt:      time.Now().Add(3 * 24 * time.Hour),
		},
	}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "RATE LIMIT")
	if len(section) != 1 {
		t.Fatalf("rate limit lines = %#v, want one line", section)
	}
	if got := section[0]; got != "42% 2h30m" {
		t.Fatalf("rate limit line = %q, want %q", got, "42% 2h30m")
	}
}

func TestRenderInfoPanelRateLimitShowsWindowLabelsAndUnifiedResetFormat(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.rateLimitSnapshot = &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{
			UsedPct:       42,
			WindowMinutes: 5 * 60,
			ResetsAt:      time.Now().Add(2*time.Hour + 30*time.Minute),
		},
		Secondary: &ratelimit.RateLimitWindow{
			UsedPct:       75,
			WindowMinutes: 7 * 24 * 60,
			ResetsAt:      time.Now().Add(3*24*time.Hour + 4*time.Hour),
		},
	}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "RATE LIMIT")
	want := []string{"5h: 42% 2h30m", "1w: 75% 3d4h"}
	if len(section) != len(want) {
		t.Fatalf("rate limit lines = %#v, want %#v", section, want)
	}
	for i, line := range want {
		if section[i] != line {
			t.Fatalf("rate limit line %d = %q, want %q", i, section[i], line)
		}
	}
}

func TestFormatRateLimitResetTimeUsesDayHourAndHourMinute(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "expired", d: -1 * time.Second, want: ""},
		{name: "under1m", d: 45 * time.Second, want: "45s"},
		{name: "under24h", d: 2*time.Hour + 30*time.Minute, want: "2h30m"},
		{name: "over24h", d: 49*time.Hour + 15*time.Minute, want: "2d1h"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := ratelimit.RateLimitWindow{ResetsAt: time.Now().Add(tc.d)}
			if got := formatRateLimitResetTime(w); got != tc.want {
				t.Fatalf("formatRateLimitResetTime(%s) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestNextRateLimitSnapshotDisplayTransitionUsesSecondAndMinuteGranularity(t *testing.T) {
	now := time.Now()
	snap := &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{ResetsAt: now.Add(45*time.Second + 200*time.Millisecond)},
	}
	if got := nextRateLimitSnapshotDisplayTransition(snap, now); got <= 0 || got > time.Second {
		t.Fatalf("second-granularity transition = %v, want within 1s", got)
	}

	snap = &ratelimit.KeyRateLimitSnapshot{
		Primary: &ratelimit.RateLimitWindow{ResetsAt: now.Add(2*time.Hour + 30*time.Minute + 15*time.Second)},
	}
	if got := nextRateLimitSnapshotDisplayTransition(snap, now); got < 44*time.Second || got > 46*time.Second {
		t.Fatalf("minute-granularity transition = %v, want about 45s", got)
	}
}

func TestRenderInfoPanelModelLineShowsRunningModelRefVerbatim(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.runningModelRef = "sample/gpt-5.5@xhigh"
	m := NewModel(backend)
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 1 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "sample/gpt-5.5@xhigh" {
		t.Fatalf("model line = %q, want sample/gpt-5.5@xhigh", got)
	}
}

func TestRenderInfoPanelModelLineDoesNotLeakVariantToFallbackModel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "sample/gpt-5.5@xhigh"
	backend.runningModelRef = "sample/glm-5.1"
	backend.runningVariant = "xhigh"
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 1 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "sample/glm-5.1" {
		t.Fatalf("model line = %q, want sample/glm-5.1", got)
	}
}

func TestRenderInfoPanelModelLineShowsFallbackVariantWhenRunningRefIncludesVariant(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "sample/gpt-5.5@xhigh"
	backend.runningModelRef = "sample/glm-5.1@high"
	backend.runningVariant = "xhigh"
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 1 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "sample/glm-5.1@high" {
		t.Fatalf("model line = %q, want sample/glm-5.1@high", got)
	}
}

func TestRenderInfoPanelIdleModelLineUsesNextRequestModel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "provider-a/model-a"
	backend.runningModelRef = "provider-b/model-b"
	backend.nextRequestModelRef = "provider-b/model-b"
	backend.keysConfirmed = 5
	backend.keysTotal = 5
	m := NewModel(backend)

	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 2 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "provider-b/model-b" {
		t.Fatalf("model line = %q, want provider-b/model-b", got)
	}
	if got := modelLines[1]; got != "Keys: 5/5" {
		t.Fatalf("keys line = %q, want Keys: 5/5", got)
	}
}

func TestRenderInfoPanelBusyModelLineIgnoresNextRequestModel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.providerModelRef = "provider-a/model-a"
	backend.runningModelRef = "provider-a/model-a"
	backend.nextRequestModelRef = "provider-b/model-b"
	m := NewModel(backend)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 1 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "provider-a/model-a" {
		t.Fatalf("model line = %q, want provider-a/model-a", got)
	}
}

func infoPanelRawLines(rendered string) []string {
	raw := strings.Split(stripANSI(rendered), "\n")
	for i, line := range raw {
		raw[i] = strings.TrimRight(line, " ")
	}
	return raw
}

func infoPanelPlainLines(rendered string) []string {
	raw := infoPanelRawLines(rendered)
	lines := make([]string, len(raw))
	for i, line := range raw {
		lines[i] = strings.TrimSpace(line)
	}
	return lines
}

func infoPanelSectionLines(lines []string, title string) []string {
	want := normalizeInfoPanelSectionTitle(title)
	for i, line := range lines {
		if normalizeInfoPanelSectionTitle(line) != want {
			continue
		}
		var section []string
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			if next == "" {
				k := j + 1
				for k < len(lines) && lines[k] == "" {
					k++
				}
				if k >= len(lines) || isInfoPanelSectionTitle(lines[k]) {
					break
				}
				section = append(section, "")
				continue
			}
			if isInfoPanelSectionTitle(next) {
				break
			}
			section = append(section, next)
		}
		return section
	}
	return nil
}

func TestRenderInfoPanelCollapsibleSectionsIndentContentNotHeaders(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "Investigate spacing", Status: "in_progress"}}
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true}}
	backend.availableSkills = []*skill.Meta{{Name: "go-expert", Discovered: true}}
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)

	rawLines := infoPanelRawLines(m.renderInfoPanel(48, 80))
	cases := []struct {
		title string
		want  string
	}{
		{title: "▼ LSP", want: "   ● gopls"},
		{title: "▼ MCP", want: "   ● exa"},
		{title: "▼ TODOS", want: "   ▶ Investigate spacing"},
		{title: "▼ SKILLS", want: "   go-expert"},
		{title: "▼ CHANGED FILES", want: "   foo.go +2 -1"},
		{title: "▼ AGENTS", want: "   ● builder"},
	}
	for _, tc := range cases {
		section := infoPanelSectionLines(rawLines, tc.title)
		if len(section) == 0 {
			t.Fatalf("section %q missing body lines in raw output: %#v", tc.title, rawLines)
		}
		if section[0] != tc.want {
			t.Fatalf("section %q first body line = %q, want %q", tc.title, section[0], tc.want)
		}
	}
}

func isInfoPanelSectionTitle(line string) bool {
	return normalizeInfoPanelSectionTitle(line) != ""
}

func normalizeInfoPanelSectionTitle(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "▼ ") || strings.HasPrefix(line, "▶ ") {
		line = strings.TrimSpace(line[3:])
	}
	switch {
	case line == "MODEL":
		return "MODEL"
	case line == "RATE LIMIT":
		return "RATE LIMIT"
	case line == "USAGE":
		return "USAGE"
	case strings.HasPrefix(line, "MCP"):
		return "MCP"
	case strings.HasPrefix(line, "GIT"):
		return "GIT"
	case line == "AGENTS" || strings.HasPrefix(line, "AGENTS"):
		return "AGENTS"
	case strings.HasPrefix(line, "LSP"):
		return "LSP"
	case strings.HasPrefix(line, "SKILLS"):
		return "SKILLS"
	case strings.HasPrefix(line, "TODOS"):
		return "TODOS"
	case strings.HasPrefix(line, "CHANGED FILES"):
		return "CHANGED FILES"
	default:
		return ""
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
		plain := stripANSI(m.renderInfoPanel(width, 20))
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
