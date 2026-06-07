package tui

import (
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func (m *Model) handleSessionAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.RunningModelChangedEvent:
		effects.refreshSidebar = true
		effects.invalidateUsage = true
		return true, effects
	case agent.ModelSelectEvent:
		m.inflightDraft = nil
		m.openModelSelectFor(evt.Target)
		return true, effects
	case agent.MCPSelectEvent:
		m.inflightDraft = nil
		m.openMCPSelect()
		return true, effects
	case agent.SessionSelectEvent:
		effects.addFollowup(m.openSessionSelect(evt.Sessions))
		return true, effects
	case agent.SessionSwitchStartedEvent:
		m.beginSessionSwitch(evt.Kind, evt.SessionID)
		return true, effects
	case agent.SessionRestoredEvent:
		m.thinkingStreamMsgIndex = -1
		m.thinkingStreamBlockIndex = 0
		reason := "session_restored"
		if m.startupRestorePending {
			reason = "startup_restored"
		}
		m.resetPendingScrollFlush()
		m.setFocusedAgent("")
		effects.refreshSidebar = true
		effects.invalidateUsage = true
		effects.addFollowup(func() tea.Msg { return sessionRestoredRebuildMsg{reason: reason} })
		return true, effects
	case agent.ConfirmRequestEvent:
		if evt.ArgsJSON != "" {
			if block, ok := m.findLastPendingToolBlockByName(evt.ToolName); ok {
				m.recordTUIDiagnostic("confirm-request", "tool=%s block=%d args_len=%d", evt.ToolName, block.ID, len(evt.ArgsJSON))
				block.Content = evt.ArgsJSON
				if toolNameKey(evt.ToolName) == tools.NameDone && strings.TrimSpace(evt.DoneReport) != "" {
					block.DoneReport = evt.DoneReport
				}
				block.InvalidateCache()
				m.updateViewportBlock(block)
			}
		}
		req := ConfirmRequest{ToolName: evt.ToolName, ArgsJSON: evt.ArgsJSON, DoneReport: evt.DoneReport, RequestID: evt.RequestID, Timeout: evt.Timeout, NeedsApproval: append([]string(nil), evt.NeedsApproval...), AlreadyAllowed: append([]string(nil), evt.AlreadyAllowed...), ForceDenyReason: evt.ForceDenyReason}
		effects.addFollowup(func() tea.Msg { return confirmRequestMsg{request: req} })
		effects.addFollowup(m.maybeTerminalNotifyCmd("Chord: Permission confirmation required"))
		return true, effects
	case agent.QuestionRequestEvent:
		effects.addFollowup(injectQuestionRequestFromEvent(evt))
		effects.addFollowup(m.maybeTerminalNotifyCmd("Chord: Question requires your input"))
		return true, effects
	default:
		return false, effects
	}
}
