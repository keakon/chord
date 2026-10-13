package tui

import (
	"fmt"
	"image"
	"strings"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
)

func (m *Model) openMCPDetail(ref mcp.ServerRef, role string) {
	if m.mode != ModeMCPSelect {
		previous := m.mode
		m.clearChordState()
		m.mcpSelect = mcpSelectState{prevMode: previous}
		m.mode = ModeMCPSelect
		m.recalcViewportSize()
	}
	m.mcpSelect.detail = &ref
	m.mcpSelect.detailAgent = role
	m.invalidateDrawCaches()
}
func (m *Model) openSelectedMCPDetail() {
	name, ok := m.mcpSelectCurrent()
	if !ok {
		return
	}
	provider, ok := m.agent.(agent.MCPAuthStateProvider)
	if !ok {
		return
	}
	for _, server := range provider.MCPRuntimeSnapshot().Servers {
		if server.Ref.ServerName == name {
			m.openMCPDetail(server.Ref, "")
			return
		}
	}
}
func (m *Model) renderMCPDetailDialog() string {
	if m.mcpSelect.detail == nil {
		return ""
	}
	provider, ok := m.agent.(agent.MCPAuthStateProvider)
	if !ok {
		return ""
	}
	ref := *m.mcpSelect.detail
	snapshot := provider.MCPRuntimeSnapshot()
	if m.mcpSelect.detailAgent != "" {
		snapshot, _ = provider.MCPRuntimeSnapshotForAgent(m.mcpSelect.detailAgent)
	}
	var selected *mcp.ServerView
	for _, server := range snapshot.Servers {
		if server.Ref == ref {
			copy := server
			selected = &copy
			break
		}
	}
	body := "This MCP server is no longer visible."
	if selected != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\nOwner: %s\nAuthentication: %s\nConnection: %s\nTool catalog: %s\n", ansi.Strip(ref.ServerName), ansi.Strip(ref.Owner), selected.Auth, selected.Connection, selected.Catalog)
		if len(selected.Aliases) > 1 {
			fmt.Fprintf(&b, "Shared sign-in: %s\n", ansi.Strip(strings.Join(selected.Aliases, ", ")))
		}
		if !selected.DesiredEnabled {
			b.WriteString("Disabled; signing in will not enable this server.\n")
		}
		for _, op := range selected.Operations {
			fmt.Fprintf(&b, "%s: %s\n", op.Kind, op.State)
		}
		if selected.Diagnostic.ErrorClass != "" {
			fmt.Fprintf(&b, "Diagnostic: %s\nNext action: %s\n", selected.Diagnostic.ErrorClass, selected.Diagnostic.SuggestedAction)
		}
		if selected.Diagnostic.ControlNetworkMode == mcp.ControlNetworkDirect {
			b.WriteString("OAuth control requests: direct connection\n")
			if selected.Diagnostic.ProxyEnvironmentDetected {
				b.WriteString("Proxy environment detected; OAuth control requests do not use it.\n")
			}
		}
		for _, stage := range selected.Stages {
			if stage.State != mcp.ProbeSkipped {
				fmt.Fprintf(&b, "%s: %s (%d ms)\n", stage.Stage, stage.State, stage.DurationMS)
			}
		}
		for _, scope := range selected.ScopeRequirements {
			fmt.Fprintf(&b, "Additional scopes: %s\n", ansi.Strip(strings.Join(scope.Scopes, ", ")))
		}
		if link := provider.MCPAuthorizationURL(ref); link != "" {
			b.WriteString("\nOpen this URL to sign in:\n")
			b.WriteString(ansi.Strip(link))
			b.WriteByte('\n')
		}
		body = b.String()
	}
	rendered, _ := RenderOverlay(OverlayConfig{Title: "MCP details", MaxWidth: 90, Hint: hintLine(hint("l", "login"), hint("a", "reauthorize"), hint("o", "logout"), hint("r", "reconnect"), hint("c", "cancel login"), hint("Esc", "back"))}, body, image.Rect(0, 0, m.width, m.height))
	return rendered
}
func (m *Model) handleMCPDetailKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if key == "esc" || keyMatches(key, m.keyMap.InsertEscape) {
		if m.mcpSelect.selector.list == nil {
			return m.closeMCPSelect()
		}
		m.mcpSelect.detail = nil
		m.mcpSelect.detailAgent = ""
		m.invalidateDrawCaches()
		return nil
	}
	provider, ok := m.agent.(agent.MCPAuthStateProvider)
	if !ok || m.mcpSelect.detail == nil {
		return nil
	}
	var action agent.MCPAuthAction
	switch key {
	case "l":
		action = agent.MCPAuthLogin
	case "a":
		action = agent.MCPAuthReauthorize
	case "o":
		action = agent.MCPAuthLogout
	case "r":
		action = agent.MCPAuthReconnect
	case "c":
		action = agent.MCPAuthCancel
	default:
		return nil
	}
	provider.RequestMCPAuth(agent.MCPAuthRequest{Action: action, Ref: *m.mcpSelect.detail, AgentDefinition: m.mcpSelect.detailAgent})
	return nil
}
