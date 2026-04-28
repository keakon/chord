package tui

import (
	"encoding/json"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
)

const (
	confirmDialogMaxWidth = 90
	confirmDialogMaxRatio = 0.8
	confirmEditMinHeight  = 4
	confirmEditMaxHeight  = 10
)

type confirmRuleIntentResolver interface {
	ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *agent.ConfirmRuleIntent)
}

func confirmDialogWidth(totalWidth int) int {
	maxWidth := totalWidth - 6
	if maxWidth > confirmDialogMaxWidth {
		maxWidth = confirmDialogMaxWidth
	}
	if maxWidth < 40 {
		maxWidth = 40
	}
	return maxWidth
}

func confirmDialogInnerWidth(totalWidth int) int {
	innerWidth := confirmDialogWidth(totalWidth) - 2
	if innerWidth < 20 {
		innerWidth = 20
	}
	return innerWidth
}

func confirmDialogMaxHeight(totalHeight int) int {
	if totalHeight <= 0 {
		return 0
	}
	maxHeight := totalHeight - 4
	ratioHeight := int(float64(totalHeight) * confirmDialogMaxRatio)
	if ratioHeight > 0 && maxHeight > ratioHeight {
		maxHeight = ratioHeight
	}
	if maxHeight < 1 {
		maxHeight = 1
	}
	return maxHeight
}

func confirmDialogMaxBodyLines(totalHeight int) int {
	maxHeight := confirmDialogMaxHeight(totalHeight)
	if maxHeight <= 2 {
		return maxHeight
	}
	return maxHeight - 2
}

func confirmEditHeight(totalHeight int) int {
	height := totalHeight / 3
	if height < confirmEditMinHeight {
		height = confirmEditMinHeight
	}
	if height > confirmEditMaxHeight {
		height = confirmEditMaxHeight
	}
	return height
}

func newConfirmTextarea(width, height int, value string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetStyles(newTextareaStyles())
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "> "
		}
		return "  "
	})
	km := ta.KeyMap
	km.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	ta.KeyMap = km
	ta.SetWidth(confirmDialogInnerWidth(width))
	ta.SetHeight(confirmEditHeight(height))
	ta.SetValue(value)
	ta.CursorEnd()
	ta.Focus()
	return ta
}

// handleConfirmKey processes key events while in ModeConfirm.
func (m *Model) handleConfirmKey(msg tea.KeyMsg) tea.Cmd {
	if m.confirm.editing {
		return m.handleConfirmEditKey(msg)
	}
	if m.confirm.pickingRule {
		return m.handleConfirmRulePickerKey(msg)
	}
	if m.confirm.denyingWithReason {
		return m.handleConfirmDenyReasonKey(msg)
	}

	switch msg.String() {
	case "y", "Y":
		return m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})

	case "n", "N":
		return m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})

	case "r", "R":
		m.confirm.denyingWithReason = true
		m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case "e", "E":
		m.confirm.editing = true
		m.confirm.editError = ""
		m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case "a", "A":
		// Enter rule picker (not available for Delete)
		if m.confirm.request != nil && !strings.EqualFold(m.confirm.request.ToolName, "Delete") {
			m.enterRulePicker()
			m.recalcViewportSize()
		}
		return nil

	case "esc":
		return m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})
	}

	return nil
}

// enterRulePicker initializes the rule picker sub-mode.
func (m *Model) enterRulePicker() {
	if m.confirm.request == nil {
		return
	}
	req := m.confirm.request
	candidates := suggestRulePatterns(req.ToolName, req.ArgsJSON, req.NeedsApproval, m.workingDir)
	if len(candidates) == 0 {
		return
	}

	// Find default selection
	defaultIdx := 0
	for i, c := range candidates {
		if c.Default {
			defaultIdx = i
			break
		}
	}

	m.confirm.pickingRule = true
	m.confirm.candidates = candidates
	m.confirm.patternIdx = defaultIdx
	m.confirm.scopeIdx = 0
	m.confirm.scopes = []permission.RuleScope{
		permission.ScopeSession,
		permission.ScopeProject,
		permission.ScopeUserGlobal,
	}
}

// handleConfirmRulePickerKey processes key events in the rule picker sub-mode.
func (m *Model) handleConfirmRulePickerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		if m.confirm.patternIdx > 0 {
			m.confirm.patternIdx--
			m.confirm.renderCacheText = ""
			m.recalcViewportSize()
		}
		return nil

	case "down", "j":
		if m.confirm.patternIdx < len(m.confirm.candidates)-1 {
			m.confirm.patternIdx++
			m.confirm.renderCacheText = ""
			m.recalcViewportSize()
		}
		return nil

	case "tab":
		// Cycle scope
		m.confirm.scopeIdx = (m.confirm.scopeIdx + 1) % len(m.confirm.scopes)
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil

	case "enter":
		// Submit: allow + add rule
		if len(m.confirm.candidates) == 0 || len(m.confirm.scopes) == 0 {
			return m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
		}
		pattern := m.confirm.candidates[m.confirm.patternIdx].Pattern
		scope := m.confirm.scopes[m.confirm.scopeIdx]
		return m.resolveConfirm(ConfirmResult{
			Action: ConfirmAllow,
			RuleIntent: &ConfirmRuleIntent{
				Pattern: pattern,
				Scope:   scope,
			},
		})

	case "esc":
		// Back to main confirm dialog
		m.confirm.pickingRule = false
		m.confirm.candidates = nil
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil
	}
	return nil
}

