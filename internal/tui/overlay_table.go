package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

type OverlayTable struct {
	list          *OverlayList
	columns       []TableColumn
	items         []OverlayTableItem
	showSelection bool // when false, no cursor row / arrow gutter (e.g. usage stats tables)

	renderCache widthKeyedRenderCache
}

type TableColumn struct {
	Title string
	Width int // 0 means auto-expand
	Align int // 0=left, 1=right, 2=center
}

type OverlayTableItem struct {
	OverlayListItem
	Cells []string
}

func NewOverlayTable(columns []TableColumn, items []OverlayTableItem, maxVisible int) *OverlayTable {
	listItems := make([]OverlayListItem, len(items))
	for i, item := range items {
		listItems[i] = item.OverlayListItem
	}
	return &OverlayTable{
		list:          NewOverlayList(listItems, maxVisible),
		columns:       append([]TableColumn(nil), columns...),
		items:         append([]OverlayTableItem(nil), items...),
		showSelection: true,
	}
}

func (t *OverlayTable) invalidateRenderCache() {
	t.renderCache.invalidate()
}

// SetShowSelection controls whether the table renders a cursor highlight and ▸ gutter.
// Model pickers should leave this on; read-only stats tables should turn it off.
func (t *OverlayTable) SetShowSelection(on bool) {
	t.showSelection = on
	t.invalidateRenderCache()
}

func (t *OverlayTable) statusGutterWidth() int {
	if t.showSelection {
		return 3
	}
	return 0
}

func (t *OverlayTable) SetItems(items []OverlayTableItem) {
	t.items = append([]OverlayTableItem(nil), items...)
	listItems := make([]OverlayListItem, len(items))
	for i, item := range items {
		listItems[i] = item.OverlayListItem
	}
	t.list.SetItems(listItems)
	t.invalidateRenderCache()
}

func (t *OverlayTable) SetMaxVisible(maxVisible int) {
	if t.list != nil && t.list.maxVisible == maxVisible {
		return
	}
	t.list.SetMaxVisible(maxVisible)
	t.invalidateRenderCache()
}

func (t *OverlayTable) CursorUp() {
	t.list.CursorUp()
	t.invalidateRenderCache()
}
func (t *OverlayTable) CursorDown() {
	t.list.CursorDown()
	t.invalidateRenderCache()
}
func (t *OverlayTable) CursorToTop() {
	t.list.CursorToTop()
	t.invalidateRenderCache()
}
func (t *OverlayTable) CursorToBottom() {
	t.list.CursorToBottom()
	t.invalidateRenderCache()
}
func (t *OverlayTable) CursorAt() int { return t.list.CursorAt() }
func (t *OverlayTable) HandleWheel(delta int) bool {
	if !t.list.HandleWheel(delta) {
		return false
	}
	t.invalidateRenderCache()
	return true
}
func (t *OverlayTable) HandleClick(row int) bool {
	if !t.list.HandleClick(row) {
		return false
	}
	t.invalidateRenderCache()
	return true
}

func (t *OverlayTable) RenderVersion() uint64 {
	if t == nil {
		return 0
	}
	return t.renderCache.version
}

func (t *OverlayTable) SelectedItem() (OverlayTableItem, bool) {
	idx := t.list.CursorAt()
	if idx < 0 || idx >= len(t.items) {
		return OverlayTableItem{}, false
	}
	return t.items[idx], true
}

func (t *OverlayTable) WindowRange() (start, end int) {
	return t.list.WindowRange()
}

func (t *OverlayTable) Render(width int) string {
	if width <= 0 {
		width = 1
	}
	if cached, ok := t.renderCache.lookup(width); ok {
		return cached
	}
	statusColWidth := t.statusGutterWidth()
	colWidths := t.columnWidths(width)
	contentWidth := width - statusColWidth
	if contentWidth < 1 {
		contentWidth = 1
	}
	headerCells := make([]string, len(t.columns))
	for i, col := range t.columns {
		headerCells[i] = formatTableCell(col.Title, colWidths[i], col.Align)
	}
	headerLine := strings.Join(headerCells, "  ")
	headerLine = padTableLine(headerLine, contentWidth)
	headerLine = strings.Repeat(" ", statusColWidth) + headerLine
	lines := []string{DimStyle.Render(headerLine)}
	start, end := t.WindowRange()
	for i := start; i < end; i++ {
		item := t.items[i]
		if item.Header {
			label := item.Label
			if label == "" && len(item.Cells) > 0 {
				label = item.Cells[0]
			}
			label = ansi.Truncate(label, contentWidth, "…")
			line := padTableLine(label, contentWidth)
			line = strings.Repeat(" ", statusColWidth) + line
			lines = append(lines, DimStyle.Render(line))
			continue
		}
		cells := make([]string, len(t.columns))
		for c := range t.columns {
			val := ""
			if c < len(item.Cells) {
				val = item.Cells[c]
			}
			cells[c] = formatTableCell(val, colWidths[c], t.columns[c].Align)
		}
		line := strings.Join(cells, "  ")
		line = padTableLine(line, contentWidth)
		if t.showSelection && i == t.list.CursorAt() {
			lines = append(lines, SelectedStyle.Width(width).Render(" ▸ "+line))
			continue
		}
		line = strings.Repeat(" ", statusColWidth) + line
		if pad := width - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\n")
	return t.renderCache.store(width, out)
}

func (t *OverlayTable) columnWidths(width int) []int {
	widths := make([]int, len(t.columns))
	if len(widths) == 0 {
		return widths
	}
	gutter := t.statusGutterWidth()
	contentWidth := width - gutter - (len(t.columns)-1)*2
	if contentWidth < len(t.columns)*4 {
		contentWidth = len(t.columns) * 4
	}
	fixed := 0
	autoCount := 0
	for i, col := range t.columns {
		if col.Width > 0 {
			widths[i] = col.Width
			fixed += col.Width
		} else {
			autoCount++
		}
	}
	remaining := contentWidth - fixed
	if remaining < autoCount*6 {
		remaining = autoCount * 6
	}
	for i, col := range t.columns {
		if col.Width > 0 {
			continue
		}
		share := 0
		if autoCount > 0 {
			share = remaining / autoCount
		}
		if share < 6 {
			share = 6
		}
		widths[i] = share
		remaining -= share
		autoCount--
	}
	return widths
}

func formatTableCell(s string, width, align int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	actual := runewidth.StringWidth(s)
	if actual >= width {
		return s
	}
	pad := strings.Repeat(" ", width-actual)
	switch align {
	case 1:
		return pad + s
	case 2:
		left := (width - actual) / 2
		right := width - actual - left
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
	default:
		return s + pad
	}
}

func padTableLine(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	if pad := width - ansi.StringWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
