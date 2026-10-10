package tui

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
)

func BenchmarkModelViewFileImages(b *testing.B) {
	resetImageRuntimeCache()
	b.Cleanup(resetImageRuntimeCache)
	caps := TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsInline: true}
	previous := currentImageCapabilities()
	setCurrentTerminalImageCapabilities(caps)
	b.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 300, 200)), nil); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "image.jpg")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		b.Fatal(err)
	}
	m := NewModelWithSize(nil, 80, 40)
	b.Cleanup(func() { _ = m.Close() })
	m.mode, m.imageCaps = ModeNormal, caps
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: "image.jpg", ImagePath: path, MimeType: "image/jpeg"}}})
	cmd := m.imageProtocolCmd()
	for steps := 0; cmd != nil; steps++ {
		if steps > 8 {
			b.Fatal("image preparation did not settle")
		}
		switch msg := cmd().(type) {
		case inlineImagesLoadedMsg:
			cmd = m.handleInlineImagesLoaded(msg)
		case tea.RawMsg:
			cmd = nil
		default:
			b.Fatalf("unexpected command: %T", msg)
		}
	}
	_ = m.View()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
		_, _ = m.inlineImageRequests()
	}
}
