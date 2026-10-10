package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/imageutil"
)

type inlineImageRequest struct {
	part            BlockImagePart
	cols, rows      int
	row, col        int
	imageID         int
	alreadySent     bool
	metadata        imagePathMetadata
	metadataPresent bool
}

type inlineImagesLoadedMsg struct {
	generation    uint64
	owner         imageViewerOwner
	signature     string
	sequence      string
	imageIDs      []int
	metadata      []imagePathMetadataUpdate
	layoutChanged bool
}

// Inline protocols draw into the transcript; dialogs must remain the final layer.
func (m *Model) inlineImagesAllowed() bool {
	return (m.mode == ModeNormal || m.mode == ModeInsert || m.mode == ModeSearch) && !m.bottomLeftOverlayDrawn()
}

// Capture viewport state on the main loop; commands only use immutable parts.
func (m *Model) inlineImageRequests() ([]inlineImageRequest, string) {
	if m.viewport == nil || !m.imageCaps.SupportsInline || !m.inlineImagesAllowed() {
		return nil, ""
	}
	blocks, starts := m.viewport.visibleBlocks(), m.viewport.blockStarts()
	windowStart, windowEnd := m.viewport.offset, m.viewport.offset+m.viewport.height
	var requests []inlineImageRequest
	var signature strings.Builder
	layout := m.ensureLayout()
	fmt.Fprintf(&signature, "%d:%d:%d:%v:%v:", m.imageCaps.Backend, m.width, m.height, layout.main, m.imageViewerOwner())
	for i, block := range blocks {
		if !blockSupportsImagePreview(block) {
			continue
		}
		block = m.viewport.materialize(block)
		for _, part := range block.ImageParts {
			if part.RenderRows <= 0 || part.RenderStartLine < 0 || part.RenderCols <= 0 {
				continue
			}
			start, end := starts[i]+part.RenderStartLine, starts[i]+part.RenderEndLine
			if end < windowStart || start >= windowEnd {
				continue
			}
			key, err := imageInlineCacheKey(part)
			if err != nil {
				continue
			}
			r := inlineImageRequest{part: part, cols: part.RenderCols, rows: part.RenderRows}
			if len(part.Data) == 0 {
				r.metadata, r.metadataPresent = imagePathMetadataSnapshot(part)
			}
			if m.imageCaps.Backend == ImageBackendKitty {
				r.imageID, err = kittyRenderImageID(part, r.cols, r.rows)
				if err != nil {
					continue
				}
				r.alreadySent = m.kittyImageTransmitted(r.imageID) && m.kittyPlacementSent(r.imageID)
				if m.kittyImageTransmitted(r.imageID) {
					m.kittyImageCache[r.imageID] = time.Time{}
				}
			} else {
				visibleStart := max(start, windowStart)
				r.rows -= visibleStart - start
				if r.rows <= 0 {
					continue
				}
				r.row = layout.main.Min.Y + visibleStart - windowStart + 1
				r.col = layout.main.Min.X + imagePartBodyLeftColumn() + 1
			}
			fmt.Fprintf(&signature, "%q:%q:%d:%d:%d:%d:%t;", key, part.FileName, r.cols, r.rows, r.row, r.col, r.alreadySent)
			requests = append(requests, r)
		}
	}
	return requests, signature.String()
}

func (m *Model) cancelInlineImages() {
	if m.inlineImageCancel != nil {
		m.inlineImageCancel()
		m.inlineImageCancel = nil
	}
	m.inlineImageGeneration++
	m.inlineImageLoading, m.inlineImageSignature = false, ""
}

