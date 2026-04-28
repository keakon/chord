package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/permission"
)

// renderConfirmDialog produces the confirmation dialog as a bordered overlay
// box (same visual style as session/model select dialogs).
func (m *Model) renderConfirmDialog() string {
	if m.confirm.request == nil {
		return ""
	}
	if !m.confirm.editing && !m.confirm.denyingWithReason && m.confirm.deadline.IsZero() && m.confirm.renderCacheText != "" &&
		m.confirm.renderCacheWidth == m.width &&
		m.confirm.renderCacheHeight == m.height &&
		m.confirm.renderCacheTheme == m.theme.Name &&
		m.confirm.renderCacheReq == m.confirm.request {
		return m.confirm.renderCacheText
	}

	maxWidth := confirmDialogWidth(m.width)
	innerWidth := confirmDialogInnerWidth(m.width)

	req := m.confirm.request
	title := ConfirmSeparatorStyle.Render("⚠ Confirmation Required")

	if m.confirm.editing {
		header := ConfirmToolStyle.Render(
			fmt.Sprintf("Tool: %s — edit args:", req.ToolName),
		)
		editLine := m.confirm.editInput.View()
		hint := ConfirmHintStyle.Render("[Enter] Submit  [Shift+Enter/Ctrl+J] New line  [Esc] Cancel edit")
		lines := []string{title, "", header}
		lines = append(lines, strings.Split(editLine, "\n")...)
		if strings.TrimSpace(m.confirm.editError) != "" {
			lines = append(lines, "")
			for _, line := range wrapText(m.confirm.editError, max(10, innerWidth-2)) {
				lines = append(lines, ConfirmDenyStyle.Render("! "+line))
			}
		}
		lines = append(lines, "", hint)
		lines = fitConfirmDialogLines(lines, confirmDialogMaxBodyLines(m.height), 2)
		body := strings.Join(lines, "\n")
		return DirectoryBorderStyle.Width(maxWidth).Render(body)
	}

	if m.confirm.denyingWithReason {
		header := ConfirmToolStyle.Render(
			fmt.Sprintf("Tool: %s — deny with reason:", req.ToolName),
		)
		inputView := m.confirm.denyReasonInput.View()
		hint := ConfirmHintStyle.Render("[Enter] Deny  [Esc] Back")
		lines := []string{title, "", header}
		lines = append(lines, strings.Split(inputView, "\n")...)
		lines = append(lines, "", hint)
		lines = fitConfirmDialogLines(lines, confirmDialogMaxBodyLines(m.height), 2)
		body := strings.Join(lines, "\n")
		return DirectoryBorderStyle.Width(maxWidth).Render(body)
	}

	if m.confirm.pickingRule {
		return m.renderRulePicker(maxWidth, innerWidth)
	}

	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed)
	lines := m.renderConfirmSummary(title, summary, innerWidth)
	lines = append(lines, "", m.renderConfirmOptions())
	lines = fitConfirmDialogLines(lines, confirmDialogMaxBodyLines(m.height), 2)
	body := strings.Join(lines, "\n")
	out := DirectoryBorderStyle.Width(maxWidth).Render(body)
	if m.confirm.deadline.IsZero() {
		m.confirm.renderCacheWidth = m.width
		m.confirm.renderCacheHeight = m.height
		m.confirm.renderCacheTheme = m.theme.Name
		m.confirm.renderCacheReq = m.confirm.request
		m.confirm.renderCacheText = out
	}
	return out
}

func (m Model) renderConfirmSummary(title string, summary confirmSummary, innerWidth int) []string {
	lines := []string{title, ""}
	lines = append(lines, ConfirmToolStyle.Render("Tool: "+summary.ToolName))
	lines = append(lines, ConfirmToolStyle.Render("Action: "+summary.Action))
	if strings.EqualFold(summary.ToolName, "Delete") {
		lines = append(lines, DimStyle.Render("Risk: ")+confirmRiskStyle(summary.Risk))
		for _, warning := range summary.Warnings {
			for _, line := range wrapText(warning, max(10, innerWidth-2)) {
				lines = append(lines, ConfirmDenyStyle.Render("! ")+DimStyle.Render(line))
			}
		}
		fields := renderConfirmFields(summary.summaryFields(), innerWidth-1, false)
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
			lines = append(lines, ConfirmDenyStyle.Render("! ")+DimStyle.Render(line))
		}
	}

	fields := renderConfirmFields(summary.summaryFields(), innerWidth-1, false)
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
	parts := []string{
		ConfirmAllowStyle.Render("[Y] Allow"),
		ConfirmDenyStyle.Render("[N] Deny"),
		ConfirmDenyStyle.Render("[R] Deny+Reason"),
		ConfirmEditStyle.Render("[E] Edit"),
	}
	// Don't show [A] for Delete tool
	if m.confirm.request != nil && !strings.EqualFold(m.confirm.request.ToolName, "Delete") {
		parts = append(parts, ConfirmEditStyle.Render("[A] Add rule…"))
	}
	return strings.Join(parts, "  ")
}

