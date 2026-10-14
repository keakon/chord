package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// The state-driven sections (LSP, MCP, SKILLS) list configured entries even
// while every row is merely pending, disabled, idle, or unloaded, so they must
// default to folded and only unfold once an entry reaches its active state.

func TestInfoPanelLSPSectionDefaultsFoldedUntilAServerLoads(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{
		{Name: "gopls", Pending: true},
		{Name: "pyright", Err: "dial tcp"},
	}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ LSP") {
		t.Fatalf("LSP section with no loaded server should default to folded, got %q", plain)
	}
	if body := infoPanelSectionLines(infoPanelPlainLines(plain), "▶ LSP"); len(body) != 0 {
		t.Fatalf("folded LSP section should render no body lines, got %#v", body)
	}

	// One loaded server unfolds the section on its own, even while another
	// server is still pending.
	backend.lspRows = []agent.LSPServerDisplay{
		{Name: "gopls", Pending: true},
		{Name: "pyright", OK: true},
	}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ LSP") {
		t.Fatalf("LSP section should unfold once a server loads, got %q", plain)
	}
	body := infoPanelSectionLines(infoPanelPlainLines(plain), "▼ LSP")
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "pyright") || !strings.Contains(joined, "gopls") {
		t.Fatalf("expanded LSP section should list its servers, got %#v", body)
	}
}

func TestInfoPanelMCPSectionDefaultsFoldedUntilAServerConnects(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "exa", Pending: true},
		{Name: "browser", Disabled: true, Manual: true},
	}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ MCP") {
		t.Fatalf("MCP section with no connected server should default to folded, got %q", plain)
	}
	if body := infoPanelSectionLines(infoPanelPlainLines(plain), "▶ MCP"); len(body) != 0 {
		t.Fatalf("folded MCP section should render no body lines, got %#v", body)
	}

	backend.mcpRows = []agent.MCPServerDisplay{
		{Name: "exa", OK: true},
		{Name: "browser", Disabled: true, Manual: true},
	}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ MCP") {
		t.Fatalf("MCP section should unfold once a server connects, got %q", plain)
	}
	body := infoPanelSectionLines(infoPanelPlainLines(plain), "▼ MCP")
	joined := strings.Join(body, "\n")
	if !strings.Contains(joined, "exa") || !strings.Contains(joined, "browser") {
		t.Fatalf("expanded MCP section should list its servers, got %#v", body)
	}
}

func TestInfoPanelSkillsSectionDefaultsFoldedUntilASkillLoads(t *testing.T) {
	catalog := []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Discovered: true},
		{Name: "py-expert", Description: "Python development expert", Discovered: true},
	}
	backend := newInfoPanelAgent()
	backend.availableSkills = catalog
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ SKILLS") {
		t.Fatalf("SKILLS section with no loaded skill should default to folded, got %q", plain)
	}
	if body := infoPanelSectionLines(infoPanelPlainLines(plain), "▶ SKILLS"); len(body) != 0 {
		t.Fatalf("folded SKILLS section should render no body lines, got %#v", body)
	}

	// Loading one skill unfolds the section; the loaded row is the one the
	// success-colored ● marks.
	backend.invokedSkills = []*skill.Meta{
		{Name: "go-expert", Description: "Go language development expert", Discovered: true, Invoked: true},
	}
	rendered := m.renderInfoPanel(40, 60)
	plain = terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "▼ SKILLS") {
		t.Fatalf("SKILLS section should unfold once a skill loads, got %q", plain)
	}
	body := infoPanelSectionLines(infoPanelPlainLines(plain), "▼ SKILLS")
	if len(body) == 0 {
		t.Fatalf("expanded SKILLS section should list its skills, got %#v", body)
	}
	loaded := skillStateLoadedStyle().Render("●") + " " + currentStyles.InfoPanelDim.Render("go-expert")
	if !strings.Contains(rendered, loaded) {
		t.Fatalf("loaded skill should keep its success-colored row; want %q in %q", loaded, rendered)
	}

	// A body that compaction archived is a reload hint, not a load: it must
	// not keep the section unfolded on its own.
	backend.invokedSkills = nil
	backend.skillStates = []skill.InvocationState{
		{Meta: catalog[0], UserLoadable: true, ModelCallable: true, LoadedEarlier: true},
		{Meta: catalog[1], UserLoadable: true, ModelCallable: true},
	}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ SKILLS") {
		t.Fatalf("SKILLS section should fold again once no body is loaded, got %q", plain)
	}
}

// The content-driven sections only render once the session has produced their
// content, so that content is the trigger and they stay expanded by default.
func TestInfoPanelContentSectionsStayExpandedByDefault(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "ship it", Status: "pending"}}
	m := NewModel(backend)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	m.sidebar.AddFileEdit("main", "/tmp/foo.go", 2, 1)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	for _, title := range []string{"▼ TODOS", "▼ CHANGED FILES", "▼ AGENTS"} {
		if !strings.Contains(plain, title) {
			t.Fatalf("content-driven section %q should stay expanded by default, got %q", title, plain)
		}
	}
}

// A section the user toggled keeps that state regardless of what its content
// does afterwards.
func TestInfoPanelToggleOverridesContentDrivenDefault(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Pending: true}}
	backend.availableSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true}}
	backend.invokedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Discovered: true, Invoked: true}}
	m := NewModel(backend)

	// Folded by default (nothing loaded), expanded by the user: stays expanded.
	m.toggleInfoPanelSection(infoPanelSectionLSP)
	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ LSP") {
		t.Fatalf("user-expanded LSP section should stay expanded, got %q", plain)
	}

	// Expanded by default (a skill is loaded), folded by the user: stays folded.
	m.toggleInfoPanelSection(infoPanelSectionSkills)
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ SKILLS") {
		t.Fatalf("user-folded SKILLS section should stay folded, got %q", plain)
	}
}
