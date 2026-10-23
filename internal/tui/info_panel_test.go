package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/ratelimit"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

type infoPanelAgent struct {
	*sessionControlAgent
	usage               analytics.SessionStats
	runningModelRef     string
	nextRequestModelRef string
	contextCurrent      int
	contextBytes        int
	contextLimit        int
	// contextUsageState overrides the state derived from contextCurrent, so a
	// test can render the frozen-estimate variant without an observation.
	contextUsageState ctxmgr.ContextUsageState
	contextReminder   float64
	contextThreshold  float64
	// contextLinesForRef optionally overrides ContextPressureLinesForModelRef
	// per queried ref so tests can distinguish which model the display
	// resolved (nil falls back to contextReminder/contextThreshold for any ref).
	contextLinesForRef  func(ref string) (reminder, threshold float64)
	contextMessageCount int
	contextReduction    agent.ContextReductionStats
	todos               []tools.TodoItem
	rateLimitSnapshot   *ratelimit.KeyRateLimitSnapshot
	lspRows             []agent.LSPServerDisplay
	mcpRows             []agent.MCPServerDisplay
	availableSkills     []*skill.Meta
	skillsByFocus       map[string][]*skill.Meta
	invokedSkills       []*skill.Meta
	// skillStates, when non-nil, replaces the derived invocation states so
	// a test can observe states only session history produces (discovery
	// notices, bodies archived by compaction).
	skillStates        []skill.InvocationState
	skillRuleset       permission.Ruleset
	keysConfirmed      int
	keysTotal          int
	wakeRateLimitCalls int
	walltimeStats      analytics.WalltimeStats
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

func (a *infoPanelAgent) GetSidebarWalltimeStats() analytics.WalltimeStats {
	return a.walltimeStats
}

func (a *infoPanelAgent) GetContextStats() (current, limit int) {
	return a.contextCurrent, a.contextLimit
}

func (a *infoPanelAgent) GetContextUsageState() ctxmgr.ContextUsageState {
	if a.contextUsageState != "" {
		return a.contextUsageState
	}
	if a.contextCurrent > 0 {
		return ctxmgr.ContextUsageObserved
	}
	return ctxmgr.ContextUsageUnknown
}

func (a *infoPanelAgent) ContextPressureLinesForModelRef(modelRef string) (reminder, threshold float64) {
	if a.contextLinesForRef != nil {
		return a.contextLinesForRef(modelRef)
	}
	return a.contextReminder, a.contextThreshold
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

func (a *infoPanelAgent) UserSkills() []*skill.Meta {
	return append([]*skill.Meta(nil), a.availableSkills...)
}

func (a *infoPanelAgent) FocusedSkills() []*skill.Meta {
	if skills, ok := a.skillsByFocus[a.focused]; ok {
		return append([]*skill.Meta(nil), skills...)
	}
	return a.UserSkills()
}

func (a *infoPanelAgent) FocusedSkillInvocationStates() []skill.InvocationState {
	if a.skillStates != nil {
		return append([]skill.InvocationState(nil), a.skillStates...)
	}
	loaded := make(map[string]struct{}, len(a.invokedSkills))
	for _, meta := range a.invokedSkills {
		if meta != nil && strings.TrimSpace(meta.Name) != "" {
			loaded[meta.Name] = struct{}{}
		}
	}
	return skill.InvocationStates(a.FocusedSkills(), a.skillRuleset, loaded)
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

func TestRenderInfoPanelCollapsibleHeaderUsesExtraSpaceAfterSummaryDot(t *testing.T) {
	got := strings.TrimSpace(terminaltext.StripANSI(renderInfoPanelCollapsibleHeader(24, true, "LSP", "3")))
	if got != "▼ LSP · 3" {
		t.Fatalf("collapsible header = %q, want %q", got, "▼ LSP · 3")
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

func TestRenderInfoPanelTransientSectionsKeepStableSectionsInPlace(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.contextCurrent = 12_300
	backend.contextMessageCount = 4
	backend.walltimeStats = analytics.WalltimeStats{Model: 2 * time.Second, Tool: time.Second}
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}}

	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/main.go", 3, 1)

	stableTitles := []string{"USAGE", "TIME", "▼ LSP", "▼ CHANGED FILES"}
	stableIndex := func(lines []string) map[string]int {
		index := make(map[string]int, len(stableTitles))
		for _, title := range stableTitles {
			index[title] = -1
			for i, line := range lines {
				if strings.HasPrefix(line, title) {
					index[title] = i
					break
				}
			}
			if index[title] < 0 {
				t.Fatalf("stable section %q missing: %#v", title, lines)
			}
		}
		return index
	}

	before := stableIndex(infoPanelPlainLines(m.renderInfoPanel(44, 60)))

	// Transient sections appear: agents, background jobs, and todos.
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "worker", TaskDesc: "Review changes"}}, "main", "builder")
	m.jobsSnapshot = []tools.JobState{{
		ID:          "job-1",
		Description: "run tests",
		Status:      jobStatusRunning,
		StartedAt:   time.Now().Add(-time.Minute),
	}}
	m.activeJobsSnapshot = m.jobsSnapshot
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration
	backend.todos = []tools.TodoItem{
		{ID: "1", Content: "Inspect layout", Status: "in_progress"},
		{ID: "2", Content: "Run tests", Status: "pending"},
	}

	lines := infoPanelPlainLines(m.renderInfoPanel(44, 60))
	after := stableIndex(lines)
	for _, title := range stableTitles {
		if after[title] != before[title] {
			t.Fatalf("stable section %q moved from line %d to %d after transient sections appeared: %#v", title, before[title], after[title], lines)
		}
	}

	lastStable := after["▼ CHANGED FILES"]
	for _, title := range []string{"▼ AGENTS", "▼ JOBS", "▼ TODOS"} {
		index := -1
		for i, line := range lines {
			if strings.HasPrefix(line, title) {
				index = i
				break
			}
		}
		if index < 0 {
			t.Fatalf("transient section %q missing: %#v", title, lines)
		}
		if index <= lastStable {
			t.Fatalf("transient section %q at line %d should follow the last stable section at line %d", title, index, lastStable)
		}
	}
}

