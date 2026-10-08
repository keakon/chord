package tui

import (
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func newInputImagePreviewModel(t *testing.T, backend ImageBackend) *Model {
	t.Helper()
	m := NewModelWithSize(nil, 80, 24)
	m.imageCaps = TerminalImageCapabilities{Backend: backend, SupportsFullscreen: true}
	m.kittyMetrics = kittyTerminalMetrics{CellWidthPx: 8, CellHeightPx: 16, Valid: true}
	m.input.SetValue("before ")
	m.input.CursorEnd()
	m.attachments = []Attachment{
		{FileName: "first.png", MimeType: "image/png", Data: makeTestPNG(t), InlineImagePlaceholder: true},
		{FileName: "report.pdf", MimeType: "application/pdf"},
		{FileName: "second.png", MimeType: "image/png", Data: makeTestPNG(t), InlineImagePlaceholder: true},
	}
	m.input.InsertImagePlaceholderWithDisplay(2, "[sample.png]")
	m.input.InsertStringPreserveInlinePastes(" between ")
	m.input.InsertImagePlaceholderWithDisplay(1, "[sample.png]")
	m.input.InsertStringPreserveInlinePastes(" after")
	m.input.syncHeight()
	m.recalcViewportSize()
	m.ensureLayout()
	return &m
}

func previewCommandMessage(t *testing.T, command tea.Cmd) imageViewerLoadedMsg {
	t.Helper()
	var result imageViewerLoadedMsg
	var run func(tea.Cmd)
	run = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if loaded, ok := msg.(imageViewerLoadedMsg); ok {
			result = loaded
			return
		}
		value := reflect.ValueOf(msg)
		if value.IsValid() && value.Kind() == reflect.Slice {
			for idx := 0; idx < value.Len(); idx++ {
				if child, ok := reflect.TypeAssert[tea.Cmd](value.Index(idx)); ok {
					run(child)
				}
			}
		}
	}
	run(command)
	if result.Generation == 0 {
		t.Fatal("no viewer load result")
	}
	return result
}

func TestInputImagePreviewObjectBindingAndSnapshotOrder(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	tokens := m.input.InlinePastes()
	parts, index := m.inputImageSnapshot(tokens[1])
	if index != 1 || len(parts) != 2 || parts[0].FileName != "second.png" || parts[1].FileName != "first.png" {
		t.Fatalf("snapshot = %#v, target = %d", parts, index)
	}
	if &parts[0].Data[0] != &m.attachments[2].Data[0] {
		t.Fatal("snapshot copied image payload")
	}
	m.input.SetValue("[sample.png] [image1]")
	if _, _, hit := m.inputImageAt(0, true); hit {
		t.Fatal("literal text bound to an image")
	}
}

func TestInputImagePreviewDoubleClickSameObjectDifferentCharacters(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	token := m.input.InlinePastes()[0]
	mouse := tea.Mouse{X: 10, Y: 20, Button: tea.MouseLeft}
	cmd, handled := m.handleInputImageClick(mouse, token.Start, true)
	if cmd != nil || !handled || m.input.SelectionText() != token.DisplayText {
		t.Fatal("single click should select object")
	}
	mouse.X += 6
	cmd, handled = m.handleInputImageClick(mouse, token.End-1, true)
	if !handled || !m.imageViewer.Open || m.input.HasSelection() || m.inputMouseDown {
		t.Fatal("double click should open viewer and stop selection")
	}
	result := previewCommandMessage(t, cmd)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	m.handleImageViewerLoaded(result)
	if m.imageViewer.currentPart().FileName != "second.png" {
		t.Fatal("wrong attachment")
	}
	m.attachments = append(m.attachments, Attachment{FileName: "third.png", MimeType: "image/png", InlineImagePlaceholder: true, Data: makeTestPNG(t)})
	m.input.InsertImagePlaceholder(3)
	current := m.input.Value()
	if len(m.imageViewer.Items) != 2 {
		t.Fatal("live admission changed preview snapshot")
	}
	cursor := m.imageViewer.Cursor
	m.handleImageViewerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModeInsert || !m.input.Focused() || m.input.HasSelection() || len(m.attachments) != 4 || m.input.Value() != current {
		t.Fatal("close lost current draft or focus")
	}
	if got := runeOffsetFromRowCol(m.input.Value(), m.input.Line(), m.input.Column()); got != cursor {
		t.Fatalf("cursor = %d, want %d", got, cursor)
	}
	if len(m.composerUndo.entries) != 0 {
		t.Fatal("preview entered undo history")
	}
}

