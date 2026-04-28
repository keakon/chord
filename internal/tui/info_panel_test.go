package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	contextCurrent      int
	contextLimit        int
	contextMessageCount int
	todos               []tools.TodoItem
	rateLimitSnapshot   *ratelimit.KeyRateLimitSnapshot
	lspRows             []agent.LSPServerDisplay
	mcpRows             []agent.MCPServerDisplay
	availableSkills     []*skill.Meta
	invokedSkills       []*skill.Meta
	keysConfirmed       int
	keysTotal           int
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

func (a *infoPanelAgent) KeyStats() (confirmed, total int) {
	return a.keysConfirmed, a.keysTotal
}

func (a *infoPanelAgent) LSPServerList() []agent.LSPServerDisplay {
	return append([]agent.LSPServerDisplay(nil), a.lspRows...)
}

func (a *infoPanelAgent) MCPServerList() []agent.MCPServerDisplay {
	return append([]agent.MCPServerDisplay(nil), a.mcpRows...)
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
	if strings.Contains(plain, "COST") || strings.Contains(plain, "TOKEN USAGE") {
		t.Fatalf("rendered info panel should collapse legacy usage blocks; got %q", plain)
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
	if strings.Contains(plain, "TOKEN USAGE") || strings.Contains(plain, "CONTEXT USAGE") || strings.Contains(plain, "COST") {
		t.Fatalf("rendered info panel should not render legacy usage blocks; got %q", plain)
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
	if !strings.Contains(plain, "Context: 2.0k (1%)") {
		t.Fatalf("rendered info panel should show context summary inside USAGE; got %q", plain)
	}
}

func TestRenderInfoPanelUsageGroupsContextMessagesAndCache(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 159_100
	backend.contextMessageCount = 360
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
		"Messages: 360",
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
		"Cache R  2.3k (23%)",
		"Cache W  640",
	}
	for _, expected := range want {
		found := false
		for _, got := range usageLines {
			if got == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("usage lines = %#v, missing %q", usageLines, expected)
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

func TestRenderInfoPanelUsageStandardWidthShowsCostInTokenSummary(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.usage = analytics.SessionStats{
		InputTokens:   2_400_000,
		OutputTokens:  88_000,
		EstimatedCost: 1.2345,
	}

	m := NewModel(backend)
	usageLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "USAGE")
	wantSummary := "↑ 2.4M  ↓ 88.0k  $ 1.2345"
	found := false
	for _, line := range usageLines {
		if line == wantSummary {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("usage lines = %#v, want token summary %q", usageLines, wantSummary)
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
		t.Fatalf("EDITED FILES +N should use info-panel background; want segment %q in output", wantAdd)
	}
	if !strings.Contains(rendered, wantSep) {
		t.Fatalf("EDITED FILES separator space should use info-panel background; want segment %q in output", wantSep)
	}
	if !strings.Contains(rendered, wantRem) {
		t.Fatalf("EDITED FILES -N should use info-panel background; want segment %q in output", wantRem)
	}
	if !strings.Contains(rendered, wantAdd+wantSep+wantRem) {
		t.Fatalf("EDITED FILES +N/-N sequence should preserve panel background across separator; want substring %q in output", wantAdd+wantSep+wantRem)
	}
	badAdd := SidebarAddedStyle.Render("+2")
	badRem := SidebarRemovedStyle.Render("-1")
	if strings.Contains(rendered, badAdd) {
		t.Fatalf("EDITED FILES should not use sidebar-only add style (no panel bg); got bare segment %q", badAdd)
	}
	if strings.Contains(rendered, badRem) {
		t.Fatalf("EDITED FILES should not use sidebar-only remove style (no panel bg); got bare segment %q", badRem)
	}

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ EDITED FILES")
	if len(section) == 0 || !strings.HasPrefix(section[0], "foo.go +2 -1") {
		t.Fatalf("EDITED FILES rows should remain visible in plain section extraction; got %#v", section)
	}
}

func TestRenderInfoPanelCollapsedEditedFilesShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)
	m.sidebar.AddFileEdit("main", "/tmp/bar.go", 1, 0)
	m.infoPanelCollapsedSections[infoPanelSectionFiles] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▶ EDITED FILES")
	if len(section) != 0 {
		t.Fatalf("collapsed EDITED FILES section should not render body lines, got %#v", section)
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
	// (it's shown in the model selector label instead). This test now
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
	if strings.Contains(joined, "Diagnostics:") {
		t.Fatalf("LSP section should not render legacy Diagnostics row; got %q", joined)
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
	modelLines := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "MODEL")
	if len(modelLines) < 1 {
		t.Fatalf("MODEL section missing lines: %#v", modelLines)
	}
	if got := modelLines[0]; got != "sample/glm-5.1@high" {
		t.Fatalf("model line = %q, want sample/glm-5.1@high", got)
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

	rawLines := infoPanelRawLines(m.renderInfoPanel(48, 24))
	cases := []struct {
		title string
		want  string
	}{
		{title: "▼ LSP", want: "   ● gopls"},
		{title: "▼ MCP", want: "   ● exa"},
		{title: "▼ TODOS", want: "   ▶ Investigate spacing"},
		{title: "▼ SKILLS", want: "   go-expert"},
		{title: "▼ EDITED FILES", want: "   foo.go +2 -1"},
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
	case line == "AGENTS" || strings.HasPrefix(line, "AGENTS"):
		return "AGENTS"
	case strings.HasPrefix(line, "LSP"):
		return "LSP"
	case strings.HasPrefix(line, "SKILLS"):
		return "SKILLS"
	case strings.HasPrefix(line, "TODOS"):
		return "TODOS"
	case strings.HasPrefix(line, "EDITED FILES"):
		return "EDITED FILES"
	default:
		return ""
	}
}

func TestInfoPanelBreakpoint(t *testing.T) {
	tests := []struct {
		width    int
		wantTier int
	}{
		{10, 0},
		{20, 0},
		{23, 0},
		{24, 1},
		{28, 1},
		{31, 1},
		{32, 2},
		{36, 2},
		{39, 2},
		{40, 3},
		{50, 3},
		{80, 3},
	}
	for _, tc := range tests {
		got := infoPanelBreakpoint(tc.width)
		if got != tc.wantTier {
			t.Errorf("infoPanelBreakpoint(%d) = %d, want %d", tc.width, got, tc.wantTier)
		}
	}
}

// TestInfoPanelBreakpointTier0HidesContextDetails verifies that at width < 24
// the USAGE section omits Context gauge, Messages, and cache details.
func TestInfoPanelBreakpointTier0HidesContextDetails(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 128_000
	backend.usage = analytics.SessionStats{
		InputTokens:     500_000,
		OutputTokens:    10_000,
		CacheReadTokens: 20_000,
	}

	m := NewModel(backend)
	// width=23, which is tier 0.
	plain := stripANSI(m.renderInfoPanel(23, 20))
	if strings.Contains(plain, "Context") {
		t.Fatalf("tier 0 should hide Context details, got %q", plain)
	}
	if strings.Contains(plain, "Messages") {
		t.Fatalf("tier 0 should hide Messages line, got %q", plain)
	}
	if strings.Contains(plain, "Cache") {
		t.Fatalf("tier 0 should hide cache details, got %q", plain)
	}
	// Token summary should still be visible.
	if !strings.Contains(plain, "TOKENS") {
		t.Fatalf("tier 0 should still show TOKENS header, got %q", plain)
	}
}

// TestInfoPanelBreakpointTier1ShowsContextHidesCache verifies that at width 24-31
// the USAGE section shows Context + gauge but hides cache details.
func TestInfoPanelBreakpointTier1ShowsContextHidesCache(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 128_000
	backend.usage = analytics.SessionStats{
		InputTokens:     500_000,
		OutputTokens:    10_000,
		CacheReadTokens: 20_000,
	}

	m := NewModel(backend)
	// width=30, which is tier 1.
	plain := stripANSI(m.renderInfoPanel(30, 20))
	if !strings.Contains(plain, "Context") {
		t.Fatalf("tier 1 should show Context, got %q", plain)
	}
	if strings.Contains(plain, "Cache") {
		t.Fatalf("tier 1 should hide cache details, got %q", plain)
	}
}

// TestInfoPanelBreakpointTier2ShowsFullDetails verifies that at width 32+
// the USAGE section shows everything including cache details.
func TestInfoPanelBreakpointTier2ShowsFullDetails(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 50_000
	backend.contextLimit = 128_000
	backend.usage = analytics.SessionStats{
		InputTokens:     500_000,
		OutputTokens:    10_000,
		CacheReadTokens: 20_000,
	}

	m := NewModel(backend)
	// width=32, which is tier 2.
	plain := stripANSI(m.renderInfoPanel(32, 20))
	if !strings.Contains(plain, "Context") {
		t.Fatalf("tier 2+ should show Context, got %q", plain)
	}
	if !strings.Contains(plain, "Cache R") {
		t.Fatalf("tier 2+ should show cache details, got %q", plain)
	}
}
