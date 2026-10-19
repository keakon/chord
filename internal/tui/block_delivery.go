package tui

import (
	"fmt"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/message"
)

type deliveryCardHit struct {
	fileID                   string
	line, start, end, action int
}

// deliveryActionLead is the body indent the action row carries. The hit
// columns below are derived from it rather than from a literal so the buttons
// stay clickable if the indent ever changes.
const deliveryActionLead = "  "

// deliveryCardTitle names the card by what it did rather than by the tool that
// declared it: the model calls present to hand files to the reader, so the
// header states the outcome.
func deliveryCardTitle(b *Block) string {
	return toolCardTitle("DELIVERED", b.displayLabelID())
}

// deliveryCardBodyOrigin returns where the card body starts in viewport
// coordinates: (row, column) of its first line and first byte.
//
// Rows: renderPrewrappedToolCard writes the label badge and a blank separator
// ahead of every tool card body, and the card frame adds its own margin and top
// padding above those. Columns: the rail sits outside the card surface, then the
// frame's left margin and left padding, then the body text. Deriving all of it
// from the same style the frame uses keeps the buttons clickable when a theme
// changes the card geometry — the previous version mixed a hardcoded column with
// a border-style offset that no longer applied, which is why the hit rows landed
// one line above the buttons they named.
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
// Enter applied to this card, and had no title telling them what the four
// bracketed buttons did.
func (b *Block) renderDeliveryCard(width int) []string {
	metrics := newToolCardMetrics(width)
	style := metrics.blockStyle
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	// Hits are rebuilt with the card: renderPrewrappedToolCard may fold, wrap or
	// truncate any row, so a stale hit set would point at the wrong button.
	b.deliveryHits = nil
	originRow, originCol := deliveryCardBodyOrigin(style)

	body := []string{toolSummaryLine(fmt.Sprintf("%d file(s) delivered", len(b.DeliveredFiles)))}
	for _, f := range b.DeliveredFiles {
		version := "Current file"
		if f.Locator.Artifact != nil {
			version = "Declared version"
		}
		body = append(body, toolFieldMarker(currentStyles.ToolResultExpandedStyle, fileLabel(f.DisplayName)+" · "+version+" · "+fileLabel(f.MIME)))
		if f.Description != "" {
			appendStyledWrappedBody(&body, currentStyles.DimStyle, toolFieldBodyIndent, f.Description, contentWidth)
		}
		if path := deliveryDisplayPath(f, b.displayWorkingDir); path != "" {
			appendStyledWrappedBody(&body, currentStyles.DimStyle, toolFieldBodyIndent, path, contentWidth)
		}
		actions, hits := wrapActionButtons(deliveryActionButtons(), max(1, contentWidth-len(deliveryActionLead)))
		base := len(body)
		for action, hit := range hits {
			b.deliveryHits = append(b.deliveryHits, deliveryCardHit{
				fileID: f.ID,
				line:   originRow + base + hit[0],
				start:  originCol + len(deliveryActionLead) + hit[1],
				end:    originCol + len(deliveryActionLead) + hit[2],
				action: action,
			})
		}
		for _, row := range actions {
			body = append(body, deliveryActionLead+row)
		}
	}
	body = append(body, "", toolFieldStandalone(currentStyles.DimStyle, "Enter or click a button to open, preview, or copy the path"))

	return b.renderToolCardWithIgnoredArgs(style, cardWidth, deliveryCardTitle(b), body, metrics.toolCardBg, cardRailPrefix("tool", b.Focused))
}

// rendersDeliveryCard reports whether this block draws the delivery card
// surface. The renderer and the click handler share it so the two cannot drift:
// a click is the delivery action only when the card the reader sees actually
// carries the delivery buttons.
func (b *Block) rendersDeliveryCard() bool {
	if b == nil {
		return false
	}
	return b.Type == BlockToolCall && len(b.DeliveredFiles) > 0 && b.ResultDone && !b.IsError
}

// activateDeliveryCard runs the action of the delivery button under the click
// and reports whether the click hit one. A miss is not handled here: the card
// body is prose about the files, and its own hint says "click a button", so it
// falls through to the caller's normal click handling and keeps text selection.
// Enter remains the whole-card action.
func (m *Model) activateDeliveryCard(block *Block, line, column int) (tea.Cmd, bool) {
	for _, hit := range block.deliveryHits {
		if line != hit.line || column < hit.start || column >= hit.end {
			continue
		}
		// A button press names its own action, so the view opens on the list
		// and the action runs against it. Auto-previewing here would load the
		// file twice: once for the viewer and once for the pressed action.
		open := m.openDeliveryFileList(block.DeliveredFiles)
		for i, f := range m.fileViewer.files {
			if f.ID == hit.fileID {
				m.fileViewer.selected = i
				return tea.Batch(open, m.activateFileAction(hit.action)), true
			}
		}
		return open, true
	}
	return nil, false
}
