package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestRenderInfoPanelAgentsUsesTaskSummaryLabelPolicy(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "update prompt tests", AgentDefName: "coder"}}, "main", "builder")

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
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
		{Name: "manual-live", Description: "Manual workflow", Discovered: true, DisableModelInvocation: true},
		{Name: "manual-idle", Description: "Manual helper", Discovered: true, DisableModelInvocation: true},
	}
	backend.invokedSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Discovered: true, Invoked: true},
		{Name: "manual-live", Description: "Manual workflow", Discovered: true, DisableModelInvocation: true, Invoked: true},
	}
	m := NewModel(backend)

	rendered := m.renderInfoPanel(40, 40)
	plain := terminaltext.StripANSI(rendered)
	section := infoPanelSectionLines(infoPanelPlainLines(plain), "▼ SKILLS")
	joined := strings.Join(section, "\n")
	if !strings.Contains(plain, "▼ SKILLS") {
		t.Fatalf("skills header missing in %q", plain)
	}
	for _, want := range []string{"go-expert", "py-expert", "manual-live", "manual-idle"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("skills section missing %q in %q", want, joined)
		}
	}
	// The icon is the only status channel: shape carries the state class and
	// color the accent, so the four combinations stay distinguishable while
	// every name stays dim.
	invokedStyle := skillStateLoadedStyle()
	cases := []struct {
		name  string
		glyph string
		style lipgloss.Style
		label string
	}{
		{name: "model-visible loaded", glyph: "●", style: invokedStyle, label: "go-expert"},
		{name: "model-visible idle", glyph: "○", style: currentStyles.InfoPanelDim, label: "py-expert"},
		{name: "manual loaded", glyph: "◌", style: invokedStyle, label: "manual-live"},
		{name: "manual idle", glyph: "◌", style: currentStyles.InfoPanelDim, label: "manual-idle"},
	}
	// Assert the two halves of the row separately instead of re-building the
	// concatenation the renderer assembles: the previous form mirrored the
	// implementation, so it kept passing while the row's separator fell outside
	// every styled fragment and left a one-cell hole in the panel surface.
	for _, tc := range cases {
		if want := tc.style.Render(tc.glyph); !strings.Contains(rendered, want) {
			t.Fatalf("%s skill row should paint its glyph with the state style; want %q in %q", tc.name, want, rendered)
		}
		assertInfoPanelRowPainted(t, rendered, tc.glyph+" "+tc.label, tc.name+" skill row")
	}
	// No row may fall back to prose: every state has an icon, and a suffix
	// would both repeat the shape and truncate early in a narrow sidebar.
	if strings.ContainsAny(joined, "(") {
		t.Fatalf("skill rows should carry icon-only status, got %q", joined)
	}
}

func TestRenderInfoPanelSkillsUseStateIcons(t *testing.T) {
	backend := newInfoPanelAgent()
	catalog := []*skill.Meta{
		{Name: "relevant-skill", Description: "A project read matched its paths", Discovered: true},
		{Name: "reload-skill", Description: "Body archived by compaction", Discovered: true},
		{Name: "manual-skill", Description: "Explicit loads only", Discovered: true, DisableModelInvocation: true},
	}
	backend.availableSkills = catalog
	backend.skillStates = []skill.InvocationState{
		{Meta: catalog[0], UserLoadable: true, ModelCallable: true, NoticeRecorded: true},
		{Meta: catalog[1], UserLoadable: true, ModelCallable: true, LoadedEarlier: true},
		{Meta: catalog[2], UserLoadable: true, ModelCallable: false},
	}
	m := NewModel(backend)
	// None of these states carries a loaded body, so the SKILLS section
	// defaults to folded; expand it explicitly to pin the row rendering.
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = false

	rendered := m.renderInfoPanel(40, 24)
	plain := terminaltext.StripANSI(rendered)
	section := strings.Join(infoPanelSectionLines(infoPanelPlainLines(plain), "▼ SKILLS"), "\n")
	for _, want := range []string{"◎ relevant-skill", "↻ reload-skill", "◌ manual-skill"} {
		if !strings.Contains(section, want) {
			t.Fatalf("skills section missing row %q in %q", want, section)
		}
	}
	if strings.ContainsAny(section, "()") {
		t.Fatalf("skills section should carry icon-only status, got %q", section)
	}
	// The reload state is the only non-dim, non-success accent: amber marks
	// "load it again", so it has to survive rendering as a colored glyph.
	reloadGlyph := currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg)).Render("↻")
	if !strings.Contains(rendered, reloadGlyph) {
		t.Fatalf("reload row should paint ↻ in the warning color; want %q in %q", reloadGlyph, rendered)
	}
}

