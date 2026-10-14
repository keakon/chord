package tui

import (
	"strings"

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

// wrapDialogLines runs before scroll-window sizing, so navigation counts the
// physical rows that will be drawn rather than logical lines that may wrap.
func wrapDialogLines(lines []string, width int) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.Split(terminaltext.WidthMethod.Wrap(line, max(width, 1), ""), "\n")...)
	}
	return out
}
