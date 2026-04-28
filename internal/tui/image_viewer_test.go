package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestOpenImageViewerUsesRequestedImageIndex(t *testing.T) {
	ApplyTheme(DefaultTheme())
	model := NewModelWithSize(nil, 80, 24)
	m := model
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         42,
		Type:       BlockUser,
		ImageCount: 2,
		ImageParts: []BlockImagePart{
			{FileName: "image1.png", MimeType: "image/png", Data: pngData, RenderStartLine: -1, RenderEndLine: -1},
			{FileName: "image2.png", MimeType: "image/png", Data: pngData, RenderStartLine: -1, RenderEndLine: -1},
		},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}

	m.openImageViewer(block.ID, 1)
	if !m.imageViewer.Open {
		t.Fatal("image viewer should be open")
	}
	if got := m.imageViewer.Index; got != 1 {
		t.Fatalf("imageViewer.Index = %d, want 1", got)
	}
	if got := m.imageViewer.Total; got != 2 {
		t.Fatalf("imageViewer.Total = %d, want 2", got)
	}
	if got := m.imageViewer.Part.FileName; got != "image2.png" {
		t.Fatalf("imageViewer.Part.FileName = %q, want %q", got, "image2.png")
	}
}

func TestStepImageViewerUsesSequenceForKittyPhysicalPlacement(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         7,
		Type:       BlockUser,
		ImageCount: 2,
		ImageParts: []BlockImagePart{
			{FileName: "image1.png", MimeType: "image/png", Data: pngData, RenderStartLine: -1, RenderEndLine: -1},
			{FileName: "image2.png", MimeType: "image/png", Data: pngData, RenderStartLine: -1, RenderEndLine: -1},
		},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.openImageViewer(block.ID, 0)
	m.imageViewer.ImageID = 123
	m.imageViewer.PlacementID = 456
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)

	cmd := m.stepImageViewer(1)
	if cmd == nil {
		t.Fatal("stepImageViewer() returned nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.RawMsg); ok {
		t.Fatal("stepImageViewer() should return a sequence, not a single raw command")
	}
	if got := fmt.Sprintf("%T", msg); got != "tea.sequenceMsg" {
		t.Fatalf("stepImageViewer() msg = %s, want tea.sequenceMsg", got)
	}
	if got := m.imageViewer.Index; got != 1 {
		t.Fatalf("imageViewer.Index = %d, want 1", got)
	}
}

func TestCloseImageViewerAvoidsClearScreenBatch(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.imageViewer = imageViewerState{Open: true, ImageID: 11, PlacementID: 22}

	cmd := m.closeImageViewer()
	if cmd == nil {
		t.Fatal("closeImageViewer() returned nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatal("closeImageViewer() should not batch clear screen with delete command")
	}
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("closeImageViewer() msg = %T, want tea.RawMsg", msg)
	}
	if got, want := fmt.Sprint(raw.Msg), kittyDeleteSequenceForPlacement(11, 22); got != want {
		t.Fatalf("closeImageViewer() raw msg = %q, want %q", got, want)
	}
	if m.imageViewer.Open {
		t.Fatal("image viewer should be closed")
	}
}

func TestImageViewerOutsideClickClosesWithoutClearScreenSequence(t *testing.T) {
	ApplyTheme(DefaultTheme())
	model := NewModelWithSize(nil, 80, 24)
	m := model
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         5,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "image.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.openImageViewer(block.ID, 0)
	m.imageViewer.ImageID = 11
	m.imageViewer.PlacementID = 22
	m.layout = m.generateLayout(m.width, m.height)
	rect, _ := m.imageViewerOverlayRect()
	if rect.Empty() {
		t.Fatal("expected non-empty image viewer rect")
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: rect.Min.X - 1, Y: rect.Min.Y, Button: tea.MouseLeft})
	modelPtr := updated.(*Model)
	if cmd == nil {
		t.Fatal("outside click should close viewer")
	}
	msg := cmd()
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatal("outside click close should not batch clear screen")
	}
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("outside click close msg = %T, want tea.RawMsg", msg)
	}
	if got, want := fmt.Sprint(raw.Msg), kittyDeleteSequenceForPlacement(11, 22); got != want {
		t.Fatalf("outside click close raw msg = %q, want %q", got, want)
	}
	if modelPtr.imageViewer.Open {
		t.Fatal("image viewer should be closed after outside click")
	}
}

func TestToggleCollapseOnFocusedImageReturnsImageProtocolCmd(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         17,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "image.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	if cmd == nil {
		t.Fatal("space on focused image should return image protocol command")
	}
	if !m.imageViewer.Open {
		t.Fatal("space on focused image should open viewer")
	}
	msg := cmd()
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("space open viewer msg = %T, want tea.RawMsg", msg)
	}
	if got := fmt.Sprint(raw.Msg); got == "" {
		t.Fatal("space open viewer should emit non-empty image protocol sequence")
	}
}

