package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

const (
	questionDialogMaxWidth = 88
	questionInputWidthPad  = 4
	questionInputMinWidth  = 20
	questionInputHeight    = 4
)

func newQuestionTextarea(width int) textarea.Model {
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
	ta.SetWidth(questionInputWidth(width) + 2)
	ta.SetHeight(questionInputHeight)
	return ta
}

func questionInputWidth(totalWidth int) int {
	w := min(totalWidth-6, questionDialogMaxWidth) - questionInputWidthPad
	if w < questionInputMinWidth {
		w = questionInputMinWidth
	}
	return w
}

// renderQuestionDialog produces the question dialog as a bordered overlay box,
// matching the visual style of renderConfirmDialog.
func (m *Model) renderQuestionDialog() string {
	if m.question.request == nil {
		return ""
	}
	if m.question.currentQ >= len(m.question.request.Questions) {
		return ""
	}

	q := m.question.request.Questions[m.question.currentQ]
	selectedKey := questionSelectedFingerprint(m.question.selected)
	if !m.question.custom && m.question.deadline.IsZero() && len(q.Options) > 0 && m.question.renderCacheText != "" &&
		m.question.renderCacheWidth == m.width &&
		m.question.renderCacheTheme == m.theme.Name &&
		m.question.renderCacheReq == m.question.request &&
		m.question.renderCacheCurrentQ == m.question.currentQ &&
		m.question.renderCacheCursor == m.question.cursor &&
		m.question.renderCacheSelected == selectedKey {
		return m.question.renderCacheText
	}

	// Cap dialog width for readability.
	maxWidth := m.width - 6
	if maxWidth > questionDialogMaxWidth {
		maxWidth = questionDialogMaxWidth
	}
	if maxWidth < 40 {
		maxWidth = 40
	}
	innerWidth := maxWidth - 2 // account for border padding
	if innerWidth < 20 {
		innerWidth = 20
	}

	total := len(m.question.request.Questions)

	// Title line with optional progress indicator.
	titleText := fmt.Sprintf("❓ %s", q.Header)
	if total > 1 {
		titleText = fmt.Sprintf("❓ %s  (%d / %d)", q.Header, m.question.currentQ+1, total)
	}
	title := QuestionSeparatorStyle.Render(titleText)

	var lines []string
	lines = append(lines, title, "")

	// Question text — split on <br> and newlines for multi-line display.
	qRaw := strings.ReplaceAll(q.Question, "<br>", "\n")
	for _, qLine := range strings.Split(qRaw, "\n") {
		for _, wrapped := range wrapText(qLine, innerWidth-4) {
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

			labelText := strings.TrimSpace(fmt.Sprintf("%s %s %s", numKey, marker, opt.Label))
			textPlain := labelText
			if i != currentOption && opt.Description != "" {
				maxDescWidth := innerWidth - runewidth.StringWidth(labelText) - 4
				if maxDescWidth > 10 {
					desc := truncateOneLine(opt.Description, maxDescWidth)
					textPlain += DimStyle.Render("  " + desc)
				}
			}

			line := " " + textPlain
			if i == currentOption {
				line = QuestionSelectedStyle.MarginLeft(1).Width(innerWidth - 2).Render(labelText)
			}
			lines = append(lines, line)
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
				line = QuestionSelectedStyle.MarginLeft(1).Width(innerWidth - 2).Render(text)
			}
			lines = append(lines, line)
		}
	}

	// Custom text input (shown when focused or when no options)
	if m.question.custom || len(q.Options) == 0 {
		inputView := strings.TrimSuffix(m.question.input.View(), "\n")
		lines = append(lines, strings.Split(inputView, "\n")...)
	}

	lines = append(lines, "")
	lines = append(lines, QuestionHintStyle.Render(questionHint(q, m.question.custom)))

	// Timeout countdown
	if !m.question.deadline.IsZero() {
		remaining := time.Until(m.question.deadline)
		if remaining < 0 {
			remaining = 0
		}
		secs := int(remaining.Seconds()) + 1
		lines = append(lines, QuestionTimeoutStyle.Render(
			fmt.Sprintf("⏱ Auto-cancel in %ds", secs),
		))
	}

	body := strings.Join(lines, "\n")
	out := DirectoryBorderStyle.Width(maxWidth).Render(body)
	if !m.question.custom && m.question.deadline.IsZero() && len(q.Options) > 0 {
		m.question.renderCacheWidth = m.width
		m.question.renderCacheTheme = m.theme.Name
		m.question.renderCacheReq = m.question.request
		m.question.renderCacheCurrentQ = m.question.currentQ
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
	prefix := " " + strings.Repeat(" ", runewidth.StringWidth(numKey)+3)
	wrapWidth := innerWidth - 2
	if wrapWidth < 10 {
		wrapWidth = 10
	}
	available := wrapWidth - runewidth.StringWidth(prefix)
	if available < 10 {
		available = 10
	}
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(description, "<br>", "\n"), "\n") {
		for _, wrapped := range wrapText(line, available) {
			lines = append(lines, DimStyle.Render(prefix+wrapped))
		}
	}
	return lines
}

func questionHint(q tools.QuestionItem, customMode bool) string {
	if len(q.Options) == 0 {
		return "[Enter] Submit  [Shift+Enter/Ctrl+J] New line  [Esc] Cancel"
	}
	if customMode {
		return "[Enter] Submit  [Shift+Enter/Ctrl+J] New line  [Tab/Esc] Back to options"
	}

	parts := make([]string, 0, 4)
	if q.Multiple {
		parts = append(parts, "[Space] Toggle", "[Enter] Submit")
	} else {
		parts = append(parts, "[Enter] Select")
	}
	parts = append(parts, "[Tab] Custom")
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
