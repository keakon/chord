package tui

import "github.com/keakon/chord/internal/agent"

func (m *Model) recordToolErrorDiagnostic(event agent.ToolResultEvent) {
	if event.Status != agent.ToolResultStatusError || event.Diagnostic == nil {
		return
	}
	diagnostic := event.Diagnostic
	m.recordAgentError(event.AgentID, diagnostic.Err, diagnostic.Provider, diagnostic.Model, "", "", "", false)
}
