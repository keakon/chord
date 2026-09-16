package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

func TestWindowSizeMsgShrinkIsDebounced(t *testing.T) {
	m := NewModelWithSize(nil, 121, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 119, Height: 39})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 121 || model.height != 40 {
		t.Fatalf("size changed eagerly to %dx%d, want initial 121x40", model.width, model.height)
	}
	if model.pendingResizeW != 119 || model.pendingResizeH != 39 {
		t.Fatalf("pending resize = %dx%d, want 119x39", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("small shrink WindowSizeMsg should schedule a debounced applyResizeMsg")
	}
}

func TestWindowSizeMsgLargeWidthShrinkAppliesImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 121, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 115, Height: 40})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 115 || model.height != 40 {
		t.Fatalf("size = %dx%d, want immediate 115x40", model.width, model.height)
	}
	if model.pendingResizeW != 115 || model.pendingResizeH != 40 {
		t.Fatalf("pending resize = %dx%d, want 115x40", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if model.rightPanelVisible {
		t.Fatal("rightPanelVisible should be false after immediate shrink to width 115")
	}
	if cmd != nil {
		t.Fatalf("large width shrink should not debounce, got %#v", cmd)
	}
}

func TestWindowSizeMsgLargeHeightShrinkAppliesImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 120 || model.height != 36 {
		t.Fatalf("size = %dx%d, want immediate 120x36", model.width, model.height)
	}
	if model.pendingResizeW != 120 || model.pendingResizeH != 36 {
		t.Fatalf("pending resize = %dx%d, want 120x36", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd != nil {
		t.Fatalf("large height shrink should not debounce, got %#v", cmd)
	}
}

func TestWindowSizeMsgLargeWidthShrinkHeightSmallShrinkAppliesWidthImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 150, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 144, Height: 38})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 144 {
		t.Fatalf("width = %d, want immediate 144", model.width)
	}
	if model.height != 40 {
		t.Fatalf("height = %d, want old 40 until debounced small shrink applies", model.height)
	}
	if model.pendingResizeW != 144 || model.pendingResizeH != 38 {
		t.Fatalf("pending resize = %dx%d, want 144x38", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("remaining small shrink should still schedule debounced apply")
	}
}

func TestWindowSizeMsgGrowthAppliesImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 120, 35)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 || model.height != 40 {
		t.Fatalf("size = %dx%d, want immediate 150x40", model.width, model.height)
	}
	if model.pendingResizeW != 150 || model.pendingResizeH != 40 {
		t.Fatalf("pending resize = %dx%d, want 150x40", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd != nil {
		t.Fatalf("growth WindowSizeMsg should not debounce, got %#v", cmd)
	}
}

func TestWindowSizeMsgWidthGrowthHeightShrinkAppliesWidthImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 150, Height: 37})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 {
		t.Fatalf("width = %d, want immediate 150", model.width)
	}
	if model.height != 40 {
		t.Fatalf("height = %d, want old 40 until debounced shrink applies", model.height)
	}
	if model.pendingResizeW != 150 || model.pendingResizeH != 37 {
		t.Fatalf("pending resize = %dx%d, want 150x37", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("mixed grow/shrink resize should still schedule debounced shrink apply")
	}
}

func TestWindowSizeMsgHeightGrowthWidthShrinkAppliesHeightImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 150, 35)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 145, Height: 40})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 {
		t.Fatalf("width = %d, want old 150 until debounced shrink applies", model.width)
	}
	if model.height != 40 {
		t.Fatalf("height = %d, want immediate 40", model.height)
	}
	if model.pendingResizeW != 145 || model.pendingResizeH != 40 {
		t.Fatalf("pending resize = %dx%d, want 145x40", model.pendingResizeW, model.pendingResizeH)
	}
	if model.resizeVersion != 1 {
		t.Fatalf("resizeVersion = %d, want 1", model.resizeVersion)
	}
	if cmd == nil {
		t.Fatal("mixed grow/shrink resize should schedule debounced shrink apply")
	}
}

