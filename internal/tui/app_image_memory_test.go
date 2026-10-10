package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

func TestKittyOffScreenGraceAndReturnToViewport(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	requests, _ := m.inlineImageRequests()
	id := requests[0].imageID
	m.markKittyImageTransmitted(id)
	m.markKittyPlacementSent(id)
	now := time.Now()
	if m.releaseOffScreenKittyImages(now) != nil {
		t.Fatal("visible image was deleted")
	}
	m.viewport.offset = m.viewport.TotalLines() + 100
	if m.releaseOffScreenKittyImages(now) != nil {
		t.Fatal("off-screen image was deleted immediately")
	}
	if m.releaseOffScreenKittyImages(now.Add(kittyOffScreenGrace-time.Nanosecond)) != nil {
		t.Fatal("grace was not respected")
	}
	m.viewport.offset = 0
	if m.releaseOffScreenKittyImages(now.Add(kittyOffScreenGrace)) != nil || !m.kittyImageCache[id].IsZero() {
		t.Fatal("returning within grace did not reset expiry")
	}
	m.viewport.offset = m.viewport.TotalLines() + 100
	m.releaseOffScreenKittyImages(now)
	cmd := m.releaseOffScreenKittyImages(now.Add(kittyOffScreenGrace))
	if cmd == nil || m.kittyImageTransmitted(id) || m.kittyPlacementSent(id) {
		t.Fatal("off-screen terminal data was not forgotten")
	}
	value := reflect.ValueOf(cmd())
	var msgs []tea.Msg
	for idx := 0; idx < value.Len(); idx++ {
		child, _ := reflect.TypeAssert[tea.Cmd](value.Index(idx))
		msgs = append(msgs, child())
	}
	var deleted bool
	var released kittyImagesReleasedMsg
	for _, msg := range msgs {
		switch msg := msg.(type) {
		case tea.RawMsg:
			deleted = strings.Contains(fmt.Sprint(msg.Msg), "d=I")
		case kittyImagesReleasedMsg:
			released = msg
		}
	}
	if !deleted {
		t.Fatal("protocol deleted placement without freeing image data")
	}
	// Simulate scrolling back and a retransmit while the deletion was queued.
	m.viewport.offset = 0
	m.markKittyImageTransmitted(id)
	m.markKittyPlacementSent(id)
	next := m.handleKittyImagesReleased(released)
	if next == nil {
		t.Fatal("overlapping retransmit was not reconciled")
	}
	loaded := next().(inlineImagesLoadedMsg)
	if loaded.sequence == "" || m.handleInlineImagesLoaded(loaded) == nil {
		t.Fatal("returning to the viewport did not rebuild the image")
	}
}

func TestImageMemorySweepKeepsVisibleViewerAndExpiresITermTransport(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	m.mode = ModeImageViewer
	m.imageViewer = imageViewerState{Open: true, ImageID: 123, Prepared: &imageViewerPrepared{Sequence: "payload"}, lastProtocolAt: time.Now().Add(-imageViewerTransportTTL)}
	m.markKittyImageTransmitted(123)
	m.kittyImageCache[123] = time.Now().Add(-kittyOffScreenGrace)
	m.handleImageMemorySweepTick(imageMemorySweepTickMsg{generation: m.imageMemorySweepGeneration})
	if !m.kittyImageTransmitted(123) || m.imageViewer.Prepared == nil || m.imageViewer.Prepared.Sequence != "" {
		t.Fatal("idle sweep deleted the visible bitmap or kept its transport")
	}
	m.imageCaps.Backend = ImageBackendITerm2
	if m.imageViewerProtocolCmd() == nil || !m.imageViewer.Loading {
		t.Fatal("expired iTerm transport was not prepared asynchronously on redraw")
	}
}

