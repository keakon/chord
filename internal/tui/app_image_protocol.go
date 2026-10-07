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
		m.kittyImageCache = make(map[int]struct{})
	}
	m.kittyImageCache[imageID] = struct{}{}
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
	if m.viewport == nil || !m.imageCaps.SupportsInline || !visibleInline {
		m.lastImageProtocolAt = time.Now()
		m.lastImageProtocolReason = reason
		m.lastImageProtocolSummary = fmt.Sprintf("skipped viewport_nil=%t supports_inline=%t visible_inline=%t backend=%s", m.viewport == nil, m.imageCaps.SupportsInline, visibleInline, m.imageCaps.Backend.String())
		m.recordTUIDiagnostic("image-protocol-skip", "reason=%s viewport_nil=%t supports_inline=%t visible_inline=%t backend=%s", reason, m.viewport == nil, m.imageCaps.SupportsInline, visibleInline, m.imageCaps.Backend.String())
		return nil
	}
	kittyVisibleParts := 0
	kittySeqBytes := 0
	if m.imageCaps.Backend == ImageBackendKitty {
		blocks := m.viewport.visibleBlocks()
		starts := m.viewport.blockStarts()
		windowStart := m.viewport.offset
		windowEnd := m.viewport.offset + m.viewport.height
		var seq strings.Builder
		for i, block := range blocks {
			if !blockSupportsImagePreview(block) {
				continue
			}
			block = m.viewport.materialize(block)
			blockStart := starts[i]
			for _, part := range block.ImageParts {
				if part.RenderRows <= 0 || part.RenderStartLine < 0 {
					continue
				}
				globalStart := blockStart + part.RenderStartLine
				globalEnd := blockStart + part.RenderEndLine
				if globalEnd < windowStart || globalStart >= windowEnd {
					continue
				}
				imgID, err := kittyRenderImageID(part, part.RenderCols, part.RenderRows)
				if err != nil {
					continue
				}
				inlineSeq, _, err := kittyInlineSequence(part, part.RenderCols, part.RenderRows, m.kittyImageTransmitted(imgID) && m.kittyPlacementSent(imgID))
				if err != nil {
					continue
				}
				kittyVisibleParts++
				seq.WriteString(inlineSeq)
				m.markKittyImageTransmitted(imgID)
				m.markKittyPlacementSent(imgID)
			}
		}
		kittySeqBytes = seq.Len()
		if seq.Len() > 0 {
			cmds = append(cmds, tea.Raw(seq.String()))
		}
	}
	if m.imageCaps.Backend == ImageBackendITerm2 {
		if cmd := m.iterm2InlineProtocolCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	m.lastImageProtocolAt = time.Now()
	m.lastImageProtocolReason = reason
	m.lastImageProtocolSummary = fmt.Sprintf("backend=%s visible_inline=%t kitty_visible_parts=%d kitty_seq_bytes=%d cmds=%d", m.imageCaps.Backend.String(), visibleInline, kittyVisibleParts, kittySeqBytes, len(cmds))
	m.recordTUIDiagnostic("image-protocol", "reason=%s backend=%s visible_inline=%t kitty_visible_parts=%d kitty_seq_bytes=%d cmds=%d", reason, m.imageCaps.Backend.String(), visibleInline, kittyVisibleParts, kittySeqBytes, len(cmds))
	return tea.Batch(cmds...)
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
		placementID, row, col, _, _, ok := m.imageViewerPhysicalPlacement()
		if !ok {
			return nil
		}
		v.ImageID = v.Prepared.ImageID
		v.PlacementID = placementID
		if v.NeedsRetransmit {
			delete(m.kittyImageCache, v.ImageID)
			delete(m.kittyPlacementCache, v.ImageID)
		}
		v.NeedsRetransmit = false
		m.markKittyImageTransmitted(v.ImageID)
		return tea.Raw(deferredCursorSequence(row+1, col+1, seq))
	case ImageBackendITerm2:
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

func (m *Model) iterm2InlineProtocolCmd() tea.Cmd {
	if m.viewport == nil || m.imageCaps.Backend != ImageBackendITerm2 || !m.imageCaps.SupportsInline {
		return nil
	}
	layout := m.ensureLayout()
	blocks := m.viewport.visibleBlocks()
	starts := m.viewport.blockStarts()
	windowStart := m.viewport.offset
	windowEnd := m.viewport.offset + m.viewport.height
	mainLeft := layout.main.Min.X
	mainTop := layout.main.Min.Y
	var seq strings.Builder
	for i, block := range blocks {
		if !blockSupportsImagePreview(block) {
			continue
		}
		block = m.viewport.materialize(block)
		blockStart := starts[i]
		style := UserCardStyle
		cardInnerOffset := style.GetMarginLeft() + style.GetBorderLeftSize() + style.GetPaddingLeft()
		imageCol := mainLeft + cardInnerOffset
		for _, part := range block.ImageParts {
			if part.RenderRows <= 0 || part.RenderStartLine < 0 || part.RenderCols <= 0 {
				continue
			}
			globalStart := blockStart + part.RenderStartLine
			globalEnd := blockStart + part.RenderEndLine
			if globalEnd < windowStart || globalStart >= windowEnd {
				continue
			}
			visibleStart := max(globalStart, windowStart)
			row := mainTop + (visibleStart - windowStart)
			col := imageCol + 2
			bodyRows := part.RenderRows
			if clippedTop := visibleStart - globalStart; clippedTop > 0 {
				bodyRows -= clippedTop
			}
			if bodyRows <= 0 {
				continue
			}
			seqPart, err := iterm2InlineSequence(part, part.RenderCols, bodyRows)
			if err != nil {
				continue
			}
			seq.WriteString(deferredCursorSequence(row+1, col+1, seqPart))
		}
	}
	if seq.Len() == 0 {
		return nil
	}
	return tea.Raw(encodeDeferredTerminalSequence(seq.String()))
}
