package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

type countedIntegrationAgent struct {
	*infoPanelAgent
	lspCalls, mcpCalls, invokedCalls, skillsCalls int
}

func (a *countedIntegrationAgent) LSPServerList() []agent.LSPServerDisplay {
	a.lspCalls++
	return a.infoPanelAgent.LSPServerList()
}
func (a *countedIntegrationAgent) MCPServerList() []agent.MCPServerDisplay {
	a.mcpCalls++
	return a.infoPanelAgent.MCPServerList()
}
func (a *countedIntegrationAgent) InvokedSkills() []*skill.Meta {
	a.invokedCalls++
	return a.infoPanelAgent.InvokedSkills()
}

func (a *countedIntegrationAgent) FocusedSkills() []*skill.Meta {
	a.skillsCalls++
	return a.infoPanelAgent.FocusedSkills()
}

func TestInfoPanelReusesIntegrationSnapshots(t *testing.T) {
	backend := &countedIntegrationAgent{infoPanelAgent: newInfoPanelAgent()}
	backend.lspRows = []agent.LSPServerDisplay{{Name: "server", Err: "unavailable"}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "service", Err: "login required"}, {Name: "disabled", Disabled: true}, {Name: "idle", Idle: true}}
	m := NewModel(backend)
	for pass := 1; pass <= 2; pass++ {
		rendered := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
		if !strings.Contains(rendered, "▶ LSP · 1 failed") || !strings.Contains(rendered, "▶ MCP · 1 failed") {
			t.Fatalf("failures hidden: %s", rendered)
		}
		if backend.lspCalls != pass || backend.mcpCalls != pass || backend.invokedCalls != 0 || backend.skillsCalls != 0 {
			t.Fatalf("snapshot calls: LSP=%d MCP=%d invoked=%d skills=%d pass=%d", backend.lspCalls, backend.mcpCalls, backend.invokedCalls, backend.skillsCalls, pass)
		}
	}
	// Idle and failure have identical OK/Pending bits but different appearance.
	backend.lspRows[0] = agent.LSPServerDisplay{Name: "server", Idle: true}
	rendered := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if strings.Contains(rendered, "▶ LSP · 1 failed") {
		t.Fatal("stale failure header")
	}
	m.toggleInfoPanelSection(infoPanelSectionMCP)
	if rendered := terminaltext.StripANSI(m.renderInfoPanel(40, 60)); !strings.Contains(rendered, "▼ MCP") || !strings.Contains(rendered, "service") {
		t.Fatal("manual expansion lost")
	}
}

func TestInfoPanelInvocationSnapshotFiltersDeniedSkills(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.skillStates = []skill.InvocationState{
		{Meta: &skill.Meta{Name: "allowed"}, UserLoadable: true, BodyPresent: true},
		{Meta: &skill.Meta{Name: "denied"}, BodyPresent: true},
	}
	m := NewModel(backend)
	rendered := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(rendered, "allowed") || strings.Contains(rendered, "denied") {
		t.Fatalf("unexpected skills: %s", rendered)
	}
	backend.skillStates = []skill.InvocationState{
		{Meta: &skill.Meta{Name: "replacement"}, UserLoadable: true},
	}
	m.infoPanelCollapsedSections[infoPanelSectionSkills] = false
	rendered = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(rendered, "replacement") || strings.Contains(rendered, "allowed") {
		t.Fatalf("stale skill catalog: %s", rendered)
	}
}