func TestInputImagePreviewDistinctObjectsAndPadding(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	tokens := m.input.InlinePastes()
	mouse := tea.Mouse{X: 10, Y: 20, Button: tea.MouseLeft}
	m.handleInputImageClick(mouse, tokens[0].Start, true)
	m.handleInputImageClick(mouse, tokens[1].Start, true)
	if m.imageViewer.Open || m.input.SelectionText() != tokens[1].DisplayText {
		t.Fatal("distinct objects treated as double click")
	}
	m.handleInputImageClick(mouse, tokens[1].End-1, false)
	if !m.inputImageClick.Time.IsZero() {
		t.Fatal("padding retained image click identity")
	}
	m.handleInputImageClick(mouse, tokens[1].Start, true)
	if m.imageViewer.Open {
		t.Fatal("padding finished a double click")
	}
}

func TestInputImagePreviewEditInvalidatesDoubleClick(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	token := m.input.InlinePastes()[0]
	mouse := tea.Mouse{Button: tea.MouseLeft}
	m.handleInputImageClick(mouse, token.Start, true)
	m.input.ClearSelection()
	m.input.CursorEnd()
	m.input.InsertStringPreserveInlinePastes(" changed")
	m.handleInputImageClick(mouse, token.Start, true)
	if m.imageViewer.Open {
		t.Fatal("edit did not reset object click")
	}
}

func TestInputImagePreviewUnsupportedKeepsSelection(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendNone)
	token := m.input.InlinePastes()[0]
	mouse := tea.Mouse{Button: tea.MouseLeft}
	m.handleInputImageClick(mouse, token.Start, true)
	cmd, _ := m.handleInputImageClick(mouse, token.Start+1, true)
	if cmd == nil || m.imageViewer.Open || m.input.SelectionText() != token.DisplayText || m.mode != ModeInsert {
		t.Fatal("unsupported preview lost selection or failed to notify")
	}
	if cmd, _ := m.handleInputImageClick(mouse, token.Start+1, true); cmd != nil {
		t.Fatal("third click repeated unsupported toast")
	}
}

func TestImageViewerRejectsLateLoadResults(t *testing.T) {
	for _, action := range []string{"close", "step", "agent", "session", "overlay"} {
		t.Run(action, func(t *testing.T) {
			m := newInputImagePreviewModel(t, ImageBackendKitty)
			parts, _ := m.inputImageSnapshot(m.input.InlinePastes()[0])
			result := previewCommandMessage(t, m.openImageViewerItems(parts, 0, ModeInsert))
			switch action {
			case "close":
				m.closeImageViewer()
			case "step":
				m.stepImageViewer(1)
			case "agent":
				m.setFocusedAgent("worker")
			case "session":
				m.beginSessionSwitch("new", "")
			case "overlay":
				m.switchModeWithIME(ModeQuestion)
			}
			mode := m.mode
			if action == "agent" || action == "session" {
				if m.imageViewer.Open || m.mode == ModeImageViewer {
					t.Fatal("ownership switch did not dismiss viewer")
				}
			}
			if cmd := m.handleImageViewerLoaded(result); cmd != nil {
				t.Fatal("stale result dispatched image protocol")
			}
			if m.mode != mode || m.imageViewer.Prepared != nil {
				t.Fatal("stale result restored viewer state")
			}
		})
	}
}

func TestImageViewerLoadFailurePreservesDraftAndRetry(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendITerm2)
	path := filepath.Join(t.TempDir(), "sample.png")
	m.attachments[2].Data, m.attachments[2].ImagePath = nil, path
	before := m.input.Value()
	parts, _ := m.inputImageSnapshot(m.input.InlinePastes()[0])
	result := previewCommandMessage(t, m.openImageViewerItems(parts, 0, ModeInsert))
	if result.Err == nil {
		t.Fatal("missing path should fail")
	}
	m.handleImageViewerLoaded(result)
	errorOverlay := stripANSI(m.renderImageViewerOverlay())
	if !strings.Contains(errorOverlay, "read image file") || m.input.Value() != before || len(m.attachments) != 3 {
		t.Fatal("load error lost draft or did not display error")
	}
	if !strings.Contains(errorOverlay, "[Esc] close") || !strings.Contains(errorOverlay, "[r] retry") {
		t.Fatalf("load error should advertise retry as key chips:\n%s", errorOverlay)
	}
	if err := os.WriteFile(path, makeTestPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	result = previewCommandMessage(t, m.prepareImageViewer())
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	protocol := m.handleImageViewerLoaded(result)
	if protocol == nil {
		t.Fatal("retry did not prepare image")
	}
	if len(m.attachments[2].Data) != 0 || m.attachments[2].ImagePath != path {
		t.Fatal("viewer modified path-backed attachment")
	}
	if !strings.Contains(fmt.Sprint(protocol().(tea.RawMsg).Msg), deferredImageSequencePrefix) {
		t.Fatal("iTerm2 payload must draw after frame")
	}
}