func TestInfoPanelScrollReusesCompleteRenderedContent(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update(nil, "main", "builder")
	for i := range 30 {
		m.sidebar.AddFileEdit("main", fmt.Sprintf("/tmp/file-%02d.go", i), i+1, 0)
	}
	first := m.renderInfoPanel(48, 12)
	cachedLines := m.cachedInfoPanelLines
	cachedFingerprint := m.cachedInfoPanelFP
	if !m.scrollInfoPanel(3) {
		t.Fatal("expected overflowing info panel to scroll")
	}
	second := m.renderInfoPanel(48, 12)

	if first == second {
		t.Fatal("scrolling should change the visible info panel window")
	}
	if !slices.Equal(m.cachedInfoPanelLines, cachedLines) || m.cachedInfoPanelFP != cachedFingerprint {
		t.Fatal("scrolling should reuse complete rendered content and its fingerprint")
	}
}

func TestToolResultStatusFromRestoredContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    message.ToolResultStatus
	}{
		{name: "empty", content: "", want: message.ToolStatusSuccess},
		{name: "cancelled", content: "Cancelled", want: message.ToolStatusCancelled},
		{name: "error", content: "Error: exit code 1", want: message.ToolStatusError},
		{name: "success", content: "written successfully", want: message.ToolStatusSuccess},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolResultStatusFromRestoredContent(tc.content); got != tc.want {
				t.Fatalf("toolResultStatusFromRestoredContent(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func infoPanelRawLines(rendered string) []string {
	raw := strings.Split(terminaltext.StripANSI(rendered), "\n")
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
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true, Enabled: true}}
	backend.availableSkills = []*skill.Meta{{Name: "go-expert", Discovered: true}}
	m := NewModel(backend)
	// The skill is available but not loaded, so the section defaults to
	// folded; expand it explicitly to pin the content indent.
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = false
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
		{title: "▼ SKILLS", want: "   ○ go-expert"},
		{title: "▼ CHANGED FILES", want: "   foo.go +2 -1"},
		{title: "▼ AGENTS", want: "   ▸ ○ builder"},
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
	case line == "BACKEND":
		return "BACKEND"
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
	case strings.HasPrefix(line, "TIME"):
		return "TIME"
	default:
		return ""
	}
}

// contextFGSGR returns the foreground color spec lipgloss merges into SGR
// sequences (e.g. "214" for a 256-color orange). Sequences combine multiple
// attributes (bold/bg/fg), so matching the bare "38;5;<spec>" substring is
// more robust than matching a whole escape.
func contextFGSGR(spec string) string {
	return "38;5;" + spec
}
