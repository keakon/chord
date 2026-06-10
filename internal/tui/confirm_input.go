package tui

import (
	"encoding/json"
	"strings"

	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
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

// isConfirmGenericShortcut reports whether the key is a confirm-dialog action
// shortcut (A/D/E/M). Done dialogs intentionally omit some of these, so they
// are swallowed there instead of falling through to the generic switch.
func isConfirmGenericShortcut(key string) bool {
	switch key {
	case "a", "A", "d", "D", "e", "E", "m", "M":
		return true
	}
	return false
}

func isPlainKey(msg tea.KeyMsg, code rune) bool {
	key := msg.Key()
	return key.Code == code && key.Mod == 0
}

func confirmDialogWidth(totalWidth int) int {
	maxWidth := min(totalWidth-6, confirmDialogMaxWidth)
	if maxWidth < 40 {
		maxWidth = 40
	}
	return maxWidth
}

func confirmDialogInnerWidth(totalWidth int) int {
	innerWidth := confirmDialogWidth(totalWidth) - DirectoryBorderStyle.GetHorizontalPadding() - DirectoryBorderStyle.GetHorizontalBorderSize()
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
	ta.SetPromptFunc(0, func(textarea.PromptInfo) string {
		return ""
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

func (m *Model) activeConfirmTextarea() (*textarea.Model, string, bool) {
	if m.confirm.editing {
		return &m.confirm.editInput, "Command", true
	}
	if m.confirm.denyingWithReason {
		return &m.confirm.denyReasonInput, "Reason", true
	}
	return nil, "", false
}

func doneConfirmReportContent(req *ConfirmRequest) string {
	if req == nil {
		return ""
	}
	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed, req.DoneReport)
	if strings.TrimSpace(summary.DoneReport) != "" {
		return summary.DoneReport
	}
	if parsed, err := tools.ParseDoneArgs(json.RawMessage(req.ArgsJSON)); err == nil && strings.TrimSpace(parsed.Report) != "" {
		return strings.TrimSpace(parsed.Report)
	}
	return req.DoneReport
}

// handleConfirmKey processes key events while in ModeConfirm.
func (m *Model) handleConfirmKey(msg tea.KeyMsg) tea.Cmd {
	if m.confirm.editing {
		return m.handleConfirmEditKey(msg)
	}
	if m.confirm.pickingRule && m.confirm.editingRulePattern {
		return m.handleConfirmRulePatternEditKey(msg)
	}
	if m.confirm.pickingRule {
		return m.handleConfirmRulePickerKey(msg)
	}
	if m.confirm.denyingWithReason {
		return m.handleConfirmDenyReasonKey(msg)
	}

	if m.confirm.request != nil && toolNameKey(m.confirm.request.ToolName) == tools.NameDone {
		if msg.String() == "v" || msg.String() == "V" {
			return m.openContentViewer("Done report", doneConfirmReportContent(m.confirm.request))
		}
		if m.confirm.request.ForceDenyReason {
			if msg.Key().Code == tea.KeyEscape || msg.String() == "r" || msg.String() == "R" {
				m.confirm.denyingWithReason = true
				m.confirm.editError = ""
				m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
				m.recalcViewportSize()
				return textareaBlinkCmd()
			}
			// Force-deny Done dialog only exposes V/R/esc; swallow generic
			// shortcuts (A/D/E/M) so the handler stays consistent with the
			// rendered options. Other keys (e.g. scroll) fall through below.
			if isConfirmGenericShortcut(msg.String()) {
				return nil
			}
		} else {
			switch {
			case isPlainKey(msg, tea.KeyEnter) || msg.String() == "a" || msg.String() == "A":
				return m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
			case msg.Key().Code == tea.KeyEscape || msg.String() == "r" || msg.String() == "R":
				m.confirm.denyingWithReason = true
				m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
				m.recalcViewportSize()
				return textareaBlinkCmd()
			}
			// Done dialog only exposes A/V/R/esc; swallow generic shortcuts
			// (D/E/M) so the handler stays consistent with the rendered
			// options. Other keys (e.g. scroll) fall through below.
			if isConfirmGenericShortcut(msg.String()) {
				return nil
			}
		}
	}

	switch {
	case keyMatches(msg.String(), m.keyMap.ScrollDown):
		return m.repeatNormalVertical(1, 1)
	case keyMatches(msg.String(), m.keyMap.ScrollUp):
		return m.repeatNormalVertical(-1, 1)
	case keyMatches(msg.String(), m.keyMap.FullPageDown):
		prevOffset := m.viewport.offset
		m.viewport.ScrollDown(m.viewport.height)
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	case keyMatches(msg.String(), m.keyMap.FullPageUp):
		prevOffset := m.viewport.offset
		m.viewport.ScrollUp(m.viewport.height)
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	case keyMatches(msg.String(), m.keyMap.ScrollToBottom):
		prevOffset := m.viewport.offset
		m.viewport.ScrollToBottom()
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}

	switch {
	case isPlainKey(msg, tea.KeyEnter) || msg.String() == "a" || msg.String() == "A":
		if m.confirm.request != nil && m.confirm.request.ForceDenyReason {
			return nil
		}
		return m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})

	case msg.String() == "d" || msg.String() == "D":
		return m.resolveConfirm(ConfirmResult{Action: ConfirmDeny})

	case msg.String() == "r" || msg.String() == "R":
		m.confirm.denyingWithReason = true
		m.confirm.denyReasonInput = newConfirmTextarea(m.width, m.height, "")
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case msg.String() == "e" || msg.String() == "E":
		if m.confirm.request != nil && toolNameKey(m.confirm.request.ToolName) == tools.NameDone {
			return nil
		}
		m.confirm.editing = true
		m.confirm.editError = ""
		m.confirm.editInput = newConfirmTextarea(m.width, m.height, m.confirm.request.ArgsJSON)
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case msg.String() == "m" || msg.String() == "M":
		if m.confirm.request != nil {
			m.enterRulePicker()
			m.recalcViewportSize()
		}
		return nil

	case msg.Key().Code == tea.KeyEscape:
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
	candidates := suggestRulePatternsWithContext(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.NeedsApprovalRules, m.workingDir)
	if len(candidates) == 0 {
		return
	}

	// Pre-select every default candidate so a command blocked by multiple
	// matched ask rules pre-checks all of them, not just the first. The cursor
	// starts on the first default. When no candidate is a default, pre-select
	// nothing and leave the cursor on the first row.
	cursorIdx := 0
	selected := make(map[int]struct{})
	for i, c := range candidates {
		if c.Default {
			if len(selected) == 0 {
				cursorIdx = i
			}
			selected[i] = struct{}{}
		}
	}

	m.confirm.pickingRule = true
	m.confirm.candidates = candidates
	m.confirm.patternIdx = cursorIdx
	m.confirm.selectedPatterns = selected
	m.confirm.scopeIdx = 0
	m.confirm.scopes = []permission.RuleScope{
		permission.ScopeSession,
		permission.ScopeProject,
		permission.ScopeUserGlobal,
	}
}

// handleConfirmRulePickerKey processes key events in the rule picker sub-mode.
func (m *Model) handleConfirmRulePickerKey(msg tea.KeyMsg) tea.Cmd {
	if msg.Key().Code == tea.KeySpace {
		if len(m.confirm.candidates) == 0 {
			return nil
		}
		if m.confirm.selectedPatterns == nil {
			m.confirm.selectedPatterns = make(map[int]struct{})
		}
		if _, ok := m.confirm.selectedPatterns[m.confirm.patternIdx]; ok {
			delete(m.confirm.selectedPatterns, m.confirm.patternIdx)
		} else {
			m.confirm.selectedPatterns[m.confirm.patternIdx] = struct{}{}
		}
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil
	}

	switch {
	case msg.Key().Code == tea.KeyUp || msg.String() == "k":
		if m.confirm.patternIdx > 0 {
			m.confirm.patternIdx--
			m.confirm.renderCacheText = ""
			m.recalcViewportSize()
		}
		return nil

	case msg.Key().Code == tea.KeyDown || msg.String() == "j":
		if m.confirm.patternIdx < len(m.confirm.candidates)-1 {
			m.confirm.patternIdx++
			m.confirm.renderCacheText = ""
			m.recalcViewportSize()
		}
		return nil

	case msg.Key().Code == tea.KeyTab:
		// Cycle scope
		m.confirm.scopeIdx = (m.confirm.scopeIdx + 1) % len(m.confirm.scopes)
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil

	case isPlainKey(msg, tea.KeyEnter):
		// Submit: allow + add rule
		if len(m.confirm.candidates) == 0 || len(m.confirm.scopes) == 0 {
			return m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
		}
		patterns := m.confirmSelectedRulePatterns()
		if len(patterns) == 0 {
			m.confirm.editError = "Pattern is required."
			m.recalcViewportSize()
			return nil
		}
		scope := m.confirm.scopes[m.confirm.scopeIdx]
		return m.resolveConfirm(ConfirmResult{
			Action: ConfirmAllow,
			RuleIntent: &ConfirmRuleIntent{
				Patterns: patterns,
				Scope:    scope,
			},
		})

	case msg.String() == "e" || msg.String() == "E":
		if len(m.confirm.candidates) == 0 {
			return nil
		}
		m.confirm.editingRulePattern = true
		m.confirm.editError = ""
		m.confirm.rulePatternInput = newConfirmTextarea(m.width, m.height, m.confirm.candidates[m.confirm.patternIdx].Pattern)
		m.recalcViewportSize()
		return textareaBlinkCmd()

	case msg.Key().Code == tea.KeyEscape:
		// Back to main confirm dialog
		m.confirm.pickingRule = false
		m.confirm.candidates = nil
		m.confirm.selectedPatterns = nil
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil
	}
	return nil
}

func (m *Model) confirmSelectedRulePatterns() []string {
	if len(m.confirm.candidates) == 0 {
		return nil
	}
	selected := m.confirm.selectedPatterns
	if len(selected) == 0 {
		selected = map[int]struct{}{m.confirm.patternIdx: {}}
	}
	patterns := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for i := range m.confirm.candidates {
		if _, ok := selected[i]; !ok {
			continue
		}
		pattern := strings.TrimSpace(m.confirm.candidates[i].Pattern)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}
	return patterns
}

func (m *Model) handleConfirmRulePatternEditKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case isPlainKey(msg, tea.KeyEnter):
		pattern := strings.TrimSpace(m.confirm.rulePatternInput.Value())
		if pattern == "" {
			m.confirm.editError = "Pattern is required."
			m.recalcViewportSize()
			return nil
		}
		if len(m.confirm.candidates) == 0 {
			m.confirm.candidates = []PatternCandidate{{Pattern: pattern, Summary: "custom pattern", Default: true}}
			m.confirm.patternIdx = 0
		} else {
			m.confirm.candidates[m.confirm.patternIdx].Pattern = pattern
			m.confirm.candidates[m.confirm.patternIdx].Summary = "custom pattern"
			m.confirm.candidates[m.confirm.patternIdx].Broad = pattern == "*"
		}
		if m.confirm.selectedPatterns == nil {
			m.confirm.selectedPatterns = make(map[int]struct{})
		}
		m.confirm.selectedPatterns[m.confirm.patternIdx] = struct{}{}
		m.confirm.editingRulePattern = false
		m.confirm.rulePatternInput.Blur()
		m.confirm.editError = ""
		m.recalcViewportSize()
		return nil
	case msg.Key().Code == tea.KeyEscape:
		m.confirm.editingRulePattern = false
		m.confirm.rulePatternInput.Blur()
		m.confirm.editError = ""
		m.recalcViewportSize()
		return nil
	default:
		if m.confirm.editError != "" {
			m.confirm.editError = ""
		}
		var cmd tea.Cmd
		m.confirm.rulePatternInput, cmd = m.confirm.rulePatternInput.Update(msg)
		return cmd
	}
}

