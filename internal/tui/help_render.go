package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/buildinfo"
)

func (m Model) helpLines(width int) []string {
	if width <= 0 {
		width = 80
	}
	contentWidth := max(width-4, 24)

	lines := []string{centerHelpLine("Chord "+buildinfo.Current().Short(), width), ""}
	intro := []string{
		"Press Esc, q, or ? to close. /help also opens this view.",
		fmt.Sprintf("%s completes a visible / command suggestion. Otherwise it sends, queues when busy, or continues when idle with empty input.", keysDisplay(m.keyMap.InsertSubmit)),
		"Ctrl+C starts quit confirmation.",
		"In normal mode, Esc stops the current run. Queued drafts are committed without resuming the run.",
		"Click a queued draft to edit it, or click [del] to remove it before send.",
	}
	for _, paragraph := range intro {
		for _, line := range wrapText(paragraph, contentWidth) {
			lines = append(lines, DimStyle.Render(line))
		}
	}
	lines = append(lines, "")

	groups := m.keyMap.HelpGroups()
	count := 0
	for _, group := range groups {
		if len(group.Bindings) > 0 {
			count++
		}
	}
	cols := helpColumnCount(count, contentWidth)
	columnWidth := (contentWidth - (cols-1)*helpColumnGap) / cols
	var blocks [][]string
	for _, group := range groups {
		if block := renderHelpGroupLines(group, columnWidth); len(block) > 0 {
			blocks = append(blocks, block)
		}
	}

	lines = append(lines, layoutHelpBlocks(blocks, contentWidth)...)
	return lines
}

func centerHelpLine(line string, width int) string {
	lineWidth := ansi.StringWidth(line)
	if width <= lineWidth {
		return line
	}
	return strings.Repeat(" ", (width-lineWidth)/2) + line
}

func (m *Model) renderHelpView() string {
	width := m.viewport.width
	height := m.viewport.height
	if width <= 0 || height <= 0 {
		return ""
	}
	offset := max(m.help.scrollOffset, 0)
	lines := m.cachedHelpLines(width)
	if offset > len(lines) {
		offset = len(lines)
	}
	if m.help.renderCacheText != "" &&
		m.help.renderCacheWidth == width &&
		m.help.renderCacheHeight == height &&
		m.help.renderCacheOffset == offset &&
		m.help.renderCacheTheme == m.theme.Name {
		return m.help.renderCacheText
	}

	visible := lines[offset:]
	if len(visible) > height {
		visible = visible[:height]
	}
	for len(visible) < height {
		visible = append(visible, "")
	}

	rendered := make([]string, 0, len(visible))
	for _, line := range visible {
		line = ansi.Truncate(line, width, "…")
		line = padLineToDisplayWidth(line, width)
		rendered = append(rendered, line)
	}
	out := strings.Join(rendered, "\n")
	m.help.renderCacheWidth = width
	m.help.renderCacheHeight = height
	m.help.renderCacheOffset = offset
	m.help.renderCacheTheme = m.theme.Name
	m.help.renderCacheText = out
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
