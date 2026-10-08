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
		if m.width < 60 {
			body = TabActiveStyle.Render(tabs[p.tab]) + "  " + hintLine(hint("Tab", "views")) + "\n"
		} else {
			body = renderTabRow(tabs, p.tab) + "\n"
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
			body += renderFilterLine(p.query, p.inputFocused, "", max(1, dialogContentWidth(cfg.MaxWidth))) + "\n"
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

// memoryPanelMaxWidth caps the memory panel so long record summaries keep a
// readable line length and the footer does not stretch across wide terminals.
const memoryPanelMaxWidth = 120

func (m *Model) memoryPanelWidth() int {
	return max(1, min(m.width-2, memoryPanelMaxWidth))
}

func (m *Model) memoryPanelOverlayConfig() OverlayConfig {
	p := &m.memoryPanel
	cfg := OverlayConfig{
		Title:            "Memory",
		MaxWidth:         m.memoryPanelWidth(),
		MinContentHeight: 5,
		Hint: hintLine(
			hint("/", "search"), hint("j/k", "move"), hint("Enter", "read"),
			hint("yy", "copy"), hint("p", "path"), hint("Tab", "views"), hint("Esc", "close"),
		),
		CompactHint: hintLine(hint("/", "search"), hint("Enter", "read"), hint("yy", "copy"), hint("Esc", "close")),
	}
	if p.detail {
		cfg.Title = p.viewer.title
		cfg.Hint = hintLine(hint("j/k", "scroll"), hint("g/G", "jump"), hint("yy", "copy"), hint("Esc", "back"))
		cfg.CompactHint = hintLine(hint("j/k", "scroll"), hint("yy", "copy"), hint("Esc", "back"))
		if p.preview != nil && !p.preview.Empty() {
			apply := primaryHint("a", "apply")
			apply.danger = len(p.preview.Remove) > 0
			cfg.Hint = hintLine(apply, hint("Esc", "cancel"), hint("yy", "copy"), hint("j/k", "scroll"), hint("g/G", "jump"))
			cfg.CompactHint = hintLine(apply, hint("Esc", "cancel"), hint("yy", "copy"))
		} else if p.preview == nil {
			cfg.Hint = appendHintChip(cfg.Hint, hint("p", "copy path"))
		}
	} else if p.tab == 0 {
		cfg.Hint += "\n" + hintLine(
			hint("Space", "select"), hint("d", "remove"), hint("o", "organize selected"),
			hint("O", "organize all"), hint("u", "undo"), hint("r", "refresh"),
		)
	}
	if p.loading {
		cfg.Footer = "Working…"
	}
	if p.err != "" {
		cfg.Footer = DialogDangerStyle.Render(truncateOneLine(sanitizeToolDisplayText(p.err), max(dialogContentWidth(cfg.MaxWidth), 1)))
	}
	if p.inputFocused {
		cfg.Hint = hintLine(hint("type", "filter"), hint("Enter", "keep"), hint("Esc", "clear"))
		cfg.CompactHint = hintLine(hint("Enter", "keep"), hint("Esc", "clear"))
	} else if p.instructionMode {
		cfg.Hint = hintLine(primaryHint("Enter", "organize"), hint("Esc", "cancel"))
		cfg.CompactHint = cfg.Hint
	} else if p.loading {
		cfg.Hint = hintLine(hint("Esc", "close"))
		cfg.CompactHint = cfg.Hint
	} else if !p.detail && (p.list == nil || p.list.Len() == 0) {
		cfg.Hint = hintLine(hint("/", "search"), hint("Tab", "views"), hint("r", "refresh"), hint("Esc", "close"))
		cfg.CompactHint = hintLine(hint("Tab", "views"), hint("Esc", "close"))
	}
	return cfg
}

func (m *Model) memoryDetailLines() []string {
	width := max(1, dialogContentWidth(m.memoryPanelWidth()))
	v := &m.contentViewer
	if v.cachedWidth != width || v.cachedLines == nil {
		v.cachedLines = renderDialogMarkdownContent(v.content, width)
		v.cachedWidth = width
	}
	return v.cachedLines
}
