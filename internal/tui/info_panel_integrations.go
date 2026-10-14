package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/skill"
)

// One render owns this snapshot. Fingerprinting, folding and rows must observe
// the same state; no long-lived cache or invalidation protocol is needed.
type infoPanelIntegrations struct {
	lsp     []agent.LSPServerDisplay
	mcp     []agent.MCPServerDisplay
	skills  []*skill.Meta
	invoked []*skill.Meta
	states  []skill.InvocationState
}

func (m *Model) captureInfoPanelIntegrations() infoPanelIntegrations {
	var snapshot infoPanelIntegrations
	if m.agent == nil {
		return snapshot
	}
	if provider, ok := m.agent.(agent.LSPStateProvider); ok {
		snapshot.lsp = provider.LSPServerList()
	}
	if provider, ok := m.agent.(agent.MCPStateProvider); ok {
		snapshot.mcp = provider.MCPServerList()
	}
	snapshot.skills = m.infoPanelSkills()
	snapshot.invoked = m.agent.InvokedSkills()
	if provider, ok := m.agent.(agent.FocusedSkillInvocationStateProvider); ok {
		snapshot.states = provider.FocusedSkillInvocationStates()
	}
	return snapshot
}

func (s infoPanelIntegrations) collapsed(section infoPanelSectionID) bool {
	switch section {
	case infoPanelSectionLSP:
		for _, row := range s.lsp {
			if row.OK {
				return false
			}
		}
	case infoPanelSectionMCP:
		for _, row := range s.mcp {
			if row.OK {
				return false
			}
		}
	case infoPanelSectionSkills:
		for _, sk := range s.invoked {
			if sk != nil && strings.TrimSpace(sk.Name) != "" {
				return false
			}
		}
	}
	return true
}

func integrationHeaderSummary(total, failed int) string {
	// Put actionable state first so it survives narrow-panel truncation.
	if failed > 0 {
		return fmt.Sprintf("%d failed · %d", failed, total)
	}
	return fmt.Sprint(total)
}