// handleConfirmDenyReasonKey processes key events in the deny-reason sub-mode.
func (m *Model) handleConfirmDenyReasonKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case isPlainKey(msg, tea.KeyEnter):
		reason := normalizeConfirmDenyReason(m.confirm.denyReasonInput.Value())
		if m.confirm.request != nil && toolNameKey(m.confirm.request.ToolName) == tools.NameDone && reason == "" {
			m.confirm.editError = "Done rejection requires a reason."
			m.recalcViewportSize()
			return nil
		}
		return m.resolveConfirm(ConfirmResult{
			Action:     ConfirmDeny,
			DenyReason: reason,
		})

	case msg.Key().Code == tea.KeyEscape:
		if m.confirm.request != nil && m.confirm.request.ForceDenyReason {
			return nil
		}
		// Back to main confirm dialog.
		m.confirm.denyingWithReason = false
		m.confirm.denyReasonInput.Blur()
		m.confirm.renderCacheText = ""
		m.recalcViewportSize()
		return nil

	default:
		if m.confirm.editError != "" {
			m.confirm.editError = ""
		}
		var cmd tea.Cmd
		m.confirm.denyReasonInput, cmd = m.confirm.denyReasonInput.Update(msg)
		return cmd
	}
}

func normalizeConfirmDenyReason(reason string) string {
	return strings.TrimSpace(reason)
}

// handleConfirmEditKey processes key events in the confirm-edit sub-mode.
func (m *Model) handleConfirmEditKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case isPlainKey(msg, tea.KeyEnter):
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

	case msg.Key().Code == tea.KeyEscape:
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
			for _, pattern := range result.RuleIntent.Patterns {
				m.addSessionRule(m.confirm.request.ToolName, pattern, result.RuleIntent.Scope)
			}
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
				Patterns: append([]string(nil), result.RuleIntent.Patterns...),
				Scope:    int(result.RuleIntent.Scope),
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
	m.terminalTitleRequestSeen = false
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
