package tui

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"
)

func (m *Model) renderErrorPanelDialog() string {
	innerWidth := m.errorPanelInnerWidth()
	lines := m.errorPanelLines(innerWidth)
	visible := min(m.errorPanelVisibleLines(), len(lines))
	start := m.errorPanel.scrollOffset
	if start > len(lines)-visible {
		start = len(lines) - visible
	}
	if start < 0 {
		start = 0
	}
	if m.errorPanel.dialogCacheText != "" &&
		m.errorPanel.dialogCacheW == m.width &&
		m.errorPanel.dialogCacheH == m.height &&
		m.errorPanel.dialogCacheScroll == start &&
		m.errorPanel.dialogCacheVer == m.errorPanel.renderVersion &&
		m.errorPanel.dialogCacheTheme == m.theme.Name {
		return m.errorPanel.dialogCacheText
	}

	content := strings.Join(lines[start:start+visible], "\n")
	scroll := ""
	if m.errorPanelMaxScroll() > 0 {
		scroll = fmt.Sprintf("  %d/%d", start+visible, len(lines))
	}
	dialog, _ := RenderOverlay(OverlayConfig{
		Title:    "Error Panel",
		Hint:     "j/k scroll  g/G jump  ctrl+f/b page  esc close" + scroll,
		MinWidth: 60,
		MaxWidth: m.errorPanelMaxWidth(),
	}, content, lipgloss.Height(content), image.Rect(0, 0, m.width, m.height))

	m.errorPanel.dialogCacheW = m.width
	m.errorPanel.dialogCacheH = m.height
	m.errorPanel.dialogCacheScroll = start
	m.errorPanel.dialogCacheVer = m.errorPanel.renderVersion
	m.errorPanel.dialogCacheTheme = m.theme.Name
	m.errorPanel.dialogCacheText = dialog
	return dialog
}
