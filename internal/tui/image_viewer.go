package tui

import (
	"context"
	"fmt"
	"image"
	"math"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	tea "github.com/keakon/bubbletea/v2"
)

// Items are a stable snapshot for both transcript and composer previews.
type imageViewerState struct {
	Open                 bool
	Items                []BlockImagePart
	Index                int
	ReturnMode           Mode
	Cursor               int
	Owner                imageViewerOwner
	cancel               context.CancelFunc
	Loading              bool
	Error                string
	Prepared             *imageViewerPrepared
	ImageID, PlacementID int
	NeedsRetransmit      bool
}

func (v imageViewerState) currentPart() BlockImagePart {
	if v.Index < 0 || v.Index >= len(v.Items) {
		return BlockImagePart{}
	}
	return v.Items[v.Index]
}

func (m *Model) imageViewerParts(blockID int) ([]BlockImagePart, bool) {
	if m.viewport == nil {
		return nil, false
	}
	if blockID < 0 {
		blockID = m.focusedBlockID
	}
	for _, block := range m.viewport.visibleBlocks() {
		if block == nil || block.ID != blockID {
			continue
		}
		block = m.viewport.materialize(block)
		if !blockSupportsImagePreview(block) {
			return nil, false
		}
		parts := slices.Clone(block.ImageParts)
		for idx := range parts {
			parts[idx].Index = idx
		}
		return parts, true
	}
	return nil, false
}

func (m *Model) openImageViewer(blockID, imageIndex int) tea.Cmd {
	parts, ok := m.imageViewerParts(blockID)
	if !ok {
		return nil
	}
	return m.openImageViewerItems(parts, imageIndex, ModeNormal)
}

func (m *Model) imageViewerOwner() imageViewerOwner {
	return imageViewerOwner{AgentID: m.focusedAgentID, Session: m.sessionTranscriptEpoch, Composer: m.input.editBoundary}
}

func (m *Model) openImageViewerItems(parts []BlockImagePart, index int, returnMode Mode) tea.Cmd {
	if len(parts) == 0 {
		return nil
	}
	if m.imageCaps.Backend == ImageBackendNone || !m.imageCaps.SupportsFullscreen {
		return m.enqueueToast("Image preview is unavailable in this terminal", "info")
	}
	cleanup := m.dismissImageViewer()
	m.imageViewer = imageViewerState{
		Open: true, Items: slices.Clone(parts), Index: min(max(index, 0), len(parts)-1),
		ReturnMode: returnMode, Cursor: runeOffsetFromRowCol(m.input.Value(), m.input.Line(), m.input.Column()),
		Owner: m.imageViewerOwner(), NeedsRetransmit: true,
	}
	m.input.ClearSelection()
	m.inputMouseDown = false
	m.inputClickCount = 0
	m.inputImageClick = inputImageClickState{}
	m.clearMouseSelection()
	imeCmd := m.switchModeWithIME(ModeImageViewer)
	m.recalcViewportSize()
	return tea.Sequence(cleanup, tea.Batch(imeCmd, m.prepareImageViewer()))
}

// dismissImageViewer invalidates work without restoring a previous business mode.
// Mode transitions, focus/session switches and ordinary close share this cleanup.
func (m *Model) dismissImageViewer() tea.Cmd {
	if !m.imageViewer.Open {
		return nil
	}
	if m.imageViewer.cancel != nil {
		m.imageViewer.cancel()
	}
	var cleanup tea.Cmd
	if m.imageCaps.Backend == ImageBackendKitty && m.imageViewer.ImageID > 0 {
		cleanup = tea.Raw(kittyDeleteSequenceForPlacement(m.imageViewer.ImageID, m.imageViewer.PlacementID))
	} else if m.imageCaps.Backend == ImageBackendITerm2 {
		cleanup = tea.ClearScreen
	}
	m.imageViewerGeneration++
	m.imageViewer = imageViewerState{}
	return cleanup
}

func (m *Model) closeImageViewer() tea.Cmd {
	if !m.imageViewer.Open {
		return nil
	}
	viewer := m.imageViewer
	cleanup := m.dismissImageViewer()
	if m.mode != ModeImageViewer {
		return cleanup
	}
	target := ModeNormal
	if viewer.Owner == m.imageViewerOwner() {
		target = viewer.ReturnMode
	}
	imeCmd := m.switchModeWithIME(target)
	var focus tea.Cmd
	if target == ModeInsert {
		m.input.ClearSelection()
		m.input.setCursorRuneOffset(min(viewer.Cursor, len([]rune(m.input.Value()))))
		focus = m.input.Focus()
	}
	m.recalcViewportSize()
	// Preserve immediate Kitty deletion, then restore the visible transcript.
	restore := tea.Batch(imeCmd, focus, m.imageProtocolCmd())
	if restore == nil {
		return cleanup
	}
	return tea.Sequence(cleanup, restore)
}

