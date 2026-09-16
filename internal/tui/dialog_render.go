package tui

import "strings"

func renderDialogBox(width int, lines []string) string {
	body := strings.Join(styleDialogBodyLines(lines, dialogContentWidth(width)), "\n")
	return DirectoryBorderStyle.Width(width).Render(body)
}

func dialogContentWidth(width int) int {
	innerWidth := width - DirectoryBorderStyle.GetHorizontalPadding() - DirectoryBorderStyle.GetHorizontalBorderSize()
	if innerWidth < 0 {
		return 0
	}
	return innerWidth
}

func styleDialogBodyLines(lines []string, width int) []string {
	if currentTheme.DialogBg == "" || width <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		line = DialogBodyStyle.Render(line)
		line = preserveBackground(line, currentTheme.DialogBg)
		line = padLineToDisplayWidthWithStyle(DialogBodyStyle, line, width)
		out[i] = ensureStyledLineReset(line)
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
	if currentTheme.DialogBg == "" || body == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" || !strings.Contains(line, "\x1b[") {
			continue
		}
		lines[i] = ensureStyledLineReset(preserveBackground(line, currentTheme.DialogBg))
	}
	return strings.Join(lines, "\n")
}
