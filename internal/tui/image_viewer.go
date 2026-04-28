package tui

import (
	"fmt"
	"image"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type imageViewerState struct {
	Open            bool
	Part            BlockImagePart
	BlockID         int
	Index           int
	Total           int
	Backend         ImageBackend
	FitWidth        int
	FitHeight       int
	RenderGen       int
	ImageID         int
	PlacementID     int
	AnchorRow       int
	AnchorCol       int
	PixelOffsetX    int
	PixelOffsetY    int
	PhysicalValid   bool
	NeedsRetransmit bool
	TitleLabel      string
}

const imageViewerUsesPhysicalPlacement = true

func (m *Model) imageViewerParts(blockID int) ([]BlockImagePart, int, bool) {
	if m.viewport == nil {
		return nil, -1, false
	}
	if blockID >= 0 {
		for _, block := range m.viewport.visibleBlocks() {
			if block == nil || block.ID != blockID {
				continue
			}
			block = m.viewport.materialize(block)
			if block.Type != BlockUser || len(block.ImageParts) == 0 {
				return nil, -1, false
			}
			_ = block.Render(m.viewport.width, "")
			parts := make([]BlockImagePart, 0, len(block.ImageParts))
			for idx, part := range block.ImageParts {
				part.Index = idx
				parts = append(parts, part)
			}
			return parts, block.ID, true
		}
	}
	if m.focusedBlockID >= 0 {
		for _, block := range m.viewport.visibleBlocks() {
			if block == nil || block.ID != m.focusedBlockID {
				continue
			}
			block = m.viewport.materialize(block)
			if block.Type != BlockUser || len(block.ImageParts) == 0 {
				return nil, -1, false
			}
			_ = block.Render(m.viewport.width, "")
			parts := make([]BlockImagePart, 0, len(block.ImageParts))
			for idx, part := range block.ImageParts {
				part.Index = idx
				parts = append(parts, part)
			}
			return parts, block.ID, true
		}
	}
	return nil, -1, false
}

func (m *Model) openImageViewer(blockID, imageIndex int) {
	caps := m.imageCaps
	if caps.Backend == ImageBackendNone || !caps.SupportsFullscreen {
		return
	}
	parts, resolvedBlockID, ok := m.imageViewerParts(blockID)
	if !ok || len(parts) == 0 {
		return
	}
	if imageIndex < 0 || imageIndex >= len(parts) {
		imageIndex = 0
	}
	part := parts[imageIndex]
	label := strings.TrimSpace(part.FileName)
	if label == "" {
		label = "image"
	}
	m.clearChordState()
	m.clearActiveSearch()
	m.imageViewer = imageViewerState{
		Open:            true,
		Part:            part,
		BlockID:         resolvedBlockID,
		Index:           imageIndex,
		Total:           len(parts),
		Backend:         caps.Backend,
		FitWidth:        0,
		FitHeight:       0,
		RenderGen:       m.imageViewer.RenderGen + 1,
		ImageID:         0,
		PlacementID:     0,
		AnchorRow:       0,
		AnchorCol:       0,
		PixelOffsetX:    0,
		PixelOffsetY:    0,
		PhysicalValid:   false,
		NeedsRetransmit: true,
		TitleLabel:      label,
	}
	m.mode = ModeImageViewer
	m.recalcViewportSize()
}

func (m *Model) closeImageViewer() tea.Cmd {
	if !m.imageViewer.Open {
		return nil
	}
	var cmd tea.Cmd
	if m.imageCaps.Backend == ImageBackendKitty && m.imageViewer.ImageID > 0 && imageViewerUsesPhysicalPlacement {
		// Viewer close is different from image draw/update: the delete command must
		// not rely on deferred flushing, otherwise it can arrive after the normal
		// frame has already been rendered and remain pending until some unrelated
		// later redraw (for example a tab switch / resize). Send it directly.
		cmd = tea.Raw(kittyDeleteSequenceForPlacement(m.imageViewer.ImageID, m.imageViewer.PlacementID))
	}
	m.imageViewer = imageViewerState{}
	m.mode = ModeNormal
	m.recalcViewportSize()
	return cmd
}

func (m *Model) imageViewerPhysicalPlacement() (placementID, row, col, pxOffsetX, pxOffsetY int, ok bool) {
	if m.imageCaps.Backend != ImageBackendKitty || !imageViewerUsesPhysicalPlacement {
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
	placementID = m.imageViewer.RenderGen
	if placementID <= 0 {
		placementID = 1
	}
	return placementID, row, col, 0, 0, true
}

func (m *Model) imageViewerContentRect() (cols, rows int) {
	layout := m.ensureLayoutForHitTest()
	cols = layout.main.Dx()
	rows = layout.main.Dy()
	if cols <= 0 {
		cols = m.width
	}
	if rows <= 0 {
		rows = m.height
	}
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
	cols, rows = m.imageViewerContentRect()
	part := m.imageViewer.Part
	if cols <= 0 || rows <= 0 {
		return 1, 1, fmt.Errorf("viewer has no available space")
	}
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		return 0, 0, err
	}
	cfg, _, err := entry.decodeConfig(part)
	if err != nil {
		return 0, 0, err
	}
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

	metrics := m.kittyMetrics
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
	if !m.imageViewer.Open || delta == 0 || m.imageViewer.Total <= 1 {
		return nil
	}
	parts, blockID, ok := m.imageViewerParts(m.imageViewer.BlockID)
	if !ok || len(parts) == 0 {
		return nil
	}
	next := m.imageViewer.Index + delta
	if next < 0 {
		next = len(parts) - 1
	} else if next >= len(parts) {
		next = 0
	}
	oldImageID := m.imageViewer.ImageID
	oldPlacementID := m.imageViewer.PlacementID
	m.openImageViewer(blockID, next)
	if oldImageID > 0 && m.imageCaps.Backend == ImageBackendKitty && imageViewerUsesPhysicalPlacement {
		deleteCmd := tea.Raw(kittyDeleteSequenceForPlacement(oldImageID, oldPlacementID))
		return tea.Sequence(deleteCmd, m.imageProtocolCmd())
	}
	return m.imageProtocolCmd()
}

func (m *Model) handleImageViewerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "left", "h":
		return m.stepImageViewer(-1)
	case "right", "l":
		return m.stepImageViewer(1)
	case "esc", "q", "enter", "o", " ", "space":
		closeCmd := m.closeImageViewer()
		if closeCmd != nil {
			return closeCmd
		}
		return tea.ClearScreen
	default:
		return nil
	}
}

