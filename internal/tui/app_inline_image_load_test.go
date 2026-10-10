package tui

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/imageutil"
)

func inlineImageTestModel(t *testing.T, backend ImageBackend) Model {
	t.Helper()
	resetImageRuntimeCache()
	t.Cleanup(resetImageRuntimeCache)
	caps := TerminalImageCapabilities{Backend: backend, SupportsInline: true}
	previous := currentImageCapabilities()
	setCurrentTerminalImageCapabilities(caps)
	t.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 30, 20)), nil); err != nil {
		t.Fatal(err)
	}
	m := NewModelWithSize(nil, 80, 40)
	m.mode, m.imageCaps = ModeNormal, caps
	setCurrentTerminalImageCapabilities(caps)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: "sample.jpg", MimeType: "image/jpeg", Data: encoded.Bytes()}}})
	return m
}

func TestInlineTransportIsAsyncAndRejectsStaleSession(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	decode := imageCacheDecode
	defer func() { imageCacheDecode = decode }()
	called := false
	imageCacheDecode = func(data []byte) (image.Image, string, error) {
		called = true
		return decode(data)
	}
	cmd := m.imageProtocolCmd()
	if cmd == nil || called {
		t.Fatal("inline transport was constructed on the main loop")
	}
	msg := cmd().(inlineImagesLoadedMsg)
	if !called || msg.sequence == "" {
		t.Fatal("background command did not prepare the image")
	}
	m.sessionTranscriptEpoch++
	if next := m.handleInlineImagesLoaded(msg); next != nil {
		if _, ok := next().(tea.RawMsg); ok {
			t.Fatal("old session image was emitted")
		}
	}
	if len(m.kittyImageCache) != 0 || len(m.kittyPlacementCache) != 0 {
		t.Fatal("stale image was marked as transmitted")
	}
}

func TestInlineTransportWaitCanBeCancelled(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	for range 2 {
		release, err := imageutil.AcquireDecodeSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	cmd := m.imageProtocolCmd()
	if cmd == nil {
		t.Fatal("missing background preparation")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	m.cancelInlineImages()
	select {
	case msg := <-done:
		if m.handleInlineImagesLoaded(msg.(inlineImagesLoadedMsg)) != nil {
			t.Fatal("cancelled preparation emitted an image")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled image still waits for decode capacity")
	}
}

func TestInlineTransportDeduplicatesPendingPreparation(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendKitty)
	defer m.Close()
	cmd := m.imageProtocolCmd()
	generation := m.inlineImageGeneration
	if cmd == nil || m.imageProtocolCmd() != nil || m.inlineImageGeneration != generation {
		t.Fatal("unchanged viewport restarted pending preparation")
	}
	if next := m.handleInlineImagesLoaded(cmd().(inlineImagesLoadedMsg)); next == nil {
		t.Fatal("current preparation was discarded")
	}
	if m.imageProtocolCmd() != nil {
		t.Fatal("transmitted image was prepared again")
	}
}

func TestInlineTransportRejectsOldITermPosition(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendITerm2)
	defer m.Close()
	requests, _ := m.inlineImageRequests()
	// Terminal coordinates are one-based; the body offset already includes
	// the two-column placeholder margin used by layout and hit testing.
	if len(requests) != 1 || requests[0].col != m.ensureLayout().main.Min.X+imagePartBodyLeftColumn()+1 {
		t.Fatal("image transport is not aligned with the transcript thumbnail")
	}
	cmd := m.imageProtocolCmd()
	if cmd == nil {
		t.Fatal("missing background preparation")
	}
	msg := cmd().(inlineImagesLoadedMsg)
	m.viewport.offset++
	next := m.handleInlineImagesLoaded(msg)
	if next == nil {
		t.Fatal("new viewport was not scheduled")
	}
	loaded, ok := next().(inlineImagesLoadedMsg)
	if !ok || loaded.signature == msg.signature {
		t.Fatal("old screen position was emitted")
	}
	if send := m.handleInlineImagesLoaded(loaded); send == nil {
		t.Fatal("current screen position was not emitted")
	} else if _, ok := send().(tea.RawMsg); !ok {
		t.Fatal("current preparation was not sent to the terminal")
	}
}

func TestInlineTransportDoesNotDrawOverDialog(t *testing.T) {
	m := inlineImageTestModel(t, ImageBackendITerm2)
	defer m.Close()
	cmd := m.imageProtocolCmd()
	if cmd == nil {
		t.Fatal("missing background preparation")
	}
	msg := cmd().(inlineImagesLoadedMsg)
	m.openHelp()
	if m.handleInlineImagesLoaded(msg) != nil {
		t.Fatal("transcript image was emitted over a dialog")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mode != ModeNormal || !m.inlineImageLoading {
		t.Fatal("closing the dialog did not reschedule the transcript image")
	}
}
