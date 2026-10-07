package tui

import (
	"fmt"
	"image"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m *Model) renderMemoryPanel() string {
	p := &m.memoryPanel
	cfg := m.memoryPanelOverlayConfig()
	area := image.Rect(0, 0, m.width, m.height)
	var body string
	if p.detail {
		m.contentViewer = p.viewer
		defer func() { p.viewer = m.contentViewer }()
		lines := m.memoryDetailLines()
		height := overlayContentHeight(cfg, area)
		maxScroll := max(0, len(lines)-height)
		m.contentViewer.scrollOffset = min(max(0, m.contentViewer.scrollOffset), maxScroll)
		body = strings.Join(lines[m.contentViewer.scrollOffset:min(len(lines), m.contentViewer.scrollOffset+height)], "\n")
	} else {
		tabs := []string{"Project memories", "Session applied", "Suggestions"}
		for i := range tabs {
			if p.tab == i {
				tabs[i] = "[" + tabs[i] + "]"
			}
		}
		body = strings.Join(tabs, "  ") + "\n"
		if m.width < 60 {
			body = tabs[p.tab] + " (Tab views)\n"
		}
		if p.view != nil {
			state := "off"
			if p.view.Enabled {
				state = "on"
			}
			body += "Auto extraction: " + state
			if p.view.Pending {
				body += " · disk updates pending"
			}
			body += "\n"
		}
		if p.instructionMode {
			count := 0
			if base, err := m.memorySelectedSnapshot(p.instructionAll); err == nil {
				count = len(base.Items)
			}
			body += fmt.Sprintf("Organize %d records (uses model)\n", count)
			body += "Request: " + ansi.TruncateLeft(p.instruction, max(1, dialogContentWidth(cfg.MaxWidth)-10), "…") + "_\n"
		} else {
			body += "filter: " + ansi.TruncateLeft(p.query, max(1, dialogContentWidth(cfg.MaxWidth)-10), "…")
			if p.inputFocused {
				body += "_"
			}
			body += "\n"
		}
		body += "\n"
		if p.list != nil {
			headerRows := len(tuiHardwrap(body, max(1, dialogContentWidth(cfg.MaxWidth)))) - 1
			p.list.SetMaxVisible(max(1, overlayContentHeight(cfg, area)-headerRows))
			if p.list.Len() > 0 {
				body += p.list.Render(max(1, dialogContentWidth(cfg.MaxWidth)))
			} else if p.query != "" {
				body += "No matching memories."
			} else {
				body += "No entries in this view."
			}
		}
	}
	box, _ := RenderOverlay(cfg, body, area)
	return box
}

func (m *Model) memoryPanelOverlayConfig() OverlayConfig {
	p := &m.memoryPanel
	cfg := OverlayConfig{Title: "Memory", MaxWidth: max(1, m.width-2), MinContentHeight: 5, Hint: "/ search  j/k move  Enter read  yy copy  p path  Tab views  Esc close", CompactHint: "/ search  Enter read  yy copy  Esc close"}
	if p.detail {
		cfg.Title = p.viewer.title
		cfg.Hint = "j/k scroll  g/G jump  yy copy  Esc back"
		if p.preview != nil && !p.preview.Empty() {
			cfg.Hint = "a apply  Esc cancel  yy copy  j/k scroll  g/G jump"
			cfg.CompactHint = "a apply  Esc cancel  yy copy"
		} else if p.preview == nil {
			cfg.Hint += "  p copy path"
			cfg.CompactHint = "j/k scroll  yy copy  Esc back"
		}
	} else if p.tab == 0 {
		cfg.Hint += "\nSpace select  d remove  o selected / O all organize  u undo  r refresh"
	}
	if p.loading {
		cfg.Footer = "Working…"
	}
	if p.err != "" {
		cfg.Footer = sanitizeToolDisplayText(p.err)
	}
	return cfg
}

func (m *Model) memoryDetailLines() []string {
	width := max(1, dialogContentWidth(max(1, m.width-2)))
	v := &m.contentViewer
	if v.cachedWidth != width || v.cachedLines == nil {
		v.cachedLines = renderDialogMarkdownContent(v.content, width)
		v.cachedWidth = width
	}
	return v.cachedLines
}