func TestSingleImageViewerDoesNotNavigateOrShowSwitchHint(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         18,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "solo.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)
	m.openImageViewer(block.ID, 0)
	m.imageViewer.ImageID = 123
	m.imageViewer.PlacementID = 456

	if cmd := m.stepImageViewer(1); cmd != nil {
		t.Fatal("single-image viewer should not navigate on right switch")
	}
	if got := m.imageViewer.Index; got != 0 {
		t.Fatalf("imageViewer.Index = %d, want 0", got)
	}
	if got := imageViewerHintText(m.imageCaps, m.imageViewer.Total); got != imageViewerSingleHint {
		t.Fatalf("imageViewerHintText(single) = %q, want %q", got, imageViewerSingleHint)
	}
	overlay := stripANSI(m.renderImageViewerOverlay())
	if !strings.Contains(overlay, imageViewerCloseBadge) {
		t.Fatalf("single-image overlay title should include close badge %q, got %q", imageViewerCloseBadge, overlay)
	}
	if !strings.Contains(overlay, "Image Viewer · solo.png") {
		t.Fatalf("single-image overlay title should include filename, got %q", overlay)
	}
}

func TestOpenImageViewerMarksNeedsRetransmit(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{ID: 21, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: "img.png", MimeType: "image/png", Data: pngData}}}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}

	m.openImageViewer(block.ID, 0)
	if !m.imageViewer.NeedsRetransmit {
		t.Fatal("opening image viewer should mark it for retransmit")
	}
}

func TestImageViewerFitSizeLimitsUpscaleWithKittyMetrics(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 160, 60)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         19,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "tiny.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 1280, WindowHeightPx: 960, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)
	m.openImageViewer(block.ID, 0)

	cols, rows, err := m.imageViewerFitSize()
	if err != nil {
		t.Fatalf("imageViewerFitSize() error = %v", err)
	}
	if cols > 2 || rows > 1 {
		t.Fatalf("tiny 2x2 image should not upscale beyond 2x1 cells under 2x cap, got %dx%d", cols, rows)
	}
}

func TestImageViewerPhysicalPlacementMatchesOverlayContentGeometry(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         29,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "image.png", MimeType: "image/png", Data: pngData}},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)
	m.openImageViewer(block.ID, 0)

	rect, _ := m.imageViewerOverlayRect()
	if rect.Empty() {
		t.Fatal("imageViewerOverlayRect() returned empty rect")
	}
	fitCols, fitRows, err := m.imageViewerFitSize()
	if err != nil {
		t.Fatalf("imageViewerFitSize() error = %v", err)
	}
	placementID, row, col, _, _, ok := m.imageViewerPhysicalPlacement()
	if !ok {
		t.Fatal("imageViewerPhysicalPlacement() returned not ok")
	}
	if placementID <= 0 {
		t.Fatalf("placementID = %d, want > 0", placementID)
	}

	availableCols := rect.Dx() - 2 - DirectoryBorderStyle.GetHorizontalPadding() - 2*imageViewerInnerPadX
	if availableCols < fitCols {
		t.Fatalf("availableCols = %d, want >= fitCols %d", availableCols, fitCols)
	}
	expectedCol := rect.Min.X + 1 + DirectoryBorderStyle.GetPaddingLeft() + imageViewerInnerPadX + (availableCols-fitCols)/2
	if col != expectedCol {
		t.Fatalf("anchor col = %d, want %d", col, expectedCol)
	}
	expectedRow := rect.Min.Y + 2 + imageViewerInnerPadY
	if row != expectedRow {
		t.Fatalf("anchor row = %d, want %d", row, expectedRow)
	}
	if m.imageViewer.FitHeight != fitRows && m.imageViewer.FitHeight != 0 {
		t.Fatalf("imageViewer.FitHeight = %d, want %d or 0 before render", m.imageViewer.FitHeight, fitRows)
	}
}

func TestStepImageViewerDeleteCmdIsImmediateForKittyPhysicalPlacement(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         9,
		Type:       BlockUser,
		ImageCount: 2,
		ImageParts: []BlockImagePart{
			{FileName: "image1.png", MimeType: "image/png", Data: pngData, RenderStartLine: -1, RenderEndLine: -1},
			{FileName: "image2.png", MimeType: "image/png", Data: pngData, RenderStartLine: -1, RenderEndLine: -1},
		},
	}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = block.ID
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.openImageViewer(block.ID, 0)
	m.imageViewer.ImageID = 123
	m.imageViewer.PlacementID = 456
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, WindowWidthPx: 640, WindowHeightPx: 384, Valid: true}
	m.layout = m.generateLayout(m.width, m.height)

	cmd := m.stepImageViewer(1)
	if cmd == nil {
		t.Fatal("stepImageViewer() returned nil")
	}
	msg := cmd()
	if got := fmt.Sprintf("%T", msg); got != "tea.sequenceMsg" {
		t.Fatalf("stepImageViewer() msg = %T, want tea.sequenceMsg", msg)
	}
	seqVal := reflect.ValueOf(msg)
	if seqVal.Kind() != reflect.Slice {
		t.Fatalf("stepImageViewer() msg kind = %s, want slice", seqVal.Kind())
	}
	if seqVal.Len() < 2 {
		t.Fatalf("stepImageViewer() sequence len = %d, want >= 2", seqVal.Len())
	}
	firstCmd, ok := seqVal.Index(0).Interface().(tea.Cmd)
	if !ok {
		t.Fatalf("first sequence entry = %T, want tea.Cmd", seqVal.Index(0).Interface())
	}
	first := firstCmd()
	raw, ok := first.(tea.RawMsg)
	if !ok {
		t.Fatalf("first sequence msg = %T, want tea.RawMsg", first)
	}
	if got, want := fmt.Sprint(raw.Msg), kittyDeleteSequenceForPlacement(123, 456); got != want {
		t.Fatalf("delete raw msg = %q, want %q", got, want)
	}
}
