package tui

import (
	"fmt"
	"image"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/keakon/bubbles/v2/textarea"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

const (
	questionDialogMaxWidth = 88
	questionInputHeight    = 4
)

func newQuestionTextarea(width int) textarea.Model {
	return newDialogTextarea(questionInputWidth(width), 1, questionInputHeight, "")
}

func questionInputWidth(totalWidth int) int {
	return max(dialogContentWidth(questionDialogWidth(totalWidth))-2, 1)
}

func questionDialogWidth(totalWidth int) int {
	return max(min(totalWidth-1, questionDialogMaxWidth), 5)
}

// renderQuestionDialog produces the question dialog as a bordered overlay box,
// matching the visual style of renderConfirmDialog.
func (m *Model) renderQuestionDialog() string {
	if m.question.request == nil {
		return ""
	}

	q := m.question.request.Item
	selectedKey := questionSelectedFingerprint(m.question.selected)
	if !m.question.custom && m.question.deadline.IsZero() && len(q.Options) > 0 && m.question.renderCacheText != "" &&
		m.question.renderCacheWidth == m.width &&
		m.question.renderCacheHeight == m.height &&
		m.question.renderCacheOffset == m.question.scrollOffset &&
		m.question.renderCacheFollow == m.question.followCursor &&
		m.question.renderCacheTheme == m.theme.Name &&
		m.question.renderCacheReq == m.question.request &&
		m.question.renderCacheCursor == m.question.cursor &&
		m.question.renderCacheSelected == selectedKey {
		return m.question.renderCacheText
	}

	// Cap dialog width for readability.
	maxWidth := questionDialogWidth(m.width)
	innerWidth := max(dialogContentWidth(maxWidth), 1)

	titleText := fmt.Sprintf("❓ %s", sanitizeToolDisplayText(q.Header))
	if m.question.request.AgentID != "" {
		titleText = fmt.Sprintf("❓ %s · %s", sanitizeToolDisplayText(m.question.request.AgentID), sanitizeToolDisplayText(q.Header))
	}

	var lines []string
	if m.question.interacting {
		lines = append(lines, "Cancelling automatic selection…")
	}
	if m.question.interacted {
		lines = append(lines, "Automatic selection cancelled; answer when ready")
	}
	if m.question.submitting {
		lines = append(lines, "Submitting…")
	}
	if q.DefaultOptionID != "" {
		for _, o := range q.Options {
			if o.ID == q.DefaultOptionID {
				lines = append(lines, "Default: "+sanitizeToolDisplayText(o.Label))
				break
			}
		}
	}

	focusLine := 0

	// Question text — split on <br> and newlines for multi-line display.
	qRaw := sanitizeToolDisplayText(strings.ReplaceAll(q.Question, "<br>", "\n"))
	for qLine := range strings.SplitSeq(qRaw, "\n") {
		for _, wrapped := range wrapText(qLine, max(innerWidth-2, 1)) {
			lines = append(lines, QuestionTextStyle.Render(wrapped))
		}
	}
	lines = append(lines, "")

	if len(q.Options) > 0 {
		currentOption := -1
		if !m.question.custom && m.question.cursor >= 0 && m.question.cursor < len(q.Options) {
			currentOption = m.question.cursor
		}

		// Render option list
		for i, opt := range q.Options {
			marker := "○"
			if m.question.selected[i] {
				marker = "●"
			}

			numKey := ""
			if i < 9 {
				numKey = fmt.Sprintf("%d.", i+1)
			}

			labelText := strings.TrimSpace(fmt.Sprintf("%s %s %s", numKey, marker, sanitizeToolDisplayText(opt.Label)))
			textPlain := labelText
			if i != currentOption && opt.Description != "" {
				maxDescWidth := innerWidth - runewidth.StringWidth(labelText) - 4
				if maxDescWidth > 10 {
					desc := truncateOneLine(sanitizeToolDisplayText(opt.Description), maxDescWidth)
					textPlain += DimStyle.Render("  " + desc)
				}
			}

			line := " " + textPlain
			if i == currentOption {
				focusLine = len(lines)
				line = QuestionSelectedStyle.MarginLeft(1).Width(innerWidth - 2).Render(labelText)
			}
			lines = append(lines, strings.Split(ansi.Hardwrap(line, innerWidth, true), "\n")...)
			if i == currentOption {
				lines = append(lines, renderCurrentQuestionOptionDescription(opt.Description, numKey, innerWidth)...)
			}
		}

		// "Type your own answer" virtual entry
		{
			idx := len(q.Options)
			text := "✎ Type your own answer"
			line := " " + text
			if idx == m.question.cursor && !m.question.custom {
				focusLine = len(lines)
				line = QuestionSelectedStyle.MarginLeft(1).Width(innerWidth - 2).Render(text)
			}
			lines = append(lines, strings.Split(ansi.Hardwrap(line, innerWidth, true), "\n")...)
		}
	}

	area := image.Rect(0, 0, m.width, m.height)
	cfg := OverlayConfig{Title: titleText, MaxWidth: maxWidth}
	cfg.Hint = questionHint(q, m.question.custom) + "  [PgUp/PgDn] Scroll"
	editing := m.question.custom || len(q.Options) == 0
	action, escape := "select", "hide"
	if editing || q.Multiple {
		action = "send"
	}
	if m.question.custom && len(q.Options) > 0 {
		escape = "hide"
	}
	secondary := "Tab custom  PgUp/PgDn scroll"
	if editing {
		secondary = "Shift+Enter newline  PgUp/PgDn scroll"
	} else if q.Multiple {
		secondary = "Space toggle  Tab custom  PgUp/PgDn scroll"
	}
	cfg.CompactHint = "Enter " + action + "  Esc " + escape + "  Ctrl+D decline  Ctrl+W withdraw\n" + secondary
	if !m.question.deadline.IsZero() {
		secs := int(ceilDuration(max(time.Until(m.question.deadline), 0), time.Second) / time.Second)
		if editing && m.height < 9 {
			cfg.Title = fmt.Sprintf("❓ %ds · %s", secs, sanitizeToolDisplayText(q.Header))
		} else {
			label := fmt.Sprintf("Closes in %ds", secs)
			if q.ResponsePolicy == tools.QuestionPolicyDefaultAllowed {
				label = fmt.Sprintf("Auto-select in %ds", secs)
			}
			cfg.Footer = QuestionTimeoutStyle.Render(truncateOneLine(label, innerWidth))
		}
	}
	if editing {
		editorHeight := max(min(questionInputHeight, overlayContentHeight(cfg, area)-1), 1)
		configureDialogTextarea(&m.question.input, questionInputWidth(m.width), 1, editorHeight)
		inputLines := strings.Split(strings.TrimSuffix(m.question.input.View(), "\n"), "\n")
		if len(inputLines) > 0 {
			inputLines[0] = QuestionSelectedStyle.Render("> ") + inputLines[0]
		}
		if cfg.Footer != "" {
			inputLines = append(inputLines, cfg.Footer)
		}
		cfg.Footer = strings.Join(inputLines, "\n")
	}
	bodyHeight := overlayContentHeight(cfg, area)
	m.question.bodyHeight, m.question.visibleBodyHeight = len(lines), bodyHeight
	offset := m.question.scrollOffset
	if m.question.followCursor && !m.question.custom && len(q.Options) > 0 {
		if focusLine < offset {
			offset = focusLine
		}
		if focusLine >= offset+bodyHeight {
			offset = focusLine - bodyHeight + 1
		}
	}
	offset = max(0, min(offset, len(lines)-bodyHeight))
	m.question.scrollOffset = offset
	visible := lines[offset:min(offset+bodyHeight, len(lines))]
	if len(lines) > bodyHeight {
		cfg.Title += fmt.Sprintf(" [%d-%d/%d]", offset+1, offset+len(visible), len(lines))
	}
	out, _ := RenderOverlay(cfg, strings.Join(visible, "\n"), area)
	if !m.question.custom && m.question.deadline.IsZero() && len(q.Options) > 0 {
		m.question.renderCacheWidth = m.width
		m.question.renderCacheHeight = m.height
		m.question.renderCacheOffset = m.question.scrollOffset
		m.question.renderCacheFollow = m.question.followCursor
		m.question.renderCacheTheme = m.theme.Name
		m.question.renderCacheReq = m.question.request
		m.question.renderCacheCursor = m.question.cursor
		m.question.renderCacheSelected = selectedKey
		m.question.renderCacheText = out
	}
	return out
}

func renderCurrentQuestionOptionDescription(description, numKey string, innerWidth int) []string {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	description = sanitizeToolDisplayText(description)
	prefix := " " + strings.Repeat(" ", runewidth.StringWidth(numKey)+3)
	wrapWidth := max(innerWidth-2, 1)
	available := max(wrapWidth-runewidth.StringWidth(prefix), 1)
	var lines []string
	for line := range strings.SplitSeq(strings.ReplaceAll(description, "<br>", "\n"), "\n") {
		for _, wrapped := range wrapText(line, available) {
			lines = append(lines, DimStyle.Render(prefix+wrapped))
		}
	}
	return lines
}

func questionHint(q tools.QuestionItem, customMode bool) string {
	if len(q.Options) == 0 {
		return "[Enter] Submit  [Shift+Enter/Ctrl+J] New line  [Esc] Hide  [Ctrl+D] Decline  [Ctrl+W] Withdraw"
	}
	if customMode {
		return "[Enter] Submit  [Shift+Enter/Ctrl+J] New line  [Tab] Options  [Esc] Hide  [Ctrl+D] Decline  [Ctrl+W] Withdraw"
	}

	parts := make([]string, 0, 4)
	if q.Multiple {
		parts = append(parts, "[Space] Toggle", "[Enter] Submit")
	} else {
		parts = append(parts, "[Enter] Select")
	}
	parts = append(parts, "[Tab] Custom", "[Esc] Hide  [Ctrl+D] Decline  [Ctrl+W] Withdraw")
	if quick := questionQuickSelectHint(len(q.Options)); quick != "" {
		parts = append(parts, quick)
	}
	return strings.Join(parts, "  ")
}

func questionSelectedFingerprint(selected map[int]bool) string {
	if len(selected) == 0 {
		return ""
	}
	idxs := make([]int, 0, len(selected))
	for idx, on := range selected {
		if on {
			idxs = append(idxs, idx)
		}
	}
	if len(idxs) == 0 {
		return ""
	}
	sort.Ints(idxs)
	parts := make([]string, len(idxs))
	for i, idx := range idxs {
		parts[i] = fmt.Sprintf("%d", idx)
	}
	return strings.Join(parts, ",")
}

func questionQuickSelectHint(optionCount int) string {
	maxNum := min(optionCount, 9)
	if maxNum <= 0 {
		return ""
	}
	if maxNum == 1 {
		return "[1] Quick-select"
	}
	return fmt.Sprintf("[1-%d] Quick-select", maxNum)
}