// handleConfirmDenyReasonKey processes key events in the deny-reason sub-mode.
func (m *Model) handleConfirmDenyReasonKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		reason := normalizeConfirmDenyReason(m.confirm.denyReasonInput.Value())
		return m.resolveConfirm(ConfirmResult{
			Action:     ConfirmDeny,
			DenyReason: reason,
		})

	case "esc":
		// Back to main confirm dialog.
		m.confirm.denyingWithReason = false
		m.confirm.denyReasonInput.Blur()
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil

	default:
		var cmd tea.Cmd
		m.confirm.denyReasonInput, cmd = m.confirm.denyReasonInput.Update(msg)
		return cmd
	}
}

func normalizeConfirmDenyReason(reason string) string {
	reason = strings.TrimSpace(reason)
	reason = strings.ReplaceAll(reason, "\n", " ")
	if len([]rune(reason)) > 200 {
		reason = string([]rune(reason)[:200])
	}
	return reason
}

// handleConfirmEditKey processes key events in the confirm-edit sub-mode.
func (m *Model) handleConfirmEditKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		edited := m.confirm.editInput.Value()
		if !json.Valid([]byte(edited)) {
			m.confirm.editError = "Arguments must be valid JSON before submission."
			m.recalcViewportSize()
			return nil
		}
		return m.resolveConfirm(ConfirmResult{
			Action:        ConfirmAllow,
			FinalArgsJSON: edited,
		})

	case "esc":
		// Leave edit sub-mode but stay in ModeConfirm.
		m.confirm.editing = false
		m.confirm.editError = ""
		m.confirm.editInput.Blur()
		m.recalcViewportSize()
		return nil

	default:
		if m.confirm.editError != "" {
			m.confirm.editError = ""
		}
		var cmd tea.Cmd
		m.confirm.editInput, cmd = m.confirm.editInput.Update(msg)
		return cmd
	}
}

// resolveConfirm sends the result back via confirmResultCh (in-process) or
// agent.ResolveConfirm (remote), clears the confirm state, restores the
// previous mode, and returns a tea.Cmd that re-subscribes to the confirm
// channel (and optionally re-focuses the input).
func (m *Model) resolveConfirm(result ConfirmResult) tea.Cmd {
	if m.confirm.request == nil {
		return nil
	}
	if strings.TrimSpace(result.FinalArgsJSON) == "" {
		result.FinalArgsJSON = m.confirm.request.ArgsJSON
	}

	var pendingToast string
	if result.RuleIntent != nil && m.confirm.request != nil {
		if m.confirm.requestID == "" {
			// In-process: /rules reads from agent overlay; track locally as a fallback.
			m.addSessionRule(m.confirm.request.ToolName, result.RuleIntent.Pattern, result.RuleIntent.Scope)
		} else {
			// Remote mode: never mutate local /rules state (paths/undo must come from backend).
			if _, ok := m.agent.(confirmRuleIntentResolver); !ok {
				result.RuleIntent = nil
				pendingToast = "Backend does not support adding rules from confirm"
			}
		}
	}

	if m.confirm.requestID != "" {
		// Remote mode: send response to server via agent.ResolveConfirm.
		actionStr := confirmActionToStr(result.Action)
		if result.RuleIntent != nil {
			intent := &agent.ConfirmRuleIntent{
				Pattern: result.RuleIntent.Pattern,
				Scope:   int(result.RuleIntent.Scope),
			}
			if resolver, ok := m.agent.(confirmRuleIntentResolver); ok {
				resolver.ResolveConfirmWithRuleIntent(actionStr, result.FinalArgsJSON, result.EditSummary, result.DenyReason, m.confirm.requestID, intent)
			} else {
				m.agent.ResolveConfirm(actionStr, result.FinalArgsJSON, result.EditSummary, result.DenyReason, m.confirm.requestID)
			}
		} else {
			m.agent.ResolveConfirm(actionStr, result.FinalArgsJSON, result.EditSummary, result.DenyReason, m.confirm.requestID)
		}
	} else {
		// In-process: send result back to the blocking caller.
		select {
		case m.confirmResultCh <- result:
		default:
		}
	}

	prevMode := m.confirm.prevMode
	m.confirm = confirmState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	titleCmd := m.syncTerminalTitleState()

	// Re-subscribe to the confirmation channel and restore focus.
	cmds := []tea.Cmd{waitForConfirmRequest(m.confirmCh), titleCmd}
	if pendingToast != "" {
		cmds = append(cmds, m.enqueueToast(pendingToast, "warn"))
	}
	if m.displayState == stateBackground {
		cmds = append(cmds, m.updateBackgroundIdleSweepState())
	}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if prevMode == ModeInsert {
		cmds = append(cmds, m.input.Focus())
	}
	return tea.Batch(cmds...)
}