func TestFocusMsgWhenImageViewerOpenMarksViewerForRetransmitAndClearsViewerCache(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeImageViewer
	m.imageViewer = imageViewerState{Open: true, ImageID: 123, NeedsRetransmit: false}
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
	m.kittyImageCache[123] = struct{}{}
	m.kittyPlacementCache[123] = struct{}{}
	m.kittyImageCache[999] = struct{}{}
	m.kittyPlacementCache[999] = struct{}{}

	updated, _ := m.Update(tea.FocusMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.imageViewer.NeedsRetransmit {
		t.Fatal("FocusMsg should mark open image viewer for retransmit")
	}
	if _, ok := model.kittyImageCache[123]; ok {
		t.Fatal("FocusMsg should clear current viewer image cache entry")
	}
	if _, ok := model.kittyPlacementCache[123]; ok {
		t.Fatal("FocusMsg should clear current viewer placement cache entry")
	}
	if _, ok := model.kittyImageCache[999]; !ok {
		t.Fatal("FocusMsg should not clear unrelated kitty image cache entries")
	}
	if _, ok := model.kittyPlacementCache[999]; !ok {
		t.Fatal("FocusMsg should not clear unrelated kitty placement cache entries")
	}
}

func TestFocusMsgWhenKittyImageViewerSchedulesDeferredReplay(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	pngData := makeTestPNG(t)
	block := &Block{
		ID:         42,
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
	m.imageViewer.ImageID = 123
	m.imageViewer.PlacementID = 456
	m.imageViewer.NeedsRetransmit = false
	m.kittyImageCache[123] = struct{}{}
	m.kittyPlacementCache[123] = struct{}{}

	updated, cmd := m.Update(tea.FocusMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("FocusMsg should schedule kitty image viewer replay when viewer is renderable")
	}
	if model.imageViewer.NeedsRetransmit {
		t.Fatal("FocusMsg should replay renderable kitty viewer immediately")
	}
	if model.lastImageProtocolReason != "focus-restore" {
		t.Fatalf("lastImageProtocolReason = %q, want focus-restore", model.lastImageProtocolReason)
	}
	if got := cmd(); got == nil {
		t.Fatal("FocusMsg replay command should emit a message")
	}
}

func TestFocusMsgWhenVisibleInlineImagesReplayEvenWithoutFreeze(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true}
	m.viewport.height = 8
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{
			FileName:        "img.png",
			MimeType:        "image/png",
			Data:            makeTestPNG(t),
			RenderRows:      2,
			RenderCols:      8,
			RenderStartLine: 0,
			RenderEndLine:   1,
		}},
	})
	m.recalcViewportSize()
	m.viewport.offset = 0
	m.layout = m.generateLayout(m.width, m.height)

	updated, cmd := m.Update(tea.FocusMsg{})
	_ = updated.(*Model)
	if cmd == nil {
		t.Fatal("FocusMsg should schedule inline image replay when visible inline images exist")
	}
}

func TestFocusRestoreTriggersImageProtocolReplay(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendITerm2, SupportsInline: true}
	m.viewport.height = 8
	m.viewport.AppendBlock(&Block{
		ID:         1,
		Type:       BlockUser,
		ImageCount: 1,
		ImageParts: []BlockImagePart{{FileName: "sample.png", MimeType: "image/png", Data: makeTestPNG(t), RenderCols: 4, RenderRows: 2, RenderStartLine: 0, RenderEndLine: 1}},
	})
	m.recalcViewportSize()
	m.viewport.offset = 0
	m.layout = m.generateLayout(m.width, m.height)

	updated, cmd := m.Update(tea.FocusMsg{})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("focus restore should replay image protocol")
	}
	if !strings.Contains(model.lastImageProtocolReason, "focus-restore") {
		t.Fatalf("lastImageProtocolReason = %q, want focus-restore", model.lastImageProtocolReason)
	}
}

