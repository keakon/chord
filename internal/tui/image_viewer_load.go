package tui

import (
	"context"
	"fmt"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/imageutil"
)

type imageViewerOwner struct {
	AgentID           string
	Session, Composer uint64
}

// Commands prepare transport data; the main loop consumes or expires Sequence.
// Rendering never invokes constructing APIs, even after cache eviction.
type imageViewerPrepared struct {
	Cols, Rows                   int
	AvailableCols, AvailableRows int
	Metrics                      kittyTerminalMetrics
	Sequence                     string
	ImageID                      int
}

type imageViewerLoadedMsg struct {
	Generation uint64
	Owner      imageViewerOwner
	Index      int
	Identity   imagePartKeyRef
	Prepared   *imageViewerPrepared
	Err        error
}

func (m *Model) prepareImageViewer() tea.Cmd {
	v := &m.imageViewer
	if !v.Open {
		return nil
	}
	if v.cancel != nil {
		v.cancel()
	}
	var cleanup tea.Cmd
	if v.ImageID > 0 && m.imageCaps.Backend == ImageBackendKitty {
		cleanup = m.releaseKittyViewerImage(v.ImageID, v.PlacementID)
	} else if v.Prepared != nil && m.imageCaps.Backend == ImageBackendITerm2 {
		cleanup = tea.ClearScreen
	}
	v.ImageID, v.PlacementID = 0, 0
	v.Loading, v.Error = true, ""
	v.Prepared = nil
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	m.imageViewerGeneration++
	generation := m.imageViewerGeneration
	part, owner, index := v.currentPart(), v.Owner, v.Index
	identity := imagePartKeyRefFor(part)
	cols, rows := m.imageViewerContentRect()
	metrics, backend := m.kittyMetrics, m.imageCaps.Backend
	load := func() tea.Msg {
		result := imageViewerLoadedMsg{Generation: generation, Owner: owner, Index: index, Identity: identity}
		if ctx.Err() != nil {
			return nil
		}
		prepared, err := prepareImageViewerPart(ctx, part, cols, rows, metrics, backend, int(generation))
		if ctx.Err() != nil {
			return nil
		}
		result.Prepared, result.Err = prepared, err
		return result
	}
	if cleanup != nil {
		return tea.Sequence(cleanup, load)
	}
	return load
}

func prepareImageViewerPart(ctx context.Context, part BlockImagePart, cols, rows int, metrics kittyTerminalMetrics, backend ImageBackend, placementID int) (*imageViewerPrepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		return nil, err
	}
	data, err := entry.raw(part)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Snapshot file bytes for this preparation only; keep browsing items and the
	// composer path-backed so loaded originals do not accumulate outside the cache.
	part.Data = data
	part.ImagePath = ""
	entry, err = imageRuntimeEntryForPart(part)
	if err != nil {
		return nil, err
	}
	cfg, _, err := entry.decodeConfig(part)
	if err != nil {
		return nil, err
	}
	fitCols, fitRows, err := imageViewerFitDimensions(cfg, cols, rows, metrics)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prepared := &imageViewerPrepared{Cols: fitCols, Rows: fitRows, AvailableCols: cols, AvailableRows: rows, Metrics: metrics}
	release, err := imageutil.AcquireDecodeSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	switch backend {
	case ImageBackendKitty:
		prepared.Sequence, prepared.ImageID, err = kittyViewerSequence(part, placementID, fitCols, fitRows, -1, 0, 0)
	case ImageBackendITerm2:
		prepared.Sequence, err = iterm2ViewerSequence(part, fitCols, fitRows)
		if err == nil {
			prepared.Sequence = encodeDeferredTerminalSequence(prepared.Sequence)
		}
	default:
		err = fmt.Errorf("image preview is unavailable in this terminal")
	}
	imageRuntimeCache.mu.Lock()
	imageRuntimeCache.enforceBudgetLocked()
	imageRuntimeCache.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (m *Model) handleImageViewerLoaded(msg imageViewerLoadedMsg) tea.Cmd {
	v := &m.imageViewer
	if !v.Open || m.mode != ModeImageViewer || msg.Generation != m.imageViewerGeneration || msg.Owner != v.Owner || msg.Owner != m.imageViewerOwner() || msg.Index != v.Index || msg.Identity != imagePartKeyRefFor(v.currentPart()) {
		return nil
	}
	v.Loading = false
	if msg.Err != nil {
		v.Error = msg.Err.Error()
		return nil
	}
	v.Prepared = msg.Prepared
	return m.imageViewerProtocolCmd()
}