func TestRenderInfoPanelShowsSkillsForFocusedSubAgent(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.skillsByFocus = map[string][]*skill.Meta{
		"":        nil,
		"agent-1": {{Name: "go-expert", Description: "Go language development expert", Discovered: true}},
	}
	m := NewModel(backend)

	mainPlain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if strings.Contains(mainPlain, "SKILLS") {
		t.Fatalf("main info panel should hide denied skills, got %q", mainPlain)
	}

	m.setFocusedAgent("agent-1")
	// The sub-agent's only skill is not loaded, so the section defaults to
	// folded; expand it explicitly to pin the focused-agent row rendering.
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = false
	subPlain := terminaltext.StripANSI(m.renderInfoPanel(40, 24))
	if !strings.Contains(subPlain, "▼ SKILLS") || !strings.Contains(subPlain, "go-expert") {
		t.Fatalf("focused subagent info panel should show its skills, got %q", subPlain)
	}
}

func TestRenderInfoPanelSkipsInvokedSkillMissingFromAvailableList(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true}}
	backend.invokedSkills = []*skill.Meta{{Name: "missing-skill", Invoked: true}, {Name: "go-expert", Description: "Go language development expert", Discovered: true, Invoked: true}}
	m := NewModel(backend)

	rendered := m.renderInfoPanel(40, 24)
	plain := terminaltext.StripANSI(rendered)
	section := strings.Join(infoPanelSectionLines(infoPanelPlainLines(plain), "▼ SKILLS"), "\n")
	if strings.Contains(section, "missing-skill") {
		t.Fatalf("skills section should omit invoked skills missing from available list, got %q", section)
	}
	// Assert the glyph's color and the row's surface separately rather than
	// re-building the concatenation the renderer assembles: the previous form
	// mirrored the implementation, so it also pinned the buggy bare separator
	// between the glyph and the name as expected output.
	if want := skillStateLoadedStyle().Render("●"); !strings.Contains(rendered, want) {
		t.Fatalf("visible invoked skill should keep a success-colored ● icon; want %q in %q", want, rendered)
	}
	assertInfoPanelRowPainted(t, rendered, "● go-expert", "visible invoked skill row")
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
	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "▶ SKILLS") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed SKILLS header should include count, got %q", plain)
	}
}

func TestRenderInfoPanelAgentsPreserveInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	rendered := m.renderInfoPanel(48, 24)

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ AGENTS")
	if len(section) < 2 {
		t.Fatalf("AGENTS section = %#v, want rows", section)
	}
	if section[0] != "▸ ○ builder" {
		t.Fatalf("AGENTS main row = %q, want %q", section[0], "▸ ○ builder")
	}
	if section[1] != "◇ ship tests" {
		t.Fatalf("AGENTS sub-agent row = %q, want %q", section[1], "◇ ship tests")
	}

	if !strings.Contains(rendered, currentStyles.InfoPanelAgentFocusedStyle.Render("▸ ○ builder")) {
		t.Fatalf("AGENTS focused line should use info-panel-aware style; want styled content in output")
	}
	entryPrefix := currentStyles.InfoPanelAgentEntryStyle.Render("  ◇ ship tests")
	if !strings.Contains(rendered, entryPrefix) {
		t.Fatalf("AGENTS sub-agent line should preserve info-panel background with collapsible content indent; want %q in output", entryPrefix)
	}
}

func TestRenderInfoPanelAgentsOmitCompactingActivity(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityCompacting}
	rendered := m.renderInfoPanel(56, 24)

	section := infoPanelSectionLines(infoPanelPlainLines(rendered), "▼ AGENTS")
	if len(section) < 1 {
		t.Fatalf("AGENTS section = %#v, want main row", section)
	}
	if section[0] != "▸ ○ builder" {
		t.Fatalf("AGENTS main row = %q, want %q", section[0], "▸ ○ builder")
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
	if before[0] != "▸ ○ builder" {
		t.Fatalf("AGENTS main row before focus switch = %q, want %q", before[0], "▸ ○ builder")
	}
	if before[1] != "◇ ship tests" {
		t.Fatalf("AGENTS sub-agent row before focus switch = %q, want %q", before[1], "◇ ship tests")
	}

	m.setFocusedAgent("agent-1")
	after := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ AGENTS")
	if len(after) < 2 {
		t.Fatalf("AGENTS section after focus switch = %#v, want rows", after)
	}
	if after[0] != "○ builder" {
		t.Fatalf("AGENTS main row after focus switch = %q, want %q", after[0], "○ builder")
	}
	if after[1] != "▸ ◇ ship tests" {
		t.Fatalf("AGENTS sub-agent row after focus switch = %q, want %q", after[1], "▸ ◇ ship tests")
	}
}