// renderRulePicker renders the rule picker sub-dialog.
func (m *Model) renderRulePicker(maxWidth, innerWidth int) string {
	title := ConfirmSeparatorStyle.Render("⚠ Add rule — " + m.confirm.request.ToolName)

	lines := []string{title, ""}

	// Pattern section
	lines = append(lines, ConfirmToolStyle.Render("Pattern:"))
	for i, c := range m.confirm.candidates {
		marker := "( )"
		if i == m.confirm.patternIdx {
			marker = "(●)"
		}
		broadTag := ""
		if c.Broad {
			broadTag = "  ⚠ very broad"
		}
		line := fmt.Sprintf("  %s %s", marker, c.Pattern)
		if c.Summary != "" {
			line += "  — " + c.Summary
		}
		if broadTag != "" {
			line += broadTag
		}
		if i == m.confirm.patternIdx {
			lines = append(lines, ConfirmAllowStyle.Render(line))
		} else {
			lines = append(lines, DimStyle.Render(line))
		}
	}

	lines = append(lines, "")

	// Scope section
	lines = append(lines, ConfirmToolStyle.Render("Scope:"))
	roleName := ""
	if m.agent != nil {
		roleName = strings.TrimSpace(m.agent.CurrentRole())
	}
	for i, scope := range m.confirm.scopes {
		marker := "( )"
		if i == m.confirm.scopeIdx {
			marker = "(●)"
		}
		scopeLabel := scopeLabel(scope)
		scopePath := resolveRuleScopePath(scope, m.usageStatsProjectRoot(), m.homeDir, roleName)
		scopePathSuffix := ""
		if scopePath != "" {
			scopePathSuffix = " (" + scopePath + ")"
		}
		line := fmt.Sprintf("  %s %s%s", marker, scopeLabel, scopePathSuffix)
		if i == m.confirm.scopeIdx {
			lines = append(lines, ConfirmAllowStyle.Render(line))
		} else {
			lines = append(lines, DimStyle.Render(line))
		}
	}

	lines = append(lines, "")
	hint := ConfirmHintStyle.Render("[↑↓] pattern  [Tab] scope  [Enter] add rule + allow  [Esc] back")
	lines = append(lines, hint)

	lines = fitConfirmDialogLines(lines, confirmDialogMaxBodyLines(m.height), 2)
	body := strings.Join(lines, "\n")
	return DirectoryBorderStyle.Width(maxWidth).Render(body)
}

func scopeLabel(scope permission.RuleScope) string {
	switch scope {
	case permission.ScopeSession:
		return "This session only"
	case permission.ScopeProject:
		return "This project"
	case permission.ScopeUserGlobal:
		return "User global"
	default:
		return scope.String()
	}
}

func fitConfirmDialogLines(lines []string, maxLines int, preserveTail int) []string {
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	if preserveTail < 0 {
		preserveTail = 0
	}
	if preserveTail > maxLines-2 {
		preserveTail = max(0, maxLines-2)
	}
	headCount := maxLines - preserveTail - 1
	if headCount < 1 {
		headCount = 1
		preserveTail = max(0, maxLines-2)
	}
	hidden := len(lines) - headCount - preserveTail
	if hidden < 1 {
		return lines[:maxLines]
	}

	footerNote := "Press E to inspect/edit full args."
	if preserveTail == 0 || ansi.StringWidth(footerNote) > 48 {
		footerNote = "Press E to inspect full args."
	}
	marker := DimStyle.Render(fmt.Sprintf("... %d more lines hidden. %s", hidden, footerNote))

	out := make([]string, 0, maxLines)
	out = append(out, lines[:headCount]...)
	out = append(out, marker)
	if preserveTail > 0 {
		out = append(out, lines[len(lines)-preserveTail:]...)
	}
	return out
}
