package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func assertInfoPanelLinePainted(t *testing.T, line, context string) {
	t.Helper()
	cells := drawLineCells(t, line)
	wantBg := colorOfTheme(currentStyles.Palette.InfoPanelBg)
	for col := 0; col < len(cells); {
		cell := cells[col]
		if !colorsEqual(cell.Style.Bg, wantBg) {
			t.Fatalf("%s: column %d background = %v, want panel background %v; row=%q",
				context, col, cell.Style.Bg, wantBg, terminaltext.StripANSI(line))
		}
		// Wide glyph continuation cells inherit their leading cell's surface.
		col += max(cell.Width, 1)
	}
}

// infoPanelRowEndingWith returns the first raw (still styled) line whose
// stripped text ends with suffix, so a test can assert on the escapes of one
// specific row instead of re-deriving the row's construction.
func infoPanelRowEndingWith(rendered, suffix string) string {
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasSuffix(strings.TrimRight(terminaltext.StripANSI(line), " "), suffix) {
			return line
		}
	}
	return ""
}

// assertInfoPanelRowPainted checks the final cells rather than reconstructing
// the renderer's styled fragments, so unstyled separators remain observable.
func assertInfoPanelRowPainted(t *testing.T, rendered, suffix, context string) {
	t.Helper()
	row := infoPanelRowEndingWith(rendered, suffix)
	if row == "" {
		t.Fatalf("%s: no panel row ends with %q in %q", context, suffix, terminaltext.StripANSI(rendered))
	}
	assertInfoPanelLinePainted(t, row, context)
}

func TestInfoPanelLinesKeepThePanelBackground(t *testing.T) {
	previous := currentStyles.Palette
	t.Cleanup(func() { ApplyTheme(previous) })
	backend := newInfoPanelAgent()
	backend.availableSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go expert", Discovered: true, Invoked: true},
		{Name: "py-expert", Description: "Python expert", Discovered: true},
		{Name: "manual-skill", Description: "Explicit loads only", Discovered: true, DisableModelInvocation: true},
	}
	backend.invokedSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go expert", Discovered: true, Invoked: true},
	}
	backend.skillStates = []skill.InvocationState{
		{Meta: backend.availableSkills[0], UserLoadable: true, ModelCallable: true, BodyPresent: true},
		{Meta: backend.availableSkills[1], UserLoadable: true, ModelCallable: true, LoadedEarlier: true},
		{Meta: backend.availableSkills[2], UserLoadable: true, ModelCallable: false},
	}
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true, Enabled: true}}
	backend.todos = []tools.TodoItem{
		{ID: "1", Content: "Inspect the module", Status: "in_progress"},
		{ID: "2", Content: "Update the example", Status: "pending"},
	}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = false
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "update the example"}}, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)

	for _, bg := range []string{m.theme.InfoPanelBg, "0", "49", "#003129"} {
		t.Run(bg, func(t *testing.T) {
			theme := m.theme
			theme.InfoPanelBg = bg
			m.SetTheme(theme)
			for _, width := range []int{40, 48, 64} {
				rendered := m.renderInfoPanel(width, 80)
				for i, line := range strings.Split(rendered, "\n") {
					assertInfoPanelLinePainted(t, line, fmt.Sprintf("width %d, line %d", width, i))
				}
			}
		})
	}
}

// TestInfoPanelSkillRowsPaintTheirSeparator is the focused half of the
// regression above: the separator between a skill's state glyph and its name
// must be covered by the panel surface, whatever the skill's invocation state.
func TestInfoPanelSkillRowsPaintTheirSeparator(t *testing.T) {
	backend := newInfoPanelAgent()
	catalog := []*skill.Meta{
		{Name: "loaded-skill", Description: "body live", Discovered: true},
		{Name: "idle-skill", Description: "nothing loaded", Discovered: true},
		{Name: "manual-skill", Description: "explicit loads only", Discovered: true, DisableModelInvocation: true},
	}
	backend.availableSkills = catalog
	backend.skillStates = []skill.InvocationState{
		{Meta: catalog[0], UserLoadable: true, ModelCallable: true, BodyPresent: true},
		{Meta: catalog[1], UserLoadable: true, ModelCallable: true},
		{Meta: catalog[2], UserLoadable: true, ModelCallable: false},
	}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = false

	rendered := m.renderInfoPanel(48, 40)
	for _, name := range []string{"loaded-skill", "idle-skill", "manual-skill"} {
		assertInfoPanelRowPainted(t, rendered, name, "skill row "+name)
	}
}
