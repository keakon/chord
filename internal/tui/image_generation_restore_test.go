package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSessionRestoredToolImagesRenderAndTransmit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolName   string
		backend    ImageBackend
		fileBacked bool
	}{
		{"generate-kitty-inline", tools.NameGenerateImage, ImageBackendKitty, false},
		{"view-kitty-inline", tools.NameViewImage, ImageBackendKitty, false},
		{"generate-kitty-file", tools.NameGenerateImage, ImageBackendKitty, true},
		{"view-kitty-file", tools.NameViewImage, ImageBackendKitty, true},
		{"generate-iterm2-inline", tools.NameGenerateImage, ImageBackendITerm2, false},
		{"view-iterm2-inline", tools.NameViewImage, ImageBackendITerm2, false},
		{"generate-iterm2-file", tools.NameGenerateImage, ImageBackendITerm2, true},
		{"view-iterm2-file", tools.NameViewImage, ImageBackendITerm2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubTUITicks(t)
			ApplyTheme(DefaultTheme())
			caps := TerminalImageCapabilities{Backend: tc.backend, SupportsInline: true, SupportsFullscreen: true}
			previous := currentImageCapabilities()
			setCurrentTerminalImageCapabilities(caps)
			t.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
			data := makeTestPNG(t)
			path := filepath.Join(t.TempDir(), "image.png")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			parts := []message.ContentPart{{Type: message.ContentPartText, Text: "Images saved"}}
			for i := range 2 {
				part := message.ContentPart{Type: message.ContentPartImage, FileName: fmt.Sprintf("image-%d.png", i), MimeType: "image/png", Data: data}
				if tc.fileBacked {
					part.Data, part.ImagePath = nil, path
				}
				parts = append(parts, part)
			}
			argsJSON := `{"operation":"generate","prompt":"A landscape"}`
			resultContent := `{"status":"success","images":2}`
			if tc.toolName == tools.NameViewImage {
				argsJSON = `{"path":"sample.png"}`
				resultContent = "Loaded image into context."
			}
			backend := &sessionControlAgent{messages: []message.Message{
				{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "image-call", Name: tc.toolName, Args: []byte(argsJSON)}}},
				{Role: "tool", ToolCallID: "image-call", Content: resultContent, Parts: parts, ToolStatus: message.ToolStatusSuccess},
			}}
			m := NewModelWithSize(backend, 80, 100)
			m.imageCaps = caps
			out := &fakeTerminalImageOut{}
			wrapped := WrapTerminalImageOutput(out)
			rawCommands := 0
			var run func(tea.Cmd)
			run = func(cmd tea.Cmd) {
				if cmd == nil {
					return
				}
				switch msg := cmd().(type) {
				case tea.BatchMsg:
					for _, child := range msg {
						run(child)
					}
				case sessionRestoredRebuildMsg:
					_, next := m.Update(msg)
					run(next)
				case inlineImagesLoadedMsg:
					run(m.handleInlineImagesLoaded(msg))
				case tea.RawMsg:
					rawCommands++
					if _, err := wrapped.Write(fmt.Append(nil, msg.Msg)); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Only consume the restore event's commands: no scrolling, model
			// output, or manually requested image protocol transmission.
			run(m.handleAgentEvent(agentEventMsg{event: agent.SessionRestoredEvent{}}))
			blocks := m.viewport.visibleBlocks()
			if len(blocks) != 1 {
				t.Fatalf("restored cards=%d, want 1", len(blocks))
			}
			block := blocks[0]
			if len(block.ImageParts) != 2 {
				t.Fatalf("restored images=%d, want 2", len(block.ImageParts))
			}
			if block.Collapsed || block.ToggleAtWidth(80) || !block.ResultDone {
				t.Fatal("restored image card should be complete without a detail toggle")
			}
			rendered := strings.Join(block.Render(80, ""), "\n")
			if strings.Contains(rendered, resultContent) {
				t.Fatal("restored image card exposed its raw tool result")
			}
			end := -1
			for i, part := range block.ImageParts {
				if !strings.Contains(rendered, fmt.Sprintf("image-%d.png", i)) || part.RenderRows <= 0 || part.RenderStartLine <= end {
					t.Fatalf("restored image %d missing or overlapping: %+v", i, part)
				}
				end = part.RenderEndLine
			}
			if !m.viewport.HasVisibleInlineImage() || rawCommands == 0 {
				t.Fatal("restore did not schedule visible image output")
			}
			if tc.backend == ImageBackendITerm2 && out.Len() != 0 {
				t.Fatal("iTerm2 image output preceded its frame")
			}
			if _, err := wrapped.Write([]byte("FRAME")); err != nil {
				t.Fatal(err)
			}
			marker := "\x1b_G"
			if tc.backend == ImageBackendITerm2 {
				marker = "\x1b]1337;File="
			}
			if !strings.Contains(out.String(), marker) {
				t.Fatal("restore did not transmit images to the terminal")
			}
			if m.imageViewer.Open {
				t.Fatal("restore opened the viewer automatically")
			}
			m.openImageViewer(block.ID, 1)
			t.Cleanup(func() { m.dismissImageViewer() })
			if !m.imageViewer.Open || m.imageViewer.Index != 1 || len(m.imageViewer.Items) != 2 {
				t.Fatal("restored gallery did not retain both originals")
			}
		})
	}
}
