package tui

import (
	"fmt"
	"image"
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/memory"
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
		contentWidth := max(1, dialogContentWidth(cfg.MaxWidth))
		const gapRows = 1
		headerLines := tuiHardwrap(m.memoryPanelListHeader(cfg), contentWidth)
		body = strings.Join(headerLines, "\n") + "\n\n"
		if p.list != nil {
			p.list.SetMaxVisible(max(1, overlayContentHeight(cfg, area)-(len(headerLines)+gapRows)))
			p.listBaseRow = layoutOverlay(cfg, area).contentBaseRow() + len(headerLines) + gapRows
			if p.list.Len() > 0 {
				body += p.list.Render(contentWidth)
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

// memoryPanelListHeader renders the rows above the list: view tabs, the
// auto-extraction state, and the filter or organize request line.
func (m *Model) memoryPanelListHeader(cfg OverlayConfig) string {
	p := &m.memoryPanel
	width := max(1, dialogContentWidth(cfg.MaxWidth))
	var b strings.Builder
	tabs := []string{"Project memories", "Session applied", "Suggestions"}
	if m.width < 60 {
		fmt.Fprintf(&b, "%s  %s\n", TabActiveStyle.Render(tabs[p.tab]), hintLine(hint("Tab", "views")))
	} else {
		fmt.Fprintf(&b, "%s\n", renderTabRow(tabs, p.tab))
	}
	if p.view != nil {
		state := "off"
		if p.view.Enabled {
			state = "on"
		}
		fmt.Fprintf(&b, "Auto extraction: %s", state)
		if p.view.Pending {
			b.WriteString(" · disk updates pending")
		}
		b.WriteString("\n")
	}
	if p.instructionMode {
		count := 0
		if base, err := m.memorySelectedSnapshot(p.instructionAll); err == nil {
			count = len(base.Items)
		}
		fmt.Fprintf(&b, "Organize %d records (uses model)\n", count)
		fmt.Fprintf(&b, "Request: %s_\n", ansi.TruncateLeft(p.instruction, max(1, width-10), "…"))
	} else {
		fmt.Fprintf(&b, "%s\n", renderFilterLine(p.query, p.inputFocused, "", width))
	}
	return strings.TrimSuffix(b.String(), "\n")
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
		Hint:             m.memoryPanelListHint(),
		CompactHint:      hintLine(hint("/", "search"), hint("Enter", "read"), hint("y", "copy"), hint("Esc", "close")),
	}
	if p.detail {
		cfg.Title = p.viewer.title
		cfg.Hint = hintLine(hint("j/k", "scroll"), hint("g/G", "jump"), hint("y", "copy"), hint("Esc", "back"))
		cfg.CompactHint = hintLine(hint("j/k", "scroll"), hint("y", "copy"), hint("Esc", "back"))
		if p.preview != nil && !p.preview.Empty() {
			apply := primaryHint("a", "apply")
			apply.danger = len(p.preview.Remove) > 0
			cfg.Hint = hintLine(apply, hint("Esc", "cancel"), hint("y", "copy"), hint("j/k", "scroll"), hint("g/G", "jump"))
			cfg.CompactHint = hintLine(apply, hint("Esc", "cancel"), hint("y", "copy"))
		} else if p.preview == nil && m.memoryPanelCurrentPath() != "" {
			cfg.Hint = appendHintChip(cfg.Hint, hint("p", "copy path"))
		}
	} else if p.tab == 0 && p.list != nil && p.list.Len() > 0 {
		cfg.Hint += "\n" + m.memoryPanelProjectHint()
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

// memoryPanelListHint advertises only the actions the current view and entry
// can run; keys that would do nothing stay out of the footer.
func (m *Model) memoryPanelListHint() string {
	p := &m.memoryPanel
	chips := []hintChip{hint("/", "search")}
	if p.list != nil && p.list.Len() > 0 {
		chips = append(chips, hint("j/k", "move"), hint("Enter", "read"), hint("y", "copy"))
		if m.memoryPanelCurrentPath() != "" {
			chips = append(chips, hint("p", "path"))
		}
	}
	chips = append(chips, hint("Tab", "views"), hint("Esc", "close"))
	return hintLine(chips...)
}

// memoryPanelProjectHint lists project-view actions that have a target: the
// record under the cursor or the current multi-selection.
func (m *Model) memoryPanelProjectHint() string {
	var chips []hintChip
	if m.memoryPanelCursorIsRecord() {
		chips = append(chips, hint("Space", "select"))
	}
	if m.memoryPanelCanModify() {
		chips = append(chips, hint("d", "remove"), hint("o", "organize selected"), hint("O", "organize all"))
	}
	if p := &m.memoryPanel; p.view != nil && p.view.Snapshot.CanUndo {
		chips = append(chips, hint("u", "undo"))
	}
	chips = append(chips, hint("r", "refresh"))
	return hintLine(chips...)
}

func (m *Model) memoryPanelCurrentPath() string {
	_, _, path := m.memoryCurrentContent()
	return path
}

// memoryPanelCursorIsRecord reports whether the cursor sits on a managed record
// rather than the read-only user-notes entry.
func (m *Model) memoryPanelCursorIsRecord() bool {
	p := &m.memoryPanel
	if p.tab != 0 || p.list == nil {
		return false
	}
	item, ok := p.list.SelectedItem()
	return ok && memory.ValidateRecordID(item.ID)
}

// memoryPanelCanModify reports whether the project view has a removal or
// organization target.
func (m *Model) memoryPanelCanModify() bool {
	if m.memoryPanel.tab != 0 {
		return false
	}
	_, err := m.memorySelectedSnapshot(false)
	return err == nil
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
