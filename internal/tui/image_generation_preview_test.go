package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestGeneratedImageResultRendersAllThumbnailsWithoutOpeningViewer(t *testing.T) {
	for _, tc := range []struct {
		name                string
		backend             ImageBackend
		started, fileBacked bool
	}{
		{"result-only", ImageBackendKitty, false, false},
		{"started-inline", ImageBackendKitty, true, false},
		{"started-file", ImageBackendKitty, true, true},
		{"started-iterm2", ImageBackendITerm2, true, true},
		{"result-only-iterm2", ImageBackendITerm2, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ApplyTheme(DefaultTheme())
			caps := TerminalImageCapabilities{Backend: tc.backend, SupportsInline: true, SupportsFullscreen: true}
			previous := currentImageCapabilities()
			setCurrentTerminalImageCapabilities(caps)
			t.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
			m := NewModelWithSize(nil, 80, 100)
			m.imageCaps = caps
			m.mode = ModeNormal
			data := makeTestPNG(t)
			path := filepath.Join(t.TempDir(), "image.png")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			parts := []message.ContentPart{{Type: message.ContentPartText, Text: "All originals saved"}}
			for i := range imagegen.MaxImages {
				part := message.ContentPart{Type: message.ContentPartImage, ArtifactID: fmt.Sprintf("sha256-image-%d", i), FileName: fmt.Sprintf("image-%d.png", i), MimeType: "image/png", Data: data}
				if tc.fileBacked {
					part.Data, part.ImagePath = nil, path
				}
				parts = append(parts, part)
			}
			if tc.started {
				m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "image-call", Name: tools.NameGenerateImage, ArgsJSON: `{"operation":"generate","prompt":"A landscape"}`})
			}
			cmd := m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{Name: tools.NameGenerateImage, CallID: "image-call", Status: agent.ToolResultStatusSuccess, Result: `{"status":"saved","details":"Image metadata"}`, Parts: parts}})
			blocks := m.viewport.visibleBlocks()
			if len(blocks) != 1 {
				t.Fatalf("tool cards=%d, want 1", len(blocks))
			}
			block := blocks[0]
			if block.Collapsed || block.ToggleAtWidth(80) {
				t.Fatal("generated image card should stay visible without a detail toggle")
			}
			if tc.started && block.Type != BlockToolCall {
				t.Fatal("terminal result replaced the started tool card")
			}
			if len(block.ImageParts) != imagegen.MaxImages {
				t.Fatalf("thumbnails=%d", len(block.ImageParts))
			}
			rendered := strings.Join(block.Render(80, ""), "\n")
			if strings.Contains(rendered, "Image metadata") {
				t.Fatal("image card exposed result JSON")
			}
			end := -1
			for i, part := range block.ImageParts {
				if !strings.Contains(rendered, fmt.Sprintf("image-%d.png", i)) || part.RenderRows <= 0 || part.RenderStartLine <= end {
					t.Fatalf("image %d missing or overlapping: %+v", i, part)
				}
				end = part.RenderEndLine
			}
			if m.mode != ModeNormal || m.imageViewer.Open {
				t.Fatal("tool result opened the image viewer automatically")
			}
			out := &fakeTerminalImageOut{}
			wrapped := WrapTerminalImageOutput(out)
			rawCommands := 0
			var run func(tea.Cmd)
			run = func(command tea.Cmd) {
				if command == nil {
					return
				}
				switch msg := command().(type) {
				case tea.BatchMsg:
					for _, child := range msg {
						run(child)
					}
				case streamFlushTickMsg:
					run(m.handleStreamFlushTick(msg))
				case inlineImagesLoadedMsg:
					run(m.handleInlineImagesLoaded(msg))
				case tea.RawMsg:
					rawCommands++
					if _, err := wrapped.Write(fmt.Append(nil, msg.Msg)); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Consume only commands returned by the tool event, without model
			// output, scrolling, or manually requesting image transmission.
			run(cmd)
			if !m.viewport.HasVisibleInlineImage() || rawCommands == 0 {
				t.Fatal("visible thumbnails did not produce terminal image output")
			}
			if m.viewport.offset != 0 {
				t.Fatalf("image dispatch required scrolling: offset=%d", m.viewport.offset)
			}
			if tc.backend == ImageBackendITerm2 && out.Len() != 0 {
				t.Fatal("iTerm2 image output preceded its frame")
			}
			if _, err := wrapped.Write([]byte("FRAME")); err != nil {
				t.Fatal(err)
			}
			imageMarker := "\x1b_G"
			if tc.backend == ImageBackendITerm2 {
				imageMarker = "\x1b]1337;File="
			}
			if !strings.Contains(out.String(), imageMarker) {
				t.Fatal("tool event did not deliver images to the terminal output")
			}
			if tc.backend == ImageBackendKitty && !strings.Contains(m.lastImageProtocolSummary, "kitty_visible_parts=5") {
				t.Fatalf("terminal output omitted thumbnails: %s", m.lastImageProtocolSummary)
			}
			// Selecting another thumbnail uses the existing viewer and full gallery.
			m.openImageViewer(block.ID, imagegen.MaxImages-1)
			t.Cleanup(func() { m.dismissImageViewer() })
			if !m.imageViewer.Open || m.imageViewer.Index != imagegen.MaxImages-1 || len(m.imageViewer.Items) != imagegen.MaxImages {
				t.Fatal("viewer did not select the requested original from the gallery")
			}
		})
	}
}
