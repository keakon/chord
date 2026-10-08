package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// renderConfirmDialog produces the confirmation dialog as a bordered overlay
// box (same visual style as session/model select dialogs).
func (m *Model) renderConfirmDialog() string {
	if m.confirm.request == nil {
		return ""
	}
	if !m.confirm.editing && !m.confirm.pickingRule && !m.confirm.denyingWithReason && m.confirm.deadline.IsZero() && m.confirm.renderCacheText != "" &&
		m.confirm.renderCacheWidth == m.width &&
		m.confirm.renderCacheHeight == m.height &&
		m.confirm.renderCacheOffset == m.confirm.scroll.offset &&
		m.confirm.renderCacheTheme == m.theme.Name &&
		m.confirm.renderCacheReq == m.confirm.request {
		return m.confirm.renderCacheText
	}

	maxWidth := confirmDialogWidth(m.width)
	innerWidth := confirmDialogInnerWidth(m.width)

	req := m.confirm.request

	if m.confirm.editing {
		submit := primaryHint("Enter", "allow")
		submit.danger = toolNameKey(req.ToolName) == tools.NameDelete
		return m.renderConfirmEditor(maxWidth, "Edit args · "+req.ToolName, &m.confirm.editInput,
			submit, hint("Esc", "back"))
	}
	if m.confirm.denyingWithReason {
		exit := hint("Esc", "back")
		if req.ForceDenyReason {
			exit = hintText("")
		}
		return m.renderConfirmEditor(maxWidth, "Deny with reason · "+req.ToolName, &m.confirm.denyReasonInput,
			primaryHint("Enter", "deny"), exit)
	}

	if m.confirm.pickingRule {
		return m.renderRulePicker(maxWidth)
	}

	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed, req.DoneReport)
	lines := m.renderConfirmSummary("", summary, innerWidth)[2:]
	actions := m.renderConfirmOptions()
	out := renderScrollableDialog(OverlayConfig{
		Title: "⚠ Confirmation Required", MaxWidth: maxWidth,
		Hint: actions, CompactHint: actions,
	}, lines, m.confirmDialogArea(), &m.confirm.scroll)
	if m.confirm.deadline.IsZero() {
		m.confirm.renderCacheWidth = m.width
		m.confirm.renderCacheHeight = m.height
		m.confirm.renderCacheOffset = m.confirm.scroll.offset
		m.confirm.renderCacheTheme = m.theme.Name
		m.confirm.renderCacheReq = m.confirm.request
		m.confirm.renderCacheText = out
	}
	return out
}

func (m Model) renderConfirmSummary(title string, summary confirmSummary, innerWidth int) []string {
	lines := []string{title, ""}
	if toolNameKey(summary.ToolName) == tools.NameDone {
		if strings.TrimSpace(summary.DoneReport) != "" {
			lines = append(lines, renderDialogMarkdownContent(summary.DoneReport, max(10, innerWidth-2))...)
		}
		return lines
	}
	lines = append(lines, ConfirmToolStyle.Render("Tool: "+summary.ToolName))
	lines = append(lines, ConfirmToolStyle.Render("Action: "+summary.Action))
	if toolNameKey(summary.ToolName) == tools.NameDelete {
		lines = append(lines, DimStyle.Render("Risk: ")+confirmRiskStyle(summary.Risk))
		for _, warning := range summary.Warnings {
			for _, line := range wrapText(warning, max(10, innerWidth-2)) {
				lines = append(lines, DialogWarningStyle.Render("! ")+DimStyle.Render(line))
			}
		}
		fields := renderConfirmFields(summary.summaryFields(), innerWidth-1)
		if len(fields) > 0 {
			lines = append(lines, "")
			lines = append(lines, fields...)
		}
		if len(summary.NeedsApproval) > 0 {
			lines = append(lines, "")
			lines = append(lines, renderConfirmPathSection("Needs approval", summary.NeedsApproval, innerWidth)...)
		}
		if len(summary.AlreadyAllowed) > 0 {
			lines = append(lines, "")
			lines = append(lines, renderConfirmPathSection("Already allowed by rules", summary.AlreadyAllowed, innerWidth)...)
		}
		return lines
	}
	lines = append(lines, DimStyle.Render("Risk: ")+confirmRiskStyle(summary.Risk))

	for _, warning := range summary.Warnings {
		for _, line := range wrapText(warning, max(10, innerWidth-2)) {
			lines = append(lines, DialogWarningStyle.Render("! ")+DimStyle.Render(line))
		}
	}

	fields := renderConfirmFields(summary.summaryFields(), innerWidth-1)
	if len(fields) > 0 {
		lines = append(lines, "")
		lines = append(lines, fields...)
	}
	return lines
}