func TestKittyViewerDropsFilePayloadAndUsesPlacementForRedraw(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	m.imageCaps.SupportsFullscreen = true
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, Valid: true}
	path := filepath.Join(t.TempDir(), "sample.png")
	if err := os.WriteFile(path, makeTestPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	m.openImageViewerItems([]BlockImagePart{{ImagePath: path, MimeType: "image/png"}}, 0, ModeNormal)
	finishImageViewerLoad(t, &m)
	if len(m.imageViewer.Items[0].Data) != 0 || m.imageViewer.Items[0].ImagePath != path {
		t.Fatal("viewer pinned the loaded original in its browsing list")
	}
	if m.imageViewer.Prepared.Sequence != "" {
		t.Fatal("Kitty viewer retained transmitted bytes")
	}
	cmd := m.imageViewerProtocolCmd()
	if cmd == nil {
		t.Fatal("missing placement redraw")
	}
	raw := cmd().(tea.RawMsg)
	seq := fmt.Sprint(raw.Msg)
	if !strings.Contains(seq, "a=p") || strings.Contains(seq, "a=t") {
		t.Fatal("redraw retransmitted bytes instead of reusing the terminal bitmap")
	}
	id := m.imageViewer.ImageID
	cleanup := m.dismissImageViewer()
	if cleanup == nil || m.kittyImageTransmitted(id) || !strings.Contains(fmt.Sprint(cleanup().(tea.RawMsg).Msg), "d=I") {
		t.Fatal("viewer close did not release the terminal resource")
	}
}

func TestImageMemorySweepStaleAfterClose(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	generation := m.imageMemorySweepGeneration
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if m.handleImageMemorySweepTick(imageMemorySweepTickMsg{generation: generation}) != nil {
		t.Fatal("closed model restarted maintenance")
	}
	if m.handleKittyImagesReleased(kittyImagesReleasedMsg{generation: generation}) != nil {
		t.Fatal("closed model emitted a recovery command")
	}
}

func TestKittyMemorySweepKeepsViewportImagesUnderDialog(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	requests, _ := m.inlineImageRequests()
	id := requests[0].imageID
	m.markKittyImageTransmitted(id)
	m.markKittyPlacementSent(id)
	m.mode = ModeHelp
	m.kittyImageCache[id] = time.Now().Add(-kittyOffScreenGrace)
	if m.releaseOffScreenKittyImages(time.Now()) != nil || !m.kittyImageTransmitted(id) {
		t.Fatal("dialog caused visible viewport images to be reclaimed")
	}
}

func TestKittyMemorySweepFollowsPaintedIDWhenFileChanges(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	path := filepath.Join(t.TempDir(), "sample.png")
	if err := os.WriteFile(path, makeTestPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	m.viewport.blocks[0].ImageParts = []BlockImagePart{{ImagePath: path, MimeType: "image/png", RenderStartLine: -1}}
	m.viewport.blocks[0].InvalidateCache()
	m.viewport.InvalidateBlock(m.viewport.blocks[0].ID)
	m.viewport.recalcTotalLines()
	m.viewport.Render("", nil, -1, 0, "")
	id := m.viewport.blocks[0].ImageParts[0].RenderImageID
	if id <= 0 {
		t.Fatal("render did not publish its Kitty image ID")
	}
	m.markKittyImageTransmitted(id)
	m.markKittyPlacementSent(id)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.releaseOffScreenKittyImages(now)
	if m.releaseOffScreenKittyImages(now.Add(kittyOffScreenGrace)) != nil || !m.kittyImageTransmitted(id) {
		t.Fatal("source change caused a still-painted image to be reclaimed")
	}
}

func TestKittyMemorySweepDoesNotMeasureInvalidLayout(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	requests, _ := m.inlineImageRequests()
	id := requests[0].imageID
	m.markKittyImageTransmitted(id)
	m.kittyImageCache[id] = time.Now().Add(-kittyOffScreenGrace)
	m.viewport.blockStartsCache = nil
	if m.releaseOffScreenKittyImages(time.Now()) != nil || !m.kittyImageTransmitted(id) {
		t.Fatal("unsettled layout was treated as off-screen")
	}
	if m.viewport.blockStartsCache != nil {
		t.Fatal("maintenance remeasured invalid layout")
	}
}