func TestImageViewerRenderAndProtocolUsePreparedDataOnly(t *testing.T) {
	for _, backend := range []ImageBackend{ImageBackendKitty, ImageBackendITerm2} {
		t.Run(backend.String(), func(t *testing.T) {
			m := newInputImagePreviewModel(t, backend)
			parts, _ := m.inputImageSnapshot(m.input.InlinePastes()[0])
			m.openImageViewerItems(parts, 0, ModeInsert)
			finishImageViewerLoad(t, m)
			read, decode, encode := imageCacheReadFile, imageCacheDecode, imageCacheEncodePNG
			t.Cleanup(func() { imageCacheReadFile, imageCacheDecode, imageCacheEncodePNG = read, decode, encode })
			imageCacheReadFile = func(string) ([]byte, error) { t.Fatal("read on UI loop"); return nil, nil }
			imageCacheDecode = func(io.Reader) (image.Image, string, error) { t.Fatal("decode on UI loop"); return nil, "", nil }
			imageCacheEncodePNG = func(io.Writer, image.Image) error { t.Fatal("encode on UI loop"); return nil }
			imageRuntimeCache.mu.Lock()
			imageRuntimeCache.entries = make(map[string]*imageRuntimeCacheEntry)
			imageRuntimeCache.mu.Unlock()
			m.renderImageViewerOverlay()
			if m.imageViewerProtocolCmd() == nil {
				t.Fatal("eviction lost prepared image")
			}
			m.width += 20
			m.layout = m.generateLayout(m.width, m.height)
			if m.imageViewerProtocolCmd() == nil || !m.imageViewer.Loading {
				t.Fatal("resize must request asynchronous variant")
			}
			m.closeImageViewer()
		})
	}
}

func TestImageViewerCancelledPreparationDoesNotRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := prepareImageViewerPart(ctx, BlockImagePart{Data: []byte("invalid"), MimeType: "image/png"}, 20, 10, kittyTerminalMetrics{}, ImageBackendKitty, 1)
	if err == nil {
		t.Fatal("cancelled preparation should fail")
	}
}

func TestInputImagePreviewWrappedHitAndDrag(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	m.input.SetWidth(8)
	m.input.syncHeight()
	token := m.input.InlinePastes()[0]
	rows := m.input.selectionDisplayRows()
	hitCount := 0
	for y, row := range rows {
		for x := 0; x < len([]rune(row.text)); x++ {
			offset, hit := m.input.RunePositionAt(inputPromptWidth+x, y)
			p, _, found := m.inputImageAt(offset, hit)
			if found && p == token {
				hitCount++
			}
		}
		if _, hit := m.input.RunePositionAt(inputPromptWidth+len([]rune(row.text)), y); hit {
			t.Fatal("padding was an object character")
		}
	}
	if hitCount != len([]rune(token.DisplayText)) {
		t.Fatalf("visible token hits = %d, want %d", hitCount, len([]rune(token.DisplayText)))
	}
	m.handleInputImageClick(tea.Mouse{Button: tea.MouseLeft}, token.Start+1, true)
	m.input.UpdateSelection(token.End + 3)
	start, end, _ := m.input.SelectionRange()
	if start != token.Start || end != token.End+3 {
		t.Fatalf("drag range = [%d,%d)", start, end)
	}
}

func TestInputImagePreviewMouseRoutingConsumesCloseClick(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	token := m.input.InlinePastes()[0]
	click := tea.MouseClickMsg{X: m.layout.input.Min.X + inputPromptWidth + token.Start, Y: m.layout.input.Min.Y + 1, Button: tea.MouseLeft}
	m.Update(click)
	click.X += 3
	_, cmd := m.Update(click)
	if !m.imageViewer.Open {
		t.Fatalf("mouse double click did not open image: mode=%v layout=%v click=%+v identity=%+v value=%q", m.mode, m.layout.input, click, m.inputImageClick, m.input.Value())
	}
	result := previewCommandMessage(t, cmd)
	m.Update(result)
	before := m.input.Value()
	rect, _ := m.imageViewerOverlayRect()
	m.Update(tea.MouseClickMsg{X: rect.Min.X - 1, Y: rect.Min.Y, Button: tea.MouseLeft})
	if m.imageViewer.Open || m.mode != ModeInsert || m.input.Value() != before || !m.input.Focused() {
		t.Fatal("close click fell through or lost draft")
	}
}

