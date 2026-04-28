package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

const (
	helpColumnGap      = 4
	helpMinColumnWidth = 44
	helpMaxColumns     = 2
)

func padDisplayRight(s string, width int) string {
	if width <= 0 {
		return s
	}
	sw := runewidth.StringWidth(s)
	if sw >= width {
		return runewidth.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", width-sw)
}

func renderHelpGroupLines(group HelpGroup, contentWidth int) []string {
	if len(group.Bindings) == 0 {
		return nil
	}
	if contentWidth < 24 {
		contentWidth = 24
	}

	keyWidth := 0
	for _, binding := range group.Bindings {
		if w := runewidth.StringWidth(keysDisplay(binding.Keys)); w > keyWidth {
			keyWidth = w
		}
	}
	keyWidth = minInt(keyWidth, 24)
	if maxKeyWidth := max(10, contentWidth/2); keyWidth > maxKeyWidth {
		keyWidth = maxKeyWidth
	}

	helpWidth := contentWidth - keyWidth - 4
	if helpWidth < 8 {
		helpWidth = 8
		keyWidth = max(8, contentWidth-helpWidth-4)
	}

	lines := []string{DialogTitleStyle.Render(group.Title)}
	for _, binding := range group.Bindings {
		keyLabel := keysDisplay(binding.Keys)
		if keyLabel == "" || binding.Help == "" {
			continue
		}
		wrapped := wrapText(binding.Help, helpWidth)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		lines = append(lines,
			fmt.Sprintf("  %s  %s", InputPromptStyle.Render(padDisplayRight(keyLabel, keyWidth)), wrapped[0]),
		)
		for _, extra := range wrapped[1:] {
			lines = append(lines, fmt.Sprintf("  %s  %s", strings.Repeat(" ", keyWidth), extra))
		}
	}
	return append(lines, "")
}

func helpColumnCount(groupCount, contentWidth int) int {
	maxCols := minInt(helpMaxColumns, groupCount)
	for cols := maxCols; cols > 1; cols-- {
		if contentWidth >= cols*helpMinColumnWidth+(cols-1)*helpColumnGap {
			return cols
		}
	}
	return 1
}

func shortestHelpColumn(heights []int) int {
	best := 0
	for i := 1; i < len(heights); i++ {
		if heights[i] < heights[best] {
			best = i
		}
	}
	return best
}

func flattenHelpBlocks(blocks [][]string) []string {
	lines := make([]string, 0)
	for _, block := range blocks {
		lines = append(lines, block...)
	}
	return trimTrailingHelpBlankLines(lines)
}

func trimTrailingHelpBlankLines(lines []string) []string {
	end := len(lines)
	for end > 0 {
		plain := ansiStrip.ReplaceAllString(lines[end-1], "")
		if strings.TrimSpace(plain) != "" {
			break
		}
		end--
	}
	return lines[:end]
}

func layoutHelpBlocks(blocks [][]string, contentWidth int) []string {
	cols := helpColumnCount(len(blocks), contentWidth)
	if cols <= 1 {
		return flattenHelpBlocks(blocks)
	}

	columnWidth := (contentWidth - helpColumnGap*(cols-1)) / cols
	columns := make([][]string, cols)
	heights := make([]int, cols)
	for _, block := range blocks {
		idx := shortestHelpColumn(heights)
		columns[idx] = append(columns[idx], block...)
		heights[idx] += len(block)
	}

	maxHeight := 0
	for _, h := range heights {
		if h > maxHeight {
			maxHeight = h
		}
	}

	gap := strings.Repeat(" ", helpColumnGap)
	lines := make([]string, 0, maxHeight)
	for row := 0; row < maxHeight; row++ {
		var parts []string
		for col := 0; col < cols; col++ {
			line := ""
			if row < len(columns[col]) {
				line = columns[col][row]
			}
			parts = append(parts, padLineToDisplayWidth(line, columnWidth))
		}
		lines = append(lines, strings.Join(parts, gap))
	}

	return trimTrailingHelpBlankLines(lines)
}
