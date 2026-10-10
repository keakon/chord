package tui

import (
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	imageMemorySweepInterval = 15 * time.Second
	kittyOffScreenGrace      = 30 * time.Second
	imageViewerTransportTTL  = 5 * time.Minute
)

type imageMemorySweepTickMsg struct{ generation uint64 }

type kittyImagesReleasedMsg struct {
	generation uint64
	imageIDs   []int
}

// Maintenance uses Bubble Tea's timer lifecycle rather than a cache goroutine.
func (m *Model) startImageMemorySweep() tea.Cmd {
	m.imageMemorySweepGeneration++
	return imageMemorySweepTick(m.imageMemorySweepGeneration)
}

func imageMemorySweepTick(generation uint64) tea.Cmd {
	return tickCmd(imageMemorySweepInterval, func(time.Time) tea.Msg {
		return imageMemorySweepTickMsg{generation: generation}
	})
}

func (m *Model) handleImageMemorySweepTick(msg imageMemorySweepTickMsg) tea.Cmd {
	if msg.generation != m.imageMemorySweepGeneration {
		return nil
	}
	now := time.Now()
	imageRuntimeCache.releaseIdlePayloads(now)
	v := &m.imageViewer
	// The terminal retains the displayed bitmap. A later redraw rebuilds the
	// iTerm2 transport asynchronously; Kitty redraws use placement commands.
	if v.Prepared != nil && now.Sub(v.lastProtocolAt) >= imageViewerTransportTTL {
		v.Prepared.Sequence = ""
	}
	return tea.Batch(m.releaseOffScreenKittyImages(now), m.refreshImagePathMetadata(), imageMemorySweepTick(msg.generation))
}

func (m *Model) releaseOffScreenKittyImages(now time.Time) tea.Cmd {
	if m.imageCaps.Backend != ImageBackendKitty || len(m.kittyImageCache) == 0 {
		return nil
	}
	visible := m.visibleKittyImageIDs()
	if m.imageViewer.Open && m.mode == ModeImageViewer && m.imageViewer.ImageID > 0 {
		visible[m.imageViewer.ImageID] = struct{}{}
	}
	var seq strings.Builder
	var released []int
	for id, hiddenSince := range m.kittyImageCache {
		if _, ok := visible[id]; ok {
			m.kittyImageCache[id] = time.Time{}
		} else if hiddenSince.IsZero() {
			m.kittyImageCache[id] = now
		} else if now.Sub(hiddenSince) >= kittyOffScreenGrace {
			// Uppercase d=I requests deletion of placements AND stored data.
			seq.WriteString(encodeKittyDeleteImage(id))
			released = append(released, id)
			delete(m.kittyImageCache, id)
			delete(m.kittyPlacementCache, id)
		}
	}
	if seq.Len() == 0 {
		return nil
	}
	generation := m.imageMemorySweepGeneration
	return tea.Sequence(tea.Raw(seq.String()), func() tea.Msg {
		return kittyImagesReleasedMsg{generation: generation, imageIDs: released}
	})
}

func (m *Model) releaseKittyViewerImage(imageID, placementID int) tea.Cmd {
	delete(m.kittyImageCache, imageID)
	delete(m.kittyPlacementCache, imageID)
	return tea.Raw(kittyDeleteSequenceForPlacement(imageID, placementID))
}

// A user can scroll back while deletion is queued. Reconcile after the RawMsg
// was delivered so an overlapping retransmit cannot leave visible placeholders
// pointing at deleted data.
func (m *Model) handleKittyImagesReleased(msg kittyImagesReleasedMsg) tea.Cmd {
	if msg.generation != m.imageMemorySweepGeneration {
		return nil
	}
	for _, id := range msg.imageIDs {
		delete(m.kittyImageCache, id)
		delete(m.kittyPlacementCache, id)
	}
	return m.imageProtocolCmdWithReason("image-memory-released")
}

// Follow IDs actually painted into the viewport, including uncovered thumbnails
// beneath a dialog. Do not stat paths or rehash sources on a maintenance tick:
// a file change must not make the still-displayed bitmap look off-screen.
func (m *Model) visibleKittyImageIDs() map[int]struct{} {
	visible := make(map[int]struct{})
	if m.viewport == nil {
		return visible
	}
	v := m.viewport
	blocks := v.visibleBlocks()
	// Layout has not yet settled: conservatively retain terminal data until the
	// normal rendering path publishes positions, instead of measuring on a tick.
	if !v.blockPositionCacheValid(blocks) {
		for id := range m.kittyImageCache {
			visible[id] = struct{}{}
		}
		return visible
	}
	starts := v.blockStartsCache
	windowStart, windowEnd := v.offset, v.offset+v.height
	for i, block := range blocks {
		if !blockSupportsImagePreview(block) {
			continue
		}
		for _, part := range block.ImageParts {
			if part.RenderImageID <= 0 || part.RenderRows <= 0 || part.RenderStartLine < 0 {
				continue
			}
			start, end := starts[i]+part.RenderStartLine, starts[i]+part.RenderEndLine
			if end >= windowStart && start < windowEnd {
				visible[part.RenderImageID] = struct{}{}
			}
		}
	}
	return visible
}
