package tui

import (
	"strings"

	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

func renderDialogBox(width int, lines []string) string {
	contentWidth := dialogContentWidth(width)
	bounded := make([]string, len(lines))
	for i, line := range lines {
		bounded[i] = ansi.Truncate(line, contentWidth, "…")
	}
	body := strings.Join(styleDialogBodyLines(bounded, contentWidth), "\n")
	return currentStyles.DirectoryBorderStyle.Width(width).Render(body)
}

func dialogContentWidth(width int) int {
	innerWidth := width - currentStyles.DirectoryBorderStyle.GetHorizontalPadding() - currentStyles.DirectoryBorderStyle.GetHorizontalBorderSize()
	if innerWidth < 0 {
		return 0
	}
	return innerWidth
}

func styleDialogBodyLines(lines []string, width int) []string {
	if currentStyles.Palette.DialogBg == "" || width <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		line = currentStyles.DialogBodyStyle.Render(line)
		line = terminaltext.PreserveBackground(line, currentStyles.Palette.DialogBg)
		line = padLineToDisplayWidthWithStyle(currentStyles.DialogBodyStyle, line, width)
		out[i] = terminaltext.EnsureReset(line)
	}
	return out
}

// preserveDialogBackground re-applies DialogBg after inner ANSI resets on each
// line of an already-joined body. It keeps existing layout/widths untouched:
// unlike styleDialogBodyLines it does not add padding or wrap with
// DialogBodyStyle, so selector rows that intentionally carry SelectedBg and
// image blocks that carry UserCardBg keep their own surfaces while trailing
// spaces and multi-segment rows (JoinHorizontal buttons, "Scope: "+styled,
// textinput/textarea View lines) fall back to DialogBg instead of the
// terminal default.
func preserveDialogBackground(body string) string {
	if currentStyles.Palette.DialogBg == "" || body == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" || !strings.Contains(line, "\x1b[") {
			continue
		}
		lines[i] = terminaltext.EnsureReset(terminaltext.PreserveBackground(line, currentStyles.Palette.DialogBg))
	}
	return strings.Join(lines, "\n")
}

// renderDialogSelectedRow renders a selector row on the SelectedStyle surface
// across width. lipgloss opens the style once per line and never reopens it
// after an SGR reset inside content, so text after an inline styled segment
// (such as a tool label) would lose SelectedFg/SelectedBg, and the dialog
// background pass would then fill it with DialogBg. Reopening the selection
// style after each inner reset keeps the whole row on its own surface; the
// dialog pass still inserts DialogBg after those resets, but the reopened
// selection sequence follows it and wins. Plain content skips the extra work.
func renderDialogSelectedRow(width int, content string) string {
	style := currentStyles.SelectedStyle
	if strings.Contains(content, "\x1b[") {
		content = terminaltext.ReapplyAfterResets(content, styleOpenSequence(style))
	}
	return style.Width(width).Render(content)
}

// styleOpenSequence returns the escape sequences lipgloss emits before the
// first content cell of style, or "" when style emits none (or adds padding or
// margins before the content).
func styleOpenSequence(style lipgloss.Style) string {
	const probe = "x"
	rendered := style.Render(probe)
	i := 0
	for i < len(rendered) && rendered[i] == '\x1b' {
		i = terminaltext.SkipANSI(rendered, i)
	}
	if i == 0 || !strings.HasPrefix(rendered[i:], probe) {
		return ""
	}
	return rendered[:i]
}

// wrapDialogLines runs before scroll-window sizing, so navigation counts the
// physical rows that will be drawn rather than logical lines that may wrap.
func wrapDialogLines(lines []string, width int) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.Split(terminaltext.WidthMethod.Wrap(line, max(width, 1), ""), "\n")...)
	}
	return out
}