func TestRenderInfoPanelAgentsHeaderShowsPlainCount(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	subs := make([]agent.SubAgentInfo, 0, 3)
	for i := range 3 {
		subs = append(subs, agent.SubAgentInfo{InstanceID: fmt.Sprintf("agent-%d", i), TaskDesc: "review"})
	}
	m.sidebar.Update(subs, "main", "builder")
	for i := range 3 {
		m.sidebar.UpdateStatus(fmt.Sprintf("agent-%d", i), string(agent.SubAgentStateFailed))
	}

	plain := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(plain, "AGENTS · 3") || strings.Contains(plain, "AGENTS · 3 ✗") {
		t.Fatalf("AGENTS header should show a plain child count, got %q", plain)
	}
	if strings.Contains(plain, "failed") {
		t.Fatalf("AGENTS rows should not spell out failure state, got %q", plain)
	}
}

func TestRenderInfoPanelCollapsedAgentsShowsHeaderOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.infoPanelCollapsedSections[infoPanelSectionAgents] = true

	plain := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(plain, "▶ AGENTS · 1") {
		t.Fatalf("collapsed AGENTS header should include a plain count, got %q", plain)
	}
	section := infoPanelSectionLines(infoPanelPlainLines(plain), "▶ AGENTS")
	if len(section) != 0 {
		t.Fatalf("collapsed AGENTS section should not render body lines, got %#v", section)
	}
}

// The info panel scrolls, so a display budget on AGENTS would only make rows
// unreachable: render every agent the sidebar still tracks and let the panel's
// own scrolling cover long lists. Same rule as CHANGED FILES.
func TestRenderInfoPanelAgentsRendersEveryAgentWithoutOverflowRow(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	const agents = 15 // above the old 10-row display budget
	subs := make([]agent.SubAgentInfo, 0, agents)
	for i := range agents {
		subs = append(subs, agent.SubAgentInfo{
			InstanceID: fmt.Sprintf("agent-%02d", i),
			TaskDesc:   fmt.Sprintf("task-%02d", i),
		})
	}
	m.sidebar.Update(subs, "main", "builder")

	// Height is generous so panel-level clipping does not hide rows here.
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, agents+20)), "▼ AGENTS")
	if got, want := len(section), agents+1; got != want {
		t.Fatalf("AGENTS rows = %d, want %d (main + every SubAgent); section=%#v", got, want, section)
	}
	if joined := strings.Join(section, "\n"); strings.Contains(joined, "more") {
		t.Fatalf("AGENTS must not collapse the tail behind an overflow row; section=%#v", section)
	}
	if section[0] != "▸ ○ builder" {
		t.Fatalf("AGENTS main row = %q, want %q", section[0], "▸ ○ builder")
	}
	joined := strings.Join(section, "\n")
	for i := range agents {
		if want := fmt.Sprintf("task-%02d", i); !strings.Contains(joined, want) {
			t.Fatalf("AGENTS section is missing %q; section=%#v", want, section)
		}
	}
}

func TestRenderInfoPanelAgentsApplyConfiguredColorToNonFocusedRows(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", Color: "196"}}, "main", "builder")

	rendered := m.renderInfoPanel(48, 24)
	want := infoPanelAgentRowStyle(SidebarEntry{ID: "agent-1", Color: "196"}, false).Render("  ◇ agent-1")
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

	before := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(before, "ship tests") {
		t.Fatalf("info panel before variant change = %q, want ship tests", before)
	}

	m.sidebar.Update([]agent.SubAgentInfo{{
		InstanceID:  "agent-1",
		TaskDesc:    "ship tests",
		SelectedRef: "openai/gpt-5.5@low",
	}}, "main", "builder")
	after := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(after, "ship tests") {
		t.Fatalf("info panel after variant change = %q, want ship tests", after)
	}
}

func TestInfoPanelOwnerReasonInvalidatesCache(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModel(backend)
	for _, reason := range []string{"Choose API A", "Choose API B", ""} {
		previous := m.infoPanelFingerprint(48, 60, m.captureInfoPanelIntegrations())
		m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "worker", State: "waiting_main", AwaitingOwnerReason: reason}}, "worker", "orchestrator")
		if previous == m.infoPanelFingerprint(48, 60, m.captureInfoPanelIntegrations()) {
			t.Fatal("reason change did not invalidate fingerprint")
		}
		plain := terminaltext.StripANSI(m.renderInfoPanel(48, 60))
		if reason != "" && !strings.Contains(plain, reason) {
			t.Fatalf("missing reason in %q", plain)
		}
		if reason == "" && strings.Contains(plain, "Choose API") {
			t.Fatal("stale reason")
		}
	}
}
