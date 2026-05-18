package tui

import "github.com/keakon/chord/internal/agent"

func (m *Model) fastModeEnabled() bool {
	if m == nil || m.agent == nil {
		return false
	}
	reporter, ok := m.agent.(agent.FastModeReporter)
	return ok && reporter.FastModeEnabled()
}