func renderConfirmPathSection(title string, paths []string, innerWidth int) []string {
	if len(paths) == 0 {
		return nil
	}
	lines := []string{ConfirmToolStyle.Render(fmt.Sprintf("%s (%d)", title, len(paths)))}
	width := max(10, innerWidth-4)
	for _, path := range paths {
		for _, wrapped := range wrapText(path, width) {
			lines = append(lines, DimStyle.Render("- ")+ConfirmToolStyle.Render(wrapped))
		}
	}
	return lines
}

func (m Model) renderConfirmOptions() string {
	rows := m.confirmOptionRows()
	var lines []string
	for _, row := range rows {
		parts := make([]string, len(row.options))
		for i, option := range row.options {
			parts[i] = renderConfirmOption(option.spec)
		}
		lines = append(lines, strings.Join(parts, "  "))
	}
	return strings.Join(lines, "\n")
}

// renderRulePicker keeps the selected scope and decision outside the candidate window.
func (m *Model) renderRulePicker(maxWidth int) string {
	if m.confirm.editingRulePattern {
		return m.renderConfirmEditor(maxWidth, "Edit rule pattern", &m.confirm.rulePatternInput,
			primaryHint("Enter", "save"), hint("Esc", "back"))
	}
	width := max(dialogContentWidth(maxWidth), 1)
	scope := permission.ScopeSession
	if len(m.confirm.scopes) > 0 {
		scope = m.confirm.scopes[m.confirm.scopeIdx]
	}
	roleName := ""
	if m.agent != nil {
		roleName = strings.TrimSpace(m.agent.CurrentRole())
	}
	lines := []string{}
	if path := resolveRuleScopePath(scope, m.usageStatsContentRoot(), roleName); path != "" {
		lines = append(lines, "Rule file: "+path)
	}
	lines = append(lines, "Pattern:")
	focusLine := 0
	for i, c := range m.confirm.candidates {
		cursor, checked := " ", "[ ]"
		if i == m.confirm.patternIdx {
			cursor = "❯"
			focusLine = len(wrapDialogLines(lines, width))
		}
		if _, ok := m.confirm.selectedPatterns[i]; ok {
			checked = "[x]"
		}
		line := fmt.Sprintf("%s %s %s", cursor, checked, c.Pattern)
		if c.Summary != "" {
			line += " — " + c.Summary
		}
		if c.Broad {
			line += " ⚠ very broad"
		}
		if i == m.confirm.patternIdx {
			line = SelectedStyle.Width(width).Render(line)
		}
		lines = append(lines, line)
	}
	cfg := OverlayConfig{
		Title: "⚠ Remember rule — " + m.confirm.request.ToolName, MaxWidth: maxWidth,
		Footer: "Scope: " + scopeLabelStr(scope),
		Hint: hintLine(primaryHint("Enter", "remember + allow"), hint("Esc", "back"),
			hint("↑↓", "pattern"), hint("Space", "select"), hint("E", "edit"), hint("Tab", "scope")),
		CompactHint: hintLine(primaryHint("Enter", "save"), hint("Esc", "back")),
	}
	if width < 30 {
		cfg.Title = "Remember + allow"
	}
	if m.confirm.editError != "" {
		cfg.Footer = DialogDangerStyle.Render(truncateOneLine(m.confirm.editError, width))
	}
	area := m.confirmDialogArea()
	firstRender := m.confirm.ruleScroll.visible == 0
	out := renderScrollableDialog(cfg, lines, area, &m.confirm.ruleScroll)
	if firstRender || m.confirm.ruleFollowCursor {
		old := m.confirm.ruleScroll.offset
		if focusLine < old {
			m.confirm.ruleScroll.offset = focusLine
		} else if focusLine >= old+m.confirm.ruleScroll.visible {
			m.confirm.ruleScroll.offset = focusLine - m.confirm.ruleScroll.visible + 1
		}
		if m.confirm.ruleScroll.offset != old {
			out = renderScrollableDialog(cfg, lines, area, &m.confirm.ruleScroll)
		}
	}
	return out
}
