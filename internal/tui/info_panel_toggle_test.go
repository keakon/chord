package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/gitinspect"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// A manual choice outlives the state that produced it: folding a section
// while its servers are enabled keeps it folded when they drop and return.
func TestInfoPanelManualToggleSticksAcrossStateChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true, Enabled: true}}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ MCP") {
		t.Fatalf("enabled MCP server should auto-expand the section, got %q", plain)
	}

	m.toggleInfoPanelSection(infoPanelSectionMCP)
	if collapsed, ok := m.infoPanelCollapsedSections[infoPanelSectionMCP]; !ok || !collapsed {
		t.Fatalf("manual fold should be recorded, got %v (present=%v)", m.infoPanelCollapsedSections[infoPanelSectionMCP], ok)
	}

	// The server drops: the automatic state is folded again, but the manual
	// fold the user asked for stays.
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Err: "connection refused"}}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ MCP") {
		t.Fatalf("manual fold should survive the server dropping, got %q", plain)
	}

	// The server returns: the automatic state would expand the section, and
	// the manual fold must still win.
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true, Enabled: true}}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ MCP") {
		t.Fatalf("manual fold must outlive the automatic state flipping back to expanded, got %q", plain)
	}
}

// Clicking a section back onto the state the automatic rules already chose
// releases the override, so the section follows its content again.
func TestInfoPanelToggleBackToAutoStateReleasesOverride(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Pending: true, Enabled: false}}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ MCP") {
		t.Fatalf("disabled MCP server should keep the section folded, got %q", plain)
	}

	m.toggleInfoPanelSection(infoPanelSectionMCP) // manual expand
	if collapsed, ok := m.infoPanelCollapsedSections[infoPanelSectionMCP]; !ok || collapsed {
		t.Fatalf("manual expand should be recorded, got %v (present=%v)", m.infoPanelCollapsedSections[infoPanelSectionMCP], ok)
	}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ MCP") {
		t.Fatalf("manual expand should render expanded, got %q", plain)
	}

	m.toggleInfoPanelSection(infoPanelSectionMCP) // back onto the automatic state
	if _, ok := m.infoPanelCollapsedSections[infoPanelSectionMCP]; ok {
		t.Fatal("clicking back to the automatic state must release the override")
	}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ MCP") {
		t.Fatalf("released section should follow the automatic folded state, got %q", plain)
	}

	// Automatic following resumes: an enabled server expands the section again.
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true, Enabled: true}}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ MCP") {
		t.Fatalf("released override should let the enabled server auto-expand the section, got %q", plain)
	}
}

// Content-driven sections open with their first entry, so restoring one to
// expanded releases its override the same way.
func TestInfoPanelContentSectionToggleReleasesOnRestore(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.todos = []tools.TodoItem{{ID: "1", Content: "ship it", Status: "pending"}}
	m := NewModel(backend)

	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ TODOS") {
		t.Fatalf("content-driven section should start expanded, got %q", plain)
	}

	m.toggleInfoPanelSection(infoPanelSectionTodos) // manual fold
	if collapsed, ok := m.infoPanelCollapsedSections[infoPanelSectionTodos]; !ok || !collapsed {
		t.Fatalf("manual fold should be recorded, got %v (present=%v)", m.infoPanelCollapsedSections[infoPanelSectionTodos], ok)
	}
	m.toggleInfoPanelSection(infoPanelSectionTodos) // restored to the automatic state
	if _, ok := m.infoPanelCollapsedSections[infoPanelSectionTodos]; ok {
		t.Fatal("restoring a content-driven section to expanded must release the override")
	}
	plain = terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▼ TODOS") {
		t.Fatalf("released TODOS section should stay expanded, got %q", plain)
	}
}

// GIT stays folded by default, so expanding and re-folding it releases the
// override just like the state-driven sections.
func TestInfoPanelGitToggleReleasesOnRestore(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "main"}

	m.toggleInfoPanelSection(infoPanelSectionGit) // manual expand
	if collapsed, ok := m.infoPanelCollapsedSections[infoPanelSectionGit]; !ok || collapsed {
		t.Fatalf("manual expand should be recorded, got %v (present=%v)", m.infoPanelCollapsedSections[infoPanelSectionGit], ok)
	}
	m.toggleInfoPanelSection(infoPanelSectionGit) // back onto the automatic state
	if _, ok := m.infoPanelCollapsedSections[infoPanelSectionGit]; ok {
		t.Fatal("restoring GIT to its automatic folded state must release the override")
	}
	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 60))
	if !strings.Contains(plain, "▶ GIT") {
		t.Fatalf("released GIT section should follow the automatic folded state, got %q", plain)
	}
}

// The real click path shares the lifecycle: two clicks on a header return the
// section to the state its content already implies and release the override.
func TestClickInfoPanelHeaderTwiceReleasesOverride(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}}
	m := NewModelWithSize(backend, 140, 24)

	clickLSPHeader := func() {
		m.layout = m.generateLayout(m.width, m.height)
		_ = m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height)
		clickY := m.layout.infoPanel.Min.Y
		for _, hit := range m.infoPanelHitBoxes {
			if hit.section == infoPanelSectionLSP {
				clickY = m.layout.infoPanel.Min.Y + hit.startY
				break
			}
		}
		updated, cmd := m.Update(tea.MouseClickMsg{X: m.layout.infoPanel.Min.X + 1, Y: clickY, Button: tea.MouseLeft})
		model, ok := updated.(*Model)
		if !ok {
			t.Fatalf("Update returned %T, want *Model", updated)
		}
		if cmd != nil {
			t.Fatalf("info panel click should not schedule command, got %#v", cmd)
		}
		m = *model
	}

	clickLSPHeader() // manual fold
	if collapsed, ok := m.infoPanelCollapsedSections[infoPanelSectionLSP]; !ok || !collapsed {
		t.Fatalf("first click should record a manual fold, got %v (present=%v)", m.infoPanelCollapsedSections[infoPanelSectionLSP], ok)
	}

	clickLSPHeader() // back onto the automatic expanded state
	if _, ok := m.infoPanelCollapsedSections[infoPanelSectionLSP]; ok {
		t.Fatal("clicking the header back to the automatic state must release the override")
	}
	plain := terminaltext.StripANSI(m.renderInfoPanel(m.layout.infoPanel.Dx(), m.viewport.height))
	if !strings.Contains(plain, "▼ LSP") {
		t.Fatalf("released LSP section should follow the loaded server, got %q", plain)
	}
}
