package tui

import (
	"bytes"
	"encoding/json"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

type confirmDialogAction uint8

const (
	confirmDialogAllow confirmDialogAction = iota
	confirmDialogDeny
	confirmDialogDenyReason
	confirmDialogView
	confirmDialogEdit
	confirmDialogAddRule
)

// handleConfirmAction is shared by keyboard shortcuts and mouse hit targets so
// an input method cannot bypass the same Done and force-deny guards.
func (m *Model) handleConfirmAction(action confirmDialogAction) tea.Cmd {
	if m.confirm.request == nil {
		return nil
	}

	switch action {
	case confirmDialogAllow:
		if m.confirm.request.ForceDenyReason {
			return nil
		}
		return m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})

	case confirmDialogDeny:
		if toolNameKey(m.confirm.request.ToolName) == tools.NameDone {
			return nil
		}
		return m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})

	case confirmDialogDenyReason:
		m.confirm.denyingWithReason = true
		m.confirm.editError = ""
		m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case confirmDialogView:
		if toolNameKey(m.confirm.request.ToolName) == tools.NameDone {
			return m.openContentViewer("Done report", doneConfirmReportContent(m.confirm.request))
		}
		content := m.confirm.request.ArgsJSON
		var out bytes.Buffer
		if err := json.Indent(&out, []byte(content), "", "  "); err == nil {
			content = out.String()
		}
		cmd := m.openContentViewer("Tool arguments", content)
		// Arguments are literal data, not Markdown: formatting must not hide
		// characters the user is being asked to approve.
		m.contentViewer.literal = true
		return cmd

	case confirmDialogEdit:
		if toolNameKey(m.confirm.request.ToolName) == tools.NameDone {
			return nil
		}
		m.confirm.editing = true
		m.confirm.editError = ""
		m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case confirmDialogAddRule:
		m.enterRulePicker()
		m.recalcViewportSize()
		return nil
	}

	return nil
}
