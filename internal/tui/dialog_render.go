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