func (m *Model) imageViewerOverlayRect() (image.Rectangle, string) {
	dialog := m.renderImageViewerOverlay()
	if dialog == "" {
		return image.Rectangle{}, ""
	}
	layout := m.ensureLayoutForHitTest()
	return centeredRect(layout.area, dialog), dialog
}

func (m *Model) imageViewerTitle() string {
	left := "Image Viewer"
	if m.imageViewer.Total > 1 {
		left = fmt.Sprintf("Image Viewer (%d/%d)", m.imageViewer.Index+1, m.imageViewer.Total)
	}
	label := strings.TrimSpace(m.imageViewer.TitleLabel)
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
	gap := contentWidth - leftW - badgeW
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + badge
}

func (m *Model) renderImageViewerOverlay() string {
	if !m.imageViewer.Open {
		return ""
	}
	caps := m.imageCaps
	fitCols, fitRows, err := m.imageViewerFitSize()
	if err != nil {
		return DirectoryBorderStyle.Width(max(24, min(m.width-4, 60))).Render(
			DialogTitleStyle.Render(m.imageViewerTitle()) + "\n\n" + ErrorStyle.Render(err.Error()),
		)
	}
	m.imageViewer.FitWidth = fitCols
	m.imageViewer.FitHeight = fitRows

	contentWidth := max(24, min(m.width-4, max(fitCols+2*imageViewerInnerPadX+2, 40))) -
		DirectoryBorderStyle.GetHorizontalBorderSize() - DirectoryBorderStyle.GetHorizontalPadding()
	var lines []string
	lines = append(lines, DialogTitleStyle.Render(m.imageViewerTitleLine(contentWidth)))
	for range imageViewerInnerPadY {
		lines = append(lines, "")
	}

	label := strings.TrimSpace(m.imageViewer.Part.FileName)
	if label == "" {
		label = "image"
	}
	placeholderLine := strings.Repeat(" ", imageViewerInnerPadX) + lipgloss.NewStyle().
		Background(lipgloss.Color(currentTheme.UserCardBg)).
		Render(strings.Repeat(" ", fitCols)) + strings.Repeat(" ", imageViewerInnerPadX)

	if caps.Backend == ImageBackendKitty {
		if imageViewerUsesPhysicalPlacement {
			for range fitRows {
				lines = append(lines, placeholderLine)
			}
		} else {
			imageID, err := kittyImageIDForVariant(m.imageViewer.Part, fmt.Sprintf("viewer:%d:%d", fitCols, fitRows))
			if err == nil {
				m.imageViewer.ImageID = imageID
				lines = append(lines, kittyViewerLines(imageID, fitCols, fitRows, currentTheme.UserCardBg)...)
			} else {
				lines = append(lines, renderImageFallback(m.imageViewer.Part, fitCols+imagePlaceholderMargin)...)
			}
		}
	} else {
		for range fitRows {
			lines = append(lines, placeholderLine)
		}
	}
	for range imageViewerInnerPadY {
		lines = append(lines, "")
	}
	if m.imageViewer.Total > 1 {
		lines = append(lines, DimStyle.Render(fmt.Sprintf("%d / %d", m.imageViewer.Index+1, m.imageViewer.Total)))
	}
	body := strings.Join(lines, "\n")
	width := max(24, min(m.width-4, max(fitCols+2*imageViewerInnerPadX+4, 40)))
	if width > m.width {
		width = m.width
	}
	return DirectoryBorderStyle.Width(width).Render(body)
}
