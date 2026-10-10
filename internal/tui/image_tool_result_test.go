package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestViewImageResultDisplaysImageAfterTextOnlyPreview(t *testing.T) {
	for _, backend := range []ImageBackend{ImageBackendKitty, ImageBackendITerm2} {
		t.Run(fmt.Sprint(backend), func(t *testing.T) {
			ApplyTheme(DefaultTheme())
			caps := TerminalImageCapabilities{Backend: backend, SupportsInline: true, SupportsFullscreen: true}
			previous := currentImageCapabilities()
			setCurrentTerminalImageCapabilities(caps)
			t.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
			path := filepath.Join(t.TempDir(), "sample.png")
			if err := os.WriteFile(path, makeTestPNG(t), 0600); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]string{"path": path})
			sink := &tools.ImageCollector{}
			result, err := tools.NewViewImageTool(nil).Execute(tools.WithImageSink(t.Context(), sink), args)
			if err != nil {
				t.Fatal(err)
			}
			m := NewModelWithSize(nil, 80, 100)
			m.imageCaps = caps
			m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "image-call", Name: tools.NameViewImage, ArgsJSON: string(args)})
			event := agent.ToolResultEvent{Name: tools.NameViewImage, CallID: "image-call", Result: result, Status: agent.ToolResultStatusSuccess}
			m.handleAgentEvent(agentEventMsg{event: event})
			event.Parts = sink.Drain()
			out := &fakeTerminalImageOut{}
			wrapped := WrapTerminalImageOutput(out)
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
				case streamFlushTickMsg:
					run(m.handleStreamFlushTick(msg))
				case tea.RawMsg:
					if _, err := wrapped.Write(fmt.Append(nil, msg.Msg)); err != nil {
						t.Fatal(err)
					}
				}
			}
			run(m.handleAgentEvent(agentEventMsg{event: event}))
			blocks := m.viewport.visibleBlocks()
			if len(blocks) != 1 || len(blocks[0].ImageParts) != 1 {
				t.Fatal("full result did not attach the image to the existing card")
			}
			block := blocks[0]
			if block.Collapsed || block.ToggleAtWidth(80) || !m.viewport.HasVisibleInlineImage() || block.ImageParts[0].RenderRows <= 0 {
				t.Fatal("view_image card did not keep its image visible")
			}
			if strings.Contains(strings.Join(block.Render(80, ""), "\n"), "Loaded image") {
				t.Fatal("view_image card repeated the load confirmation")
			}
			if block.ResultContent != result {
				t.Fatal("image presentation changed the raw tool result")
			}
			if _, err := wrapped.Write([]byte("FRAME")); err != nil {
				t.Fatal(err)
			}
			marker := "\x1b_G"
			if backend == ImageBackendITerm2 {
				marker = "\x1b]1337;File="
			}
			if !strings.Contains(out.String(), marker) {
				t.Fatal("full result did not transmit the image")
			}
			m.mode = ModeNormal
			m.focusedBlockID = block.ID
			if m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace})) == nil {
				t.Fatal("space on the image tool card did not open its viewer")
			}
			t.Cleanup(func() { m.dismissImageViewer() })
			if !m.imageViewer.Open || len(m.imageViewer.Items) != 1 {
				t.Fatal("view_image result did not open its original")
			}
		})
	}
}
