package tui

import (
	"fmt"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/overlaylist"
)

func renderUsageModels(items []overlaylist.TableItem, width int) []string {
	columns := append([]overlaylist.Column{
		{Title: "Model", Width: 0, Align: 0},
		{Title: "Calls", Width: 6, Align: 1},
		{Title: "Input", Width: 8, Align: 1},
		{Title: "Output", Width: 8, Align: 1},
		{Title: "Cost", Width: 8, Align: 1},
	}, usageSpeedColumns()...)
	modelWidth := width - 2*(len(columns)-1)
	for _, col := range columns {
		modelWidth -= col.Width
	}
	tableFits := modelWidth >= 16
	if tableFits {
		for _, item := range items {
			if ansi.StringWidth(item.Label) > modelWidth {
				tableFits = false
				break
			}
		}
	}
	var lines []string
	if tableFits || len(items) == 0 {
		lines = renderUsageTable(columns, items, width)
	} else {
		for i, item := range items {
			if i > 0 {
				lines = append(lines, "")
			}
			cells := item.Cells
			lines = append(lines,
				item.Label,
				fmt.Sprintf("Calls %s · Input %s · Output %s · Cost %s", cells[1], cells[2], cells[3], cells[4]),
				fmt.Sprintf("tok/s %s · TTFT %s · Samples %s", cells[5], cells[6], cells[7]),
			)
		}
	}
	return wrapDialogLines(lines, width)
}