func (m *Model) imageViewerPhysicalPlacement() (placementID, row, col, pxOffsetX, pxOffsetY int, ok bool) {
	if m.imageCaps.Backend != ImageBackendKitty {
		return 0, 0, 0, 0, 0, false
	}
	metrics := m.kittyMetrics
	if !metrics.Valid || metrics.CellWidthPx <= 0 || metrics.CellHeightPx <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	rect, _ := m.imageViewerOverlayRect()
	fitCols, fitRows, err := m.imageViewerFitSize()
	if err != nil || fitCols <= 0 || fitRows <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	frameInnerLeft := rect.Min.X + 1 + DirectoryBorderStyle.GetPaddingLeft() + imageViewerInnerPadX
	availableCols := max(1, rect.Dx()-2-2*imageViewerInnerPadX-DirectoryBorderStyle.GetHorizontalPadding())
	if availableCols < fitCols {
		fitCols = availableCols
	}
	leftPadCols := max(0, (availableCols-fitCols)/2)
	// The overlay body renders as:
	// - top border
	// - title line
	// - imageViewerInnerPadY spacer lines
	// - image body
	//
	// Keep the physical placement anchored to the first image body row so the
	// real image fully covers the placeholder block without leaving a dark strip.
	row = rect.Min.Y + 2 + imageViewerInnerPadY
	col = frameInnerLeft + leftPadCols
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	placementID = int(m.imageViewerGeneration)
	if placementID <= 0 {
		placementID = 1
	}
	return placementID, row, col, 0, 0, true
}

func (m *Model) imageViewerContentRect() (cols, rows int) {
	layout := m.ensureLayout()
	cols = layout.main.Dx()
	rows = layout.main.Dy()
	if cols <= 0 {
		cols = m.width
	}
	if rows <= 0 {
		rows = m.height
	}
	cols = max(1, min(cols, m.width-4)-DirectoryBorderStyle.GetHorizontalFrameSize()-2*imageViewerInnerPadX)
	rows -= imageViewerMinReservedLines + 2*imageViewerInnerPadY
	if rows < 1 {
		rows = 1
	}
	if cols < 1 {
		cols = 1
	}
	return cols, rows
}

func (m *Model) imageViewerFitSize() (cols, rows int, err error) {
	v := m.imageViewer
	if v.Error != "" {
		return 0, 0, fmt.Errorf("%s", v.Error)
	}
	if v.Prepared == nil || v.Loading {
		return 0, 0, fmt.Errorf("image is loading")
	}
	return v.Prepared.Cols, v.Prepared.Rows, nil
}

func imageViewerFitDimensions(cfg image.Config, cols, rows int, metrics kittyTerminalMetrics) (int, int, error) {
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("image has invalid dimensions")
	}

	fitCols := cols
	fitRows := max(1, int((float64(cfg.Height)*float64(fitCols)*imageCellWidthOverHeight)/float64(cfg.Width)+0.5))
	if fitRows > rows {
		scale := float64(rows) / float64(fitRows)
		fitCols = max(1, int(float64(fitCols)*scale+0.5))
		fitRows = rows
	}

	if metrics.Valid && metrics.CellWidthPx > 0 && metrics.CellHeightPx > 0 {
		naturalCols := max(1, int(math.Ceil(float64(cfg.Width)/float64(metrics.CellWidthPx))))
		naturalRows := max(1, int(math.Ceil(float64(cfg.Height)/float64(metrics.CellHeightPx))))
		maxCols := max(1, int(float64(naturalCols)*imageViewerMaxScale+0.5))
		maxRows := max(1, int(float64(naturalRows)*imageViewerMaxScale+0.5))
		if fitCols > maxCols || fitRows > maxRows {
			scale := min(float64(maxCols)/float64(fitCols), float64(maxRows)/float64(fitRows))
			if scale < 1 {
				fitCols = max(1, int(float64(fitCols)*scale+0.5))
				fitRows = max(1, int(float64(fitRows)*scale+0.5))
			}
		}
	}
	if fitCols < 1 {
		fitCols = 1
	}
	if fitRows < 1 {
		fitRows = 1
	}
	return fitCols, fitRows, nil
}

func (m *Model) stepImageViewer(delta int) tea.Cmd {
	if !m.imageViewer.Open || delta == 0 || len(m.imageViewer.Items) <= 1 {
		return nil
	}
	m.imageViewer.Index = (m.imageViewer.Index + delta%len(m.imageViewer.Items) + len(m.imageViewer.Items)) % len(m.imageViewer.Items)
	return m.prepareImageViewer()
}

