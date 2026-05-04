package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type OverlayListItem struct {
	ID       string
	Label    string
	Selected bool
	Value    any
	Disabled bool
	Header   bool
}

func (i OverlayListItem) selectable() bool {
	return !i.Disabled && !i.Header
}

// widthKeyedRenderCache memoizes a width-keyed string render. Both
// OverlayList and OverlayTable embed it so they share invalidate / lookup /
// store semantics: a non-zero version doubles as a "dirty" signal for outer
// view caches that fingerprint the overlay between paints.
type widthKeyedRenderCache struct {
	version uint64
	width   int
	text    string
	valid   bool
}

func (c *widthKeyedRenderCache) invalidate() {
	c.version++
	c.valid = false
}

func (c *widthKeyedRenderCache) lookup(width int) (string, bool) {
	if c.valid && c.width == width {
		return c.text, true
	}
	return "", false
}

func (c *widthKeyedRenderCache) store(width int, text string) string {
	c.width = width
	c.text = text
	c.valid = true
	return text
}

type OverlayList struct {
	items      []OverlayListItem
	cursor     int
	offset     int
	maxVisible int

	renderCache widthKeyedRenderCache
}

func NewOverlayList(items []OverlayListItem, maxVisible int) *OverlayList {
	l := &OverlayList{maxVisible: maxVisible}
	l.SetItems(items)
	return l
}

func (l *OverlayList) invalidateRenderCache() {
	l.renderCache.invalidate()
}

func (l *OverlayList) SetItems(items []OverlayListItem) {
	l.items = append([]OverlayListItem(nil), items...)
	if len(l.items) == 0 {
		l.cursor = 0
		l.offset = 0
		l.invalidateRenderCache()
		return
	}
	if l.cursor >= len(l.items) {
		l.cursor = len(l.items) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	l.clampCursorToSelectable(1)
	l.ensureVisible()
	l.invalidateRenderCache()
}

func (l *OverlayList) Len() int { return len(l.items) }

func (l *OverlayList) SetMaxVisible(maxVisible int) {
	if l.maxVisible == maxVisible {
		return
	}
	l.maxVisible = maxVisible
	l.ensureVisible()
	l.invalidateRenderCache()
}

func (l *OverlayList) SetCursor(idx int) {
	if len(l.items) == 0 {
		l.cursor = 0
		l.offset = 0
		l.invalidateRenderCache()
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(l.items) {
		idx = len(l.items) - 1
	}
	l.cursor = idx
	l.clampCursorToSelectable(1)
	l.ensureVisible()
	l.invalidateRenderCache()
}

func (l *OverlayList) CursorUp() {
	if len(l.items) == 0 || l.cursor < 0 {
		return
	}
	l.moveCursor(-1)
}

func (l *OverlayList) CursorDown() {
	if len(l.items) == 0 || l.cursor < 0 {
		return
	}
	l.moveCursor(1)
}

func (l *OverlayList) CursorToTop() {
	l.SetCursor(0)
}

func (l *OverlayList) CursorToBottom() {
	l.SetCursor(len(l.items) - 1)
}

func (l *OverlayList) CursorAt() int {
	return l.cursor
}

func (l *OverlayList) SelectedItem() (OverlayListItem, bool) {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		return OverlayListItem{}, false
	}
	return l.items[l.cursor], true
}

func (l *OverlayList) WindowRange() (start, end int) {
	if len(l.items) == 0 {
		return 0, 0
	}
	if l.maxVisible <= 0 || len(l.items) <= l.maxVisible {
		return 0, len(l.items)
	}
	if l.offset < 0 {
		l.offset = 0
	}
	if l.offset > len(l.items)-l.maxVisible {
		l.offset = len(l.items) - l.maxVisible
	}
	return l.offset, l.offset + l.maxVisible
}

func (l *OverlayList) HandleWheel(delta int) bool {
	if delta == 0 || len(l.items) == 0 {
		return false
	}
	if delta > 0 {
		for i := 0; i < delta; i++ {
			l.CursorDown()
		}
	} else {
		for i := 0; i < -delta; i++ {
			l.CursorUp()
		}
	}
	return true
}

func (l *OverlayList) HandleClick(row int) bool {
	start, end := l.WindowRange()
	idx := start + row
	if row < 0 || idx < start || idx >= end {
		return false
	}
	if idx < 0 || idx >= len(l.items) || !l.items[idx].selectable() {
		return false
	}
	l.SetCursor(idx)
	return true
}

func (l *OverlayList) RenderVersion() uint64 {
	if l == nil {
		return 0
	}
	return l.renderCache.version
}

func (l *OverlayList) Render(width int) string {
	if width <= 0 {
		width = 1
	}
	if cached, ok := l.renderCache.lookup(width); ok {
		return cached
	}
	start, end := l.WindowRange()
	lines := make([]string, 0, end-start)
	contentWidth := width - 3
	if contentWidth < 1 {
		contentWidth = 1
	}
	for i := start; i < end; i++ {
		item := l.items[i]
		label := item.Label
		if item.Selected {
			label += " ✓"
		}
		if item.Header {
			label = ansi.Truncate(label, width, "…")
			line := DimStyle.Render(label)
			if pad := width - ansi.StringWidth(ansi.Strip(line)); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			lines = append(lines, line)
			continue
		}
		label = ansi.Truncate(label, contentWidth, "…")
		if i == l.cursor {
			lines = append(lines, SelectedStyle.Width(width).Render(" ▸ "+label))
			continue
		}
		line := "   " + label
		if pad := width - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\n")
	return l.renderCache.store(width, out)
}

func (l *OverlayList) ensureVisible() {
	if len(l.items) == 0 {
		l.cursor = 0
		l.offset = 0
		return
	}
	l.clampCursorToSelectable(1)
	if l.cursor < 0 {
		l.offset = 0
		return
	}
	if l.maxVisible <= 0 || len(l.items) <= l.maxVisible {
		l.offset = 0
		return
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.maxVisible {
		l.offset = l.cursor - l.maxVisible + 1
	}
	if l.offset < 0 {
		l.offset = 0
	}
	if l.offset > len(l.items)-l.maxVisible {
		l.offset = len(l.items) - l.maxVisible
	}
}

func (l *OverlayList) moveCursor(delta int) {
	if delta == 0 || len(l.items) == 0 {
		return
	}
	idx := l.cursor
	if idx < 0 {
		if delta > 0 {
			idx = -1
		} else {
			idx = len(l.items)
		}
	}
	for {
		idx += delta
		if idx < 0 || idx >= len(l.items) {
			return
		}
		if l.items[idx].selectable() {
			l.cursor = idx
			l.ensureVisible()
			l.invalidateRenderCache()
			return
		}
	}
}

func (l *OverlayList) clampCursorToSelectable(direction int) {
	if len(l.items) == 0 {
		l.cursor = 0
		return
	}
	if direction == 0 {
		direction = 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= len(l.items) {
		l.cursor = len(l.items) - 1
	}
	if l.items[l.cursor].selectable() {
		return
	}
	for i := l.cursor + direction; i >= 0 && i < len(l.items); i += direction {
		if l.items[i].selectable() {
			l.cursor = i
			return
		}
	}
	for i := l.cursor - direction; i >= 0 && i < len(l.items); i -= direction {
		if l.items[i].selectable() {
			l.cursor = i
			return
		}
	}
	l.cursor = -1
}
