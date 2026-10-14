package tui

import (
	"fmt"
	"strings"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"
)

type deliveryCardHit struct {
	fileID                   string
	line, start, end, action int
}

func (b *Block) renderDeliveryCard(width int) []string {
	style := currentStyles.DirectoryBorderStyle.Width(max(1, width))
	inner := max(1, width-style.GetHorizontalFrameSize())
	rows := []string{"Delivered files · Enter or click for actions"}
	files := b.DeliveredFiles
	b.deliveryHits = nil
	if b.Collapsed && len(files) > 4 {
		files = files[:4]
	}
	top := style.GetBorderTopSize() + style.GetPaddingTop() + style.GetMarginTop()
	left := style.GetBorderLeftSize() + style.GetPaddingLeft() + style.GetMarginLeft()
	for _, f := range files {
		version := "Current file"
		if f.Locator.Artifact != nil {
			version = "Declared version"
		}
		rows = append(rows, fileLabel(f.DisplayName)+" · "+version+" · "+f.MIME)
		if f.Description != "" {
			rows = append(rows, "  "+fileLabel(f.Description))
		}
		if f.Locator.Workspace != nil {
			rows = append(rows, "  "+fileLabel(f.Locator.Workspace.Path))
		} else if f.Locator.Artifact != nil {
			rows = append(rows, "  "+fileLabel(f.Locator.Artifact.RelPath))
		}
		actions, hits := fileActionRows(max(1, inner-2), -1)
		base := len(rows)
		for action, hit := range hits {
			b.deliveryHits = append(b.deliveryHits, deliveryCardHit{fileID: f.ID, line: top + base + hit[0], start: left + 2 + hit[1], end: left + 2 + hit[2], action: action})
		}
		for _, row := range actions {
			rows = append(rows, "  "+row)
		}
	}
	if len(files) < len(b.DeliveredFiles) {
		rows = append(rows, fmt.Sprintf("… %d more files (Enter to show all)", len(b.DeliveredFiles)-len(files)))
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], inner, "…")
	}
	return strings.Split(style.Render(strings.Join(rows, "\n")), "\n")
}
func (m *Model) activateDeliveryCard(block *Block, line, column int) tea.Cmd {
	open := m.openDeliveryFiles(block.DeliveredFiles)
	for _, hit := range block.deliveryHits {
		if line == hit.line && column >= hit.start && column < hit.end {
			for i, f := range m.fileViewer.files {
				if f.ID == hit.fileID {
					m.fileViewer.selected = i
					m.fileViewer.action = hit.action
					return tea.Batch(open, m.activateFileAction(hit.action))
				}
			}
		}
	}
	return open
}
