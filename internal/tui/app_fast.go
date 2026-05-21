package tui

import "github.com/keakon/chord/internal/agent"

func (m *Model) fastModeEnabled() bool {
	if m == nil || m.agent == nil {
		return false
	}
	reporter, ok := m.agent.(agent.FastModeReporter)
	return ok && reporter.FastModeEnabled()
}

func (m *Model) maybeFastModeShortcut(key string) bool {
	if !keyMatches(key, m.keyMap.FastMode) {
		return false
	}
	if m.agent == nil {
		return true
	}
	cmd := "/fast on"
	if m.fastModeEnabled() {
		cmd = "/fast off"
	}
	m.recordTUIDiagnostic("agent-command", "shortcut:%s %s", key, cmd)
	m.agent.SendUserMessage(cmd)
	return true
}