func (m *Model) handleImageViewerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "left", "h":
		return m.stepImageViewer(-1)
	case "right", "l":
		return m.stepImageViewer(1)
	case "r":
		return m.prepareImageViewer()
	case "esc", "q", "enter", "o", " ", "space":
		return m.closeImageViewer()
	default:
		return nil
	}
}

func (m *Model) imageViewerOverlayRect() (image.Rectangle, string) {
	dialog := m.renderImageViewerOverlay()
	if dialog == "" {
		return image.Rectangle{}, ""
	}
	layout := m.ensureLayout()
	return centeredRect(layout.area, dialog), dialog
}

func (m *Model) imageViewerTitle() string {
	left := "Image Viewer"
	if len(m.imageViewer.Items) > 1 {
		left = fmt.Sprintf("Image Viewer (%d/%d)", m.imageViewer.Index+1, len(m.imageViewer.Items))
	}
	label := strings.TrimSpace(m.imageViewer.currentPart().FileName)
	if label != "" {
		left += " · " + truncateOneLine(label, 28)
	}
	return left
}

func (m *Model) imageViewerTitleLine(contentWidth int) string {
	left := m.imageViewerTitle()
	badge := imageViewerCloseBadge
	if contentWidth <= 0 {
		return left + " " + badge
	}
	leftW := lipgloss.Width(left)
	badgeW := lipgloss.Width(badge)
	if leftW+1+badgeW > contentWidth {
		left = truncateOneLine(left, max(1, contentWidth-badgeW-1))
		leftW = lipgloss.Width(left)
	}
	gap := max(contentWidth-leftW-badgeW, 1)
	return left + strings.Repeat(" ", gap) + badge
}

func (m *Model) renderImageViewerOverlay() string {
	if !m.imageViewer.Open {
		return ""
	}
	if m.imageViewer.Loading || m.imageViewer.Prepared == nil && m.imageViewer.Error == "" {
		width := min(max(24, min(m.width-4, 60)), m.width)
		return renderDialogBox(width, []string{
			DialogTitleStyle.Render(m.imageViewerTitleLine(width - 4)), "",
			DimStyle.Render("Loading image…"),
		})
	}
	fitCols, fitRows, err := m.imageViewerFitSize()
	if err != nil {
		errWidth := min(max(24, min(m.width-4, 60)), m.width)
		return renderDialogBox(errWidth, []string{
			DialogTitleStyle.Render(m.imageViewerTitleLine(errWidth - 4)),
			"",
			ErrorStyle.Render(err.Error()),
			DimStyle.Render("Esc: close · r: retry"),
		})
	}

	contentWidth := max(24, min(m.width-4, max(fitCols+2*imageViewerInnerPadX+2, 40))) -
		DirectoryBorderStyle.GetHorizontalBorderSize() - DirectoryBorderStyle.GetHorizontalPadding()
	var lines []string
	lines = append(lines, DialogTitleStyle.Render(m.imageViewerTitleLine(contentWidth)))
	for range imageViewerInnerPadY {
		lines = append(lines, "")
	}

	placeholderLine := strings.Repeat(" ", imageViewerInnerPadX) + lipgloss.NewStyle().
		Background(lipgloss.Color(currentTheme.DialogBg)).
		Render(strings.Repeat(" ", fitCols)) + strings.Repeat(" ", imageViewerInnerPadX)

	for range fitRows {
		lines = append(lines, placeholderLine)
	}

	for range imageViewerInnerPadY {
		lines = append(lines, "")
	}
	if len(m.imageViewer.Items) > 1 {
		lines = append(lines, DimStyle.Render(fmt.Sprintf("%d / %d", m.imageViewer.Index+1, len(m.imageViewer.Items))))
	}
	body := strings.Join(lines, "\n")
	body = preserveDialogBackground(body)
	width := min(max(24, min(m.width-4, max(fitCols+2*imageViewerInnerPadX+4, 40))), m.width)
	return DirectoryBorderStyle.Width(width).Render(body)
}

// retireImageViewer is used by synchronous focus/session boundary helpers.
// Update flushes its cleanup before the next business command.
func (m *Model) retireImageViewer() {
	if !m.imageViewer.Open {
		return
	}
	cleanup := m.dismissImageViewer()
	var ime tea.Cmd
	if m.mode == ModeImageViewer {
		ime = m.switchModeWithIME(ModeNormal)
	}
	m.imageViewerCleanup = tea.Sequence(m.imageViewerCleanup, cleanup, ime)
}