func TestApplyResizeMsgUnchangedPendingSizeDoesNothing(t *testing.T) {
	m := NewModelWithSize(nil, 120, 40)
	m.pendingResizeW = 120
	m.pendingResizeH = 40
	m.resizeVersion = 2

	updated, cmd := m.Update(applyResizeMsg{version: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if model.width != 120 || model.height != 40 {
		t.Fatalf("size changed unexpectedly to %dx%d", model.width, model.height)
	}
	if cmd != nil {
		t.Fatalf("no-op applyResizeMsg should not schedule command, got %#v", cmd)
	}
}

func TestApplyResizeMsgUsesLatestPendingSize(t *testing.T) {
	m := NewModelWithSize(nil, 150, 40)
	m.pendingResizeW = 121
	m.pendingResizeH = 36
	m.resizeVersion = 2

	updated, cmd := m.Update(applyResizeMsg{version: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 121 || model.height != 36 {
		t.Fatalf("applied size = %dx%d, want 121x36", model.width, model.height)
	}
	if !model.rightPanelVisible {
		t.Fatal("rightPanelVisible should remain true at width 121")
	}
	if cmd != nil {
		t.Fatalf("applyResizeMsg should not schedule extra command, got %#v", cmd)
	}
}

func TestDirectoryModeKeepsRightPanelLayoutAndStatusBarState(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "alpha"})

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl})); cmd != nil {
		t.Fatalf("opening directory should not schedule command, got %#v", cmd)
	}
	if m.mode != ModeDirectory {
		t.Fatalf("mode = %v, want ModeDirectory", m.mode)
	}
	layout := m.generateLayout(m.width, m.height)
	if layout.infoPanel.Dx() == 0 {
		t.Fatal("directory mode should keep the right panel/sidebar visible")
	}
	if layout.main.Max.X >= layout.infoPanel.Min.X {
		t.Fatalf("main layout overlaps right panel: main=%v infoPanel=%v", layout.main, layout.infoPanel)
	}
	if !m.statusBarInputs(time.Now()).InfoPanelVisible {
		t.Fatal("directory mode status bar should keep the side-panel-visible layout state")
	}
}

func TestDirectoryItemsAreNumberedAndPageNavigationWorks(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeDirectory
	m.dirEntries = make([]DirectoryEntry, 10)
	for i := range m.dirEntries {
		m.dirEntries[i] = DirectoryEntry{BlockIndex: i, Summary: fmt.Sprintf("message-%02d", i+1)}
	}
	m.dirList = NewOverlayList(directoryItems(m.dirEntries), 3)
	m.viewport.SetSize(80, 7)

	rendered := stripANSI(m.renderDirectory())
	if !strings.Contains(rendered, "1. message-01") || !strings.Contains(rendered, "2. message-02") {
		t.Fatalf("directory rows should be numbered, got:\n%s", rendered)
	}

	_ = m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	if got := m.dirList.CursorAt(); got != 3 {
		t.Fatalf("cursor after PgDown = %d, want 3", got)
	}
	_ = m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: 'f', Mod: tea.ModCtrl}))
	if got := m.dirList.CursorAt(); got != 6 {
		t.Fatalf("cursor after Ctrl+F = %d, want 6", got)
	}
	_ = m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if got := m.dirList.CursorAt(); got != 3 {
		t.Fatalf("cursor after PgUp = %d, want 3", got)
	}
	_ = m.handleDirectoryKey(tea.KeyPressMsg(tea.Key{Code: 'b', Mod: tea.ModCtrl}))
	if got := m.dirList.CursorAt(); got != 0 {
		t.Fatalf("cursor after Ctrl+B = %d, want 0", got)
	}
}

func TestApplyResizeMsgIgnoresSupersededVersion(t *testing.T) {
	m := NewModelWithSize(nil, 150, 40)
	m.pendingResizeW = 121
	m.pendingResizeH = 36
	m.resizeVersion = 3

	updated, _ := m.Update(applyResizeMsg{version: 2})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}

	if model.width != 150 || model.height != 40 {
		t.Fatalf("superseded resize changed size to %dx%d, want 150x40", model.width, model.height)
	}
}
