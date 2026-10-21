package tui

import (
	"fmt"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
)

type deliveryCardHit struct {
	fileID           string
	line, start, end int
}

// deliveryCardTitle names the card by what it did rather than by the tool that
// declared it: the model calls present to hand files to the reader, so the
// header states the outcome.
func deliveryCardTitle(b *Block) string {
	return toolCardTitle("DELIVERED", b.displayLabelID())
}

// deliveryCardBodyOrigin returns where the card body starts in viewport
// coordinates: the row and column of its first cell. Rows account for the label
// badge and blank separator renderPrewrappedToolCard writes ahead of every tool
// card body plus the frame's top margin and padding; columns add the
// conversation rail, left margin and left padding. Deriving both from the same
// style the frame uses keeps file rows clickable when a theme changes the card
// geometry.
func deliveryCardBodyOrigin(style lipgloss.Style) (row, col int) {
	rail := 0
	if cardRailPrefix("tool", false) != "" {
		rail = cardRailWidth
	}
	return style.GetMarginTop() + style.GetPaddingTop() + toolCardLabelLeadRows,
		rail + style.GetMarginLeft() + style.GetPaddingLeft()
}

// toolCardLabelLeadRows counts the label badge plus the blank separator
// renderPrewrappedToolCard inserts between a tool card's title and its body.
const toolCardLabelLeadRows = 2

// deliveryDisplayPath keeps the card, the file list and the copy action speaking
// the same path dialect. The card body and the overlay both relativize against
// the working directory; only Copy path and the external-open actions hand out
// the absolute path, because those need one that resolves outside the terminal.
func deliveryDisplayPath(f message.DeliveredFile, workingDir string) string {
	return displayToolPath(deliveryPath(f, ""), workingDir)
}

// renderDeliveryCard draws the delivered files through the standard tool card
// wrapper — the same surface, label badge, conversation rail and focus
// feedback every other tool card uses.
//
// It used to render through DirectoryBorderStyle, the overlay/dialog border, so
// the card read as a floating dialog rather than a message, carried no label or
// block number, and never consulted b.Focused: a reader could not tell that
// Enter applied to this card.
func (b *Block) renderDeliveryCard(width int) []string {
	metrics := newToolCardMetrics(width)
	style := metrics.blockStyle
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	// Hits are rebuilt with the card: renderPrewrappedToolCard may fold, wrap or
	// truncate any row, so a stale hit set would point at the wrong file.
	b.deliveryHits = nil
	originRow, originCol := deliveryCardBodyOrigin(style)

	root, paths := deliveryDisplayPaths(b.DeliveredFiles, b.displayWorkingDir)
	summary := fmt.Sprintf("%d file(s)", len(b.DeliveredFiles))
	if label := deliveryRootLabel(root); label != "" {
		summary += " · " + fileLabel(label) + "/"
	}
	body := []string{toolFieldStandalone(currentStyles.DimStyle, summary)}
	pathWidth := max(1, contentWidth-len(toolFieldLead)-ansi.StringWidth(toolFieldConnector))
	for i, f := range b.DeliveredFiles {
		b.deliveryHits = append(b.deliveryHits, deliveryCardHit{
			fileID: f.ID,
			line:   originRow + len(body),
			start:  originCol,
			end:    originCol + contentWidth,
		})
		path := paths[i]
		if path == "" {
			path = f.DisplayName
		}
		body = append(body, toolFieldLead+currentStyles.ToolFieldConnectorStyle.Render(toolFieldConnector)+deliveryCardPathLine(path, f.MIME, pathWidth))
		if f.Description != "" {
			appendStyledWrappedBody(&body, currentStyles.DimStyle, toolFieldBodyIndent, f.Description, contentWidth)
		}
	}
	body = append(body, "", toolFieldStandalone(currentStyles.DimStyle, "Enter or click a file to open"))

	return b.renderToolCardWithIgnoredArgs(style, cardWidth, deliveryCardTitle(b), body, metrics.toolCardBg, cardRailPrefix("tool", b.Focused))
}

// rendersDeliveryCard reports whether this block draws the delivery card
// surface. The renderer and the click handler share it so the two cannot drift:
// a click is a file action only when the card the reader sees carries file rows.
func (b *Block) rendersDeliveryCard() bool {
	if b == nil {
		return false
	}
	return b.Type == BlockToolCall && len(b.DeliveredFiles) > 0 && b.ResultDone && !b.IsError
}

// activateDeliveryCard opens the preview of the file row under the click and
// reports whether the click hit one. A miss is not handled here: the card body
// is prose about the files, so it falls through to the caller's normal click
// handling and keeps text selection. Enter remains the whole-card action.
func (m *Model) activateDeliveryCard(block *Block, line, column int) (tea.Cmd, bool) {
	for _, hit := range block.deliveryHits {
		if line != hit.line || column < hit.start || column >= hit.end {
			continue
		}
		open := m.openDeliveryFileList(block.DeliveredFiles)
		for i, f := range m.fileViewer.files {
			if f.ID == hit.fileID {
				m.fileViewer.selected = i
				return tea.Batch(open, m.activateFileAction(fileActionPreview)), true
			}
		}
		return open, true
	}
	return nil, false
}