func TestImageViewerTranscriptNavigationKeepsOpeningSnapshot(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	block := &Block{ID: 5, Type: BlockUser, ImageCount: 2, ImageParts: []BlockImagePart{
		{FileName: "a.png", MimeType: "image/png", Data: makeTestPNG(t)},
		{FileName: "b.png", MimeType: "image/png", Data: makeTestPNG(t)},
	}}
	m.viewport.AppendBlock(block)
	m.openImageViewer(block.ID, 0)
	finishImageViewerLoad(t, m)
	block.ImageParts = nil
	result := previewCommandMessage(t, m.stepImageViewer(1))
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	m.handleImageViewerLoaded(result)
	if m.imageViewer.currentPart().FileName != "b.png" {
		t.Fatal("navigation reparsed live block")
	}
	m.closeImageViewer()
	if m.mode != ModeNormal {
		t.Fatal("transcript preview returned to input")
	}
}

func TestImageViewerSlowReadDoesNotBlockClose(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendITerm2)
	path := filepath.Join(t.TempDir(), "slow.png")
	if err := os.WriteFile(path, makeTestPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	original := imageCacheReadFile
	started, release := make(chan struct{}), make(chan struct{})
	imageCacheReadFile = func(file string) ([]byte, error) {
		close(started)
		<-release
		return original(file)
	}
	t.Cleanup(func() { imageCacheReadFile = original })
	command := m.openImageViewerItems([]BlockImagePart{{ImagePath: path, MimeType: "image/png"}}, 0, ModeInsert)
	finished := make(chan tea.Msg, 1)
	go func() { finished <- command() }()
	<-started
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.imageViewer.Open || m.mode != ModeInsert {
		t.Fatal("slow read blocked close")
	}
	close(release)
	if msg := <-finished; msg != nil {
		t.Fatalf("cancelled read returned %T", msg)
	}
}

func TestImageViewerITerm2DeferredCursorAndClearOnClose(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendITerm2)
	parts, _ := m.inputImageSnapshot(m.input.InlinePastes()[0])
	result := previewCommandMessage(t, m.openImageViewerItems(parts, 0, ModeInsert))
	command := m.handleImageViewerLoaded(result)
	raw := command().(tea.RawMsg)
	out := &fakeTerminalImageOut{}
	wrapped := WrapTerminalImageOutput(out)
	if _, err := wrapped.Write(fmt.Append(nil, raw.Msg)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("iTerm2 image or cursor drew before overlay frame")
	}
	if _, err := wrapped.Write([]byte("FRAME")); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.HasPrefix(text, "FRAME\x1b7") || !strings.Contains(text, "]1337;") || !strings.HasSuffix(text, "\x1b8") {
		t.Fatalf("unexpected deferred order: %q", text)
	}
	closeMsg := m.closeImageViewer()()
	value := reflect.ValueOf(closeMsg)
	if value.Kind() != reflect.Slice {
		if reflect.TypeOf(closeMsg) != reflect.TypeOf(tea.ClearScreen()) {
			t.Fatalf("close result = %T, want clear screen", closeMsg)
		}
		return
	}
	first, ok := reflect.TypeAssert[tea.Cmd](value.Index(0))
	if !ok {
		t.Fatal("missing cleanup command")
	}
	if got, want := reflect.TypeOf(first()), reflect.TypeOf(tea.ClearScreen()); got != want {
		t.Fatalf("close starts with %v, want %v", got, want)
	}
}

func TestImageViewerCorruptImageStaysClosable(t *testing.T) {
	m := newInputImagePreviewModel(t, ImageBackendKitty)
	parts := []BlockImagePart{{FileName: "sample.png", MimeType: "image/png", Data: []byte("invalid image")}}
	result := previewCommandMessage(t, m.openImageViewerItems(parts, 0, ModeInsert))
	if result.Err == nil {
		t.Fatal("corrupt image should fail")
	}
	m.Update(result)
	if !m.imageViewer.Open || m.imageViewer.Error == "" {
		t.Fatal("corrupt image did not leave a closable error overlay")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mode != ModeInsert || !m.input.Focused() {
		t.Fatal("failed preview did not return to composer")
	}
}
