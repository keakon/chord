package tui

import (
	"fmt"
	"image"
	"strings"
)

func (m *Model) renderErrorPanelDialog() string {
	innerWidth := m.errorPanelInnerWidth()
	lines := m.errorPanelLines(innerWidth)
	visible := min(m.errorPanelVisibleLines(), len(lines))
	start := max(min(m.errorPanel.scrollOffset, len(lines)-visible), 0)
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
	cfg := m.errorPanelOverlayConfig()
	cfg.Hint += scroll
	dialog, _ := RenderOverlay(cfg, content, image.Rect(0, 0, m.width, m.height))

	m.errorPanel.dialogCacheW = m.width
	m.errorPanel.dialogCacheH = m.height
	m.errorPanel.dialogCacheScroll = start
	m.errorPanel.dialogCacheVer = m.errorPanel.renderVersion
	m.errorPanel.dialogCacheTheme = m.theme.Name
	m.errorPanel.dialogCacheText = dialog
	return dialog
}

func (m *Model) errorPanelHint() string {
	if m.width < 60 {
		return "↑/↓ scroll  PgUp/PgDn page  Esc close"
	}
	return "j/k scroll  g/G jump  ctrl+f/b page  esc close"
}
