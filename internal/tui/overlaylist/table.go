package overlaylist

import (
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

type Table struct {
	list          *List
	columns       []Column
	items         []TableItem
	showSelection bool // when false, no cursor row / arrow gutter (e.g. usage stats tables)

	renderCache widthKeyedRenderCache
}

type Column struct {
	Title string
	Width int // 0 means auto-expand
	Align int // 0=left, 1=right, 2=center
}

type TableItem struct {
	Item
	Cells []string
}

func NewTable(columns []Column, items []TableItem, maxVisible int) *Table {
	listItems := make([]Item, len(items))
	for i, item := range items {
		listItems[i] = item.Item
	}
	return &Table{
		list:          New(listItems, maxVisible),
		columns:       append([]Column(nil), columns...),
		items:         append([]TableItem(nil), items...),
		showSelection: true,
	}
}

func (t *Table) invalidateRenderCache() {
	t.renderCache.invalidate()
}

// SetShowSelection controls whether the table renders a cursor highlight and ▸ gutter.
// Read-only stats tables turn it off.
func (t *Table) SetShowSelection(on bool) {
	t.showSelection = on
	t.invalidateRenderCache()
}

func (t *Table) statusGutterWidth() int {
	if t.showSelection {
		return 3
	}
	return 0
}

func (t *Table) WindowRange() (start, end int) {
	return t.list.WindowRange()
}

func (t *Table) Render(width int, styles *uitheme.Styles) string {
	if width <= 0 {
		width = 1
	}
	if cached, ok := t.renderCache.lookup(width); ok {
		return cached
	}
	statusColWidth := t.statusGutterWidth()
	colWidths := t.columnWidths(width)
	contentWidth := max(width-statusColWidth, 1)
	headerCells := make([]string, len(t.columns))
	for i, col := range t.columns {
		headerCells[i] = formatTableCell(col.Title, colWidths[i], col.Align)
	}
	headerLine := strings.Join(headerCells, "  ")
	headerLine = padTableLine(headerLine, contentWidth)
	headerLine = strings.Repeat(" ", statusColWidth) + headerLine
	lines := []string{styles.DimStyle.Render(headerLine)}
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
			lines = append(lines, styles.DimStyle.Render(line))
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
			lines = append(lines, styles.SelectedStyle.Width(width).Render(" ▸ "+line))
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

func (t *Table) columnWidths(width int) []int {
	widths := make([]int, len(t.columns))
	if len(widths) == 0 {
		return widths
	}
	gutter := t.statusGutterWidth()
	contentWidth := max(width-gutter-(len(t.columns)-1)*2, len(t.columns)*4)
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
	remaining := max(contentWidth-fixed, autoCount*6)
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
	actual := terminaltext.Width(s)
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
