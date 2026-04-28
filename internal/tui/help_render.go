package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) helpLines(width int) []string {
	if width <= 0 {
		width = 80
	}
	contentWidth := width - 4
	if contentWidth < 24 {
		contentWidth = 24
	}

	lines := []string{
		DialogTitleStyle.Render("Keyboard Help"),
		DimStyle.Render("Press ? or Esc to close. /help also opens this view."),
		DimStyle.Render("In the local main-agent view, Enter sends, queues (when busy), or continues (when idle with empty input). Ctrl+C starts quit confirmation; normal-mode Esc stops the current run, and queued drafts are committed without auto-resuming on that cancel."),
		DimStyle.Render("Click a queued draft to edit it, or click [del] to remove it before send."),
		"",
	}

	var blocks [][]string
	for _, group := range m.keyMap.HelpGroups() {
		if block := renderHelpGroupLines(group, contentWidth); len(block) > 0 {
			blocks = append(blocks, block)
		}
	}

	lines = append(lines, layoutHelpBlocks(blocks, contentWidth)...)
	return lines
}

func (m *Model) renderHelpView() string {
	width := m.viewport.width
	height := m.viewport.height
	if width <= 0 || height <= 0 {
		return ""
	}
	offset := m.help.scrollOffset
	if offset < 0 {
		offset = 0
	}
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
		rendered = append(rendered, ViewportLineStyle.Width(width).Render(line))
	}
	out := strings.Join(rendered, "\n")
	m.help.renderCacheWidth = width
	m.help.renderCacheHeight = height
	m.help.renderCacheOffset = offset
	m.help.renderCacheTheme = m.theme.Name
	m.help.renderCacheText = out
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
