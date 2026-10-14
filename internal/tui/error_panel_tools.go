package tui

import (
	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func (m *Model) recordToolErrorDiagnostic(event agent.ToolResultEvent) {
	if event.Status != message.ToolStatusError || event.Diagnostic == nil {
		return
	}
	diagnostic := event.Diagnostic
	m.recordAgentError(event.AgentID, diagnostic.Err, diagnostic.Provider, diagnostic.Model, "", "", "", false)
}
