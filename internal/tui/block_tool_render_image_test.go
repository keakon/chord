package tui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestImageToolCardsShowSingleLinePrompt(t *testing.T) {
	ApplyTheme(DefaultTheme())
	previous := currentImageCapabilities()
	setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	t.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
	const prompt = "A small tree beside a quiet river under soft morning light with distant hills and scattered clouds in the background.\nUse a simple background with muted colors."
	for _, operation := range []string{imagegen.Generate, imagegen.Edit} {
		args, err := json.Marshal(map[string]string{"operation": operation, "prompt": prompt})
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []BlockType{BlockToolCall, BlockToolResult} {
			for _, tc := range []struct {
				name, result, recovery string
				status                 agent.ToolResultStatus
				done                   bool
			}{
				{name: "running"},
				{name: "success", result: `{}`, status: agent.ToolResultStatusSuccess, done: true},
				{name: "error", result: "Image request failed", status: agent.ToolResultStatusError, done: true},
				{name: "cancelled", result: "Cancelled", status: agent.ToolResultStatusCancelled, done: true},
				{name: "unknown", result: "Do not replay generation", recovery: message.ToolRecoveryStateOutcomeUnknown, status: agent.ToolResultStatusError, done: true},
			} {
				for _, width := range []int{40, 100} {
					t.Run(fmt.Sprintf("%s/%d/%s/%d", operation, kind, tc.name, width), func(t *testing.T) {
						block := &Block{Type: kind, ToolName: tools.NameGenerateImage, Content: string(args), ResultContent: tc.result, ResultStatus: tc.status, ResultDone: tc.done, RecoveryState: tc.recovery}
						initToolCardFoldState(block, block.ToolName)
						lines := block.Render(width, "")
						plain := stripANSI(strings.Join(lines, "\n"))
						if strings.Contains(plain, "Prompt:") || strings.Contains(plain, "muted colors") {
							t.Fatalf("image prompt expanded into the body: %s", plain)
						}
						if !strings.Contains(plain, "generate_image A small") || strings.Count(plain, "A small") != 1 || !strings.Contains(plain, "…") {
							t.Fatalf("image prompt missing or not truncated in the header: %s", plain)
						}
						for _, line := range lines {
							if ansi.StringWidth(line) > width {
								t.Fatalf("image card exceeds width %d: %q", width, stripANSI(line))
							}
						}
						if block.Content != string(args) || block.ResultContent != tc.result {
							t.Fatal("presentation rewrote the request or result")
						}
					})
				}
			}
		}
	}
}

func TestImageToolCardYankCopiesCompleteArguments(t *testing.T) {
	previousWrite := clipboardWriteAll
	var copied string
	clipboardWriteAll = func(text string) error {
		copied = text
		return nil
	}
	t.Cleanup(func() { clipboardWriteAll = previousWrite })
	args, err := json.Marshal(map[string]string{
		"operation": imagegen.Generate,
		"prompt":    "A small tree beside a quiet river with distant hills.\nUse muted colors and soft light.",
		"size":      "1024x1024",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, done := range []bool{false, true} {
		t.Run(fmt.Sprintf("done=%t", done), func(t *testing.T) {
			m := NewModelWithSize(nil, 40, 24)
			m.mode = ModeNormal
			block := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameGenerateImage, Content: string(args), ResultDone: done}
			if done {
				block.ResultContent = `{}`
				block.ResultStatus = agent.ToolResultStatusSuccess
			}
			m.viewport.AppendBlock(block)
			m.focusedBlockID = 1
			m.refreshBlockFocus()
			block.Render(40, "")
			key := tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'})
			m.handleNormalKey(key)
			cmd := m.handleNormalKey(key)
			if cmd == nil {
				t.Fatal("yy did not return a clipboard command")
			}
			sequence := reflect.ValueOf(cmd())
			if sequence.Kind() != reflect.Slice || sequence.Len() != 2 {
				t.Fatal("yy did not return the clipboard sequence")
			}
			sequence.Index(1).Call(nil)
			if !strings.Contains(copied, "## Arguments\n\n```json\n"+string(args)+"\n```") {
				t.Fatalf("yy lost full image arguments: %q", copied)
			}
		})
	}
}

func TestImageToolCardsKeepWarningsAndOutcomesVisible(t *testing.T) {
	ApplyTheme(DefaultTheme())
	previous := currentImageCapabilities()
	setCurrentTerminalImageCapabilities(TerminalImageCapabilities{Backend: ImageBackendNone})
	t.Cleanup(func() { setCurrentTerminalImageCapabilities(previous) })
	for _, tool := range []string{tools.NameGenerateImage, tools.NameViewImage} {
		for _, kind := range []BlockType{BlockToolCall, BlockToolResult} {
			for _, tc := range []struct {
				name, content, recovery, want string
				status                        agent.ToolResultStatus
			}{
				{"error", "Error: Image could not be loaded.\nTry another file.", "", "Try another file.", agent.ToolResultStatusError},
				{"cancelled", "Cancelled by user", "", "Cancelled", agent.ToolResultStatusCancelled},
				{"unknown", "Request interrupted; do not replay generation", message.ToolRecoveryStateOutcomeUnknown, "do not replay generation", agent.ToolResultStatusError},
			} {
				t.Run(fmt.Sprintf("%s/%d/%s", tool, kind, tc.name), func(t *testing.T) {
					block := &Block{Type: kind, ToolName: tool, ResultDone: true, ResultContent: tc.content, Content: tc.content, ResultStatus: tc.status, RecoveryState: tc.recovery}
					initToolCardFoldState(block, tool)
					if block.ToggleAtWidth(40) {
						t.Fatal("image card offered a detail toggle")
					}
					plain := stripANSI(strings.Join(block.Render(80, ""), "\n"))
					if !strings.Contains(plain, tc.want) {
						t.Fatalf("image outcome hidden: %s", plain)
					}
					if tc.name == "unknown" && !strings.Contains(plain, "Result unknown") {
						t.Fatalf("unknown outcome presented as an ordinary error: %s", plain)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		tool, content, want, hidden string
	}{
		{tools.NameGenerateImage, `{"state":"completed","request_id":"request-1","warnings":["Some images were omitted."]}`, "Some images were omitted.", "request_id"},
		{tools.NameViewImage, `Loaded image "sample.png" into context (4000x3000 -> 2000x1500 (scaled to 2000px max edge)).`, "4000x3000 -> 2000x1500", "Loaded image"},
	} {
		t.Run(tc.tool+"/notice", func(t *testing.T) {
			block := &Block{Type: BlockToolCall, ToolName: tc.tool, ResultDone: true, ResultContent: tc.content, ResultStatus: agent.ToolResultStatusSuccess}
			initToolCardFoldState(block, tc.tool)
			plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
			if !strings.Contains(plain, tc.want) || strings.Contains(plain, tc.hidden) {
				t.Fatalf("image notice not presented correctly: %s", plain)
			}
			if !strings.Contains(plain, "Image preview unavailable") {
				t.Fatalf("missing preview was silently hidden: %s", plain)
			}
			if block.ResultContent != tc.content {
				t.Fatal("presentation rewrote the tool result")
			}
		})
	}
}
