package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

func (m *Model) refreshKittyTerminalMetrics() {
	m.kittyMetrics = readKittyTerminalMetrics(m.imageCaps.Backend)
}

func readKittyTerminalMetrics(backend ImageBackend) kittyTerminalMetrics {
	if backend != ImageBackendKitty {
		return kittyTerminalMetrics{}
	}
	metrics, ok := readKittyTerminalPixelMetrics()
	if !ok {
		return kittyTerminalMetrics{}
	}
	return metrics
}

func (m *Model) kittyImageTransmitted(imageID int) bool {
	if m.kittyImageCache == nil || imageID <= 0 {
		return false
	}
	_, ok := m.kittyImageCache[imageID]
	return ok
}

func (m *Model) markKittyImageTransmitted(imageID int) {
	if imageID <= 0 {
		return
	}
	if m.kittyImageCache == nil {
		m.kittyImageCache = make(map[int]time.Time)
	}
	m.kittyImageCache[imageID] = time.Time{}
}

func (m *Model) kittyPlacementSent(imageID int) bool {
	if m.kittyPlacementCache == nil || imageID <= 0 {
		return false
	}
	_, ok := m.kittyPlacementCache[imageID]
	return ok
}

func (m *Model) markKittyPlacementSent(imageID int) {
	if imageID <= 0 {
		return
	}
	if m.kittyPlacementCache == nil {
		m.kittyPlacementCache = make(map[int]struct{})
	}
	m.kittyPlacementCache[imageID] = struct{}{}
}

func (m *Model) resetKittyPlacements() {
	if len(m.kittyPlacementCache) == 0 {
		return
	}
	clear(m.kittyPlacementCache)
}

func (m *Model) refreshInlineImagesIfViewportMoved(prevOffset int, extra ...tea.Cmd) tea.Cmd {
	if m.viewport != nil && m.viewport.offset != prevOffset && m.imageCaps.Backend != ImageBackendNone && m.imageCaps.SupportsInline {
		extra = append(extra, m.imageProtocolCmdWithReason("viewport-moved"))
	}
	return tea.Batch(extra...)
}

func (m *Model) imageProtocolCmd() tea.Cmd {
	return m.imageProtocolCmdWithReason("")
}

func (m *Model) imageProtocolCmdWithReason(reason string) tea.Cmd {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unspecified"
	}
	var cmds []tea.Cmd
	if m.mode == ModeImageViewer && m.imageViewer.Open {
		m.cancelInlineImages()
		m.lastImageProtocolAt = time.Now()
		m.lastImageProtocolReason = reason
		m.lastImageProtocolSummary = fmt.Sprintf("mode=image-viewer open=%t inline_visible=%t", m.imageViewer.Open, m.viewport != nil && m.viewport.HasVisibleInlineImage())
		m.recordTUIDiagnostic("image-protocol", "reason=%s mode=image-viewer open=%t", reason, m.imageViewer.Open)
		if cmd := m.imageViewerProtocolCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	}
	visibleInline := m.viewport != nil && m.viewport.HasVisibleInlineImage()
	if m.viewport == nil || !m.imageCaps.SupportsInline || !visibleInline || !m.inlineImagesAllowed() {
		m.cancelInlineImages()
		m.lastImageProtocolAt = time.Now()
		m.lastImageProtocolReason = reason
		m.lastImageProtocolSummary = fmt.Sprintf("skipped viewport_nil=%t supports_inline=%t visible_inline=%t backend=%s", m.viewport == nil, m.imageCaps.SupportsInline, visibleInline, m.imageCaps.Backend.String())
		m.recordTUIDiagnostic("image-protocol-skip", "reason=%s viewport_nil=%t supports_inline=%t visible_inline=%t backend=%s", reason, m.viewport == nil, m.imageCaps.SupportsInline, visibleInline, m.imageCaps.Backend.String())
		return nil
	}
	return m.prepareInlineImages(reason)
}

func (m *Model) imageViewerProtocolCmd() tea.Cmd {
	v := &m.imageViewer
	if !v.Open {
		return nil
	}
	cols, rows := m.imageViewerContentRect()
	if v.Prepared != nil && (v.Prepared.AvailableCols != cols || v.Prepared.AvailableRows != rows || v.Prepared.Metrics != m.kittyMetrics) {
		return m.prepareImageViewer()
	}
	if v.Loading || v.Error != "" || v.Prepared == nil {
		return nil
	}
	seq := v.Prepared.Sequence
	switch m.imageCaps.Backend {
	case ImageBackendKitty:
		placementID, row, col, offsetX, offsetY, ok := m.imageViewerPhysicalPlacement()
		if !ok {
			return nil
		}
		v.ImageID = v.Prepared.ImageID
		v.PlacementID = placementID
		if seq == "" {
			if v.NeedsRetransmit || !m.kittyImageTransmitted(v.ImageID) {
				return m.prepareImageViewer()
			}
			seq = encodeKittyDisplayPlacement(v.ImageID, placementID, v.Prepared.Cols, v.Prepared.Rows, -1, offsetX, offsetY)
		}
		v.NeedsRetransmit = false
		m.markKittyImageTransmitted(v.ImageID)
		v.Prepared.Sequence = ""
		v.lastProtocolAt = time.Now()
		return tea.Raw(deferredCursorSequence(row+1, col+1, seq))
	case ImageBackendITerm2:
		if seq == "" {
			return m.prepareImageViewer()
		}
		v.lastProtocolAt = time.Now()
		rect, _ := m.imageViewerOverlayRect()
		row := max(0, rect.Min.Y+2+imageViewerInnerPadY)
		col := max(0, rect.Min.X+1+DirectoryBorderStyle.GetPaddingLeft()+imageViewerInnerPadX)
		// The large payload was encoded in the prepare command. Only these small
		// cursor wrappers depend on the current overlay position.
		start := encodeDeferredTerminalSequence(fmt.Sprintf("\x1b7\x1b[%d;%dH", row+1, col+1))
		end := encodeDeferredTerminalSequence("\x1b8")
		return tea.Raw(start + seq + end)
	default:
		return nil
	}
}