func (m *Model) prepareInlineImages(reason string) tea.Cmd {
	requests, signature := m.inlineImageRequests()
	kittyCount := 0
	needsSend := false
	for _, req := range requests {
		if m.imageCaps.Backend == ImageBackendKitty {
			kittyCount++
		}
		needsSend = needsSend || !req.alreadySent || len(req.part.Data) == 0 && (!req.metadataPresent || time.Since(req.metadata.checkedAt) >= imagePathMetadataRefreshInterval)
	}
	m.lastImageProtocolAt, m.lastImageProtocolReason = time.Now(), reason
	m.lastImageProtocolSummary = fmt.Sprintf("backend=%s visible_inline=%t kitty_visible_parts=%d loading=%t", m.imageCaps.Backend.String(), len(requests) > 0, kittyCount, needsSend)
	if m.inlineImageLoading && m.inlineImageSignature == signature {
		return nil
	}
	if m.inlineImageCancel != nil {
		m.inlineImageCancel()
	}
	m.inlineImageGeneration++
	m.inlineImageLoading, m.inlineImageSignature = needsSend, signature
	if !needsSend {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.inlineImageCancel = cancel
	generation, owner, backend := m.inlineImageGeneration, m.imageViewerOwner(), m.imageCaps.Backend
	return func() tea.Msg {
		result := inlineImagesLoadedMsg{generation: generation, owner: owner, signature: signature}
		var seq strings.Builder
		for _, req := range requests {
			if ctx.Err() != nil {
				return result
			}
			if len(req.part.Data) == 0 {
				metadata := loadImagePathMetadata(req.part)
				result.metadata = append(result.metadata, imagePathMetadataUpdate{ref: imagePartKeyRefFor(req.part), metadata: metadata})
				if imagePathMetadataChanged(req.metadata, req.metadataPresent, metadata) {
					result.layoutChanged = true
					continue
				}
				if metadata.err != nil {
					continue
				}
				req.part.cacheKey = metadata.key
			}
			if req.alreadySent {
				continue
			}
			// Admission is cancellable and held through transport construction.
			release, err := imageutil.AcquireDecodeSlot(ctx)
			if err != nil {
				return result
			}
			var part string
			if backend == ImageBackendKitty {
				part, _, err = kittyInlineSequence(req.part, req.cols, req.rows, false)
			} else {
				part, err = iterm2InlineSequence(req.part, req.cols, req.rows)
			}
			release()
			if err != nil || ctx.Err() != nil {
				continue
			}
			if backend == ImageBackendKitty {
				seq.WriteString(part)
				result.imageIDs = append(result.imageIDs, req.imageID)
			} else {
				seq.WriteString(deferredCursorSequence(req.row, req.col, part))
			}
		}
		result.sequence = seq.String()
		if backend == ImageBackendITerm2 {
			result.sequence = encodeDeferredTerminalSequence(result.sequence)
		}
		return result
	}
}

func (m *Model) handleInlineImagesLoaded(msg inlineImagesLoadedMsg) tea.Cmd {
	if msg.generation != m.inlineImageGeneration {
		return nil
	}
	m.inlineImageLoading = false
	if m.inlineImageCancel != nil {
		m.inlineImageCancel()
		m.inlineImageCancel = nil
	}
	_, signature := m.inlineImageRequests()
	if msg.owner != m.imageViewerOwner() || msg.signature != signature {
		return m.imageProtocolCmdWithReason("inline-image-stale")
	}
	for _, update := range msg.metadata {
		publishImagePathMetadata(update)
	}
	if msg.layoutChanged {
		for _, block := range m.viewport.visibleBlocks() {
			changed := false
			for _, part := range block.ImageParts {
				for _, update := range msg.metadata {
					if imagePartKeyRefFor(part) == update.ref {
						changed = true
						break
					}
				}
			}
			if changed {
				block.InvalidateCache()
				m.viewport.InvalidateBlock(block.ID)
			}
		}
		return m.imageProtocolCmdWithReason("image-metadata-ready")
	}
	if msg.sequence == "" {
		return nil
	}
	for _, id := range msg.imageIDs {
		m.markKittyImageTransmitted(id)
		m.markKittyPlacementSent(id)
	}
	return tea.Raw(msg.sequence)
}
