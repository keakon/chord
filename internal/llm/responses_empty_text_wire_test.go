package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// assertNoEmptyTextBlock fails when the wire payload carries a text block with
// no text. Providers reject the whole request in that case ("Missing required
// parameter"), which is unreachable from a single bad history entry, so the
// converted input is checked as JSON instead of trusting the typed structs.
func assertNoEmptyTextBlock(t *testing.T, items []responsesInputItem) {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	for i, item := range decoded {
		for _, field := range []string{"content", "output"} {
			blocks, ok := item[field].([]any)
			if !ok {
				continue
			}
			for j, raw := range blocks {
				block, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := block["type"].(string)
				if blockType != "input_text" && blockType != "output_text" {
					continue
				}
				text, ok := block["text"].(string)
				if !ok || text == "" {
					t.Fatalf("input[%d].%s[%d] = %s, want non-empty text", i, field, j, raw)
				}
			}
		}
	}
}

func TestConvertMessagesToResponses_EmptyToolResultKeepsWireText(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "shell", Args: json.RawMessage(`{}`)}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "", ToolStatus: message.ToolStatusSuccess},
	}
	// Prompt-cache breakpoints are what turn a tool output into a content-block
	// array, so both modes have to stay wire-valid for an empty result.
	for _, cacheBreakpoints := range []bool{false, true} {
		items := convertMessagesToResponsesWithItemIDs("", msgs, false, false, cacheBreakpoints)
		assertNoEmptyTextBlock(t, items)
		raw, err := json.Marshal(items)
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		if !strings.Contains(string(raw), emptyWireTextPlaceholder) {
			t.Fatalf("cacheBreakpoints=%v: input=%s, want the empty result labeled", cacheBreakpoints, raw)
		}
	}
}

func TestConvertMessagesToResponses_EmptyUserMessageKeepsWireText(t *testing.T) {
	cases := map[string][]message.Message{
		"empty content": {{Role: message.RoleUser, Content: ""}},
		"unresolvable attachment": {{Role: message.RoleUser, Parts: []message.ContentPart{
			{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("not-base64-image")},
		}}},
	}
	for name, msgs := range cases {
		t.Run(name, func(t *testing.T) {
			for _, cacheBreakpoints := range []bool{false, true} {
				items := convertMessagesToResponsesWithItemIDs("", msgs, false, false, cacheBreakpoints)
				assertNoEmptyTextBlock(t, items)
			}
		})
	}
}

func TestConvertResponsesOutputItem_DropsEntriesWithoutText(t *testing.T) {
	refusal, ok := convertResponsesOutputItem(message.ResponsesOutputItem{
		Type: "message",
		Role: "assistant",
		Content: []message.ResponsesOutputContent{
			{Type: "output_text", Text: ""},
			{Type: "refusal", Refusal: "declined"},
		},
	}, false, false)
	if !ok {
		t.Fatal("refusal-only message item dropped, want kept")
	}
	blocks, _ := refusal.Content.([]responsesContentBlock)
	if len(blocks) != 1 || blocks[0].Refusal != "declined" {
		t.Fatalf("content=%+v, want only the refusal block", refusal.Content)
	}

	if _, ok := convertResponsesOutputItem(message.ResponsesOutputItem{
		Type:    "message",
		Role:    "assistant",
		Content: []message.ResponsesOutputContent{{Type: "output_text"}},
	}, false, false); ok {
		t.Fatal("message item with no text kept, want dropped")
	}

	reasoning, ok := convertResponsesOutputItem(message.ResponsesOutputItem{
		Type:             "reasoning",
		EncryptedContent: "encrypted",
		Content:          []message.ResponsesOutputContent{{Type: "reasoning_text"}},
		Summary:          []message.ResponsesReasoningSummary{{Type: "summary_text"}},
	}, false, false)
	if !ok {
		t.Fatal("reasoning item with encrypted content dropped, want kept")
	}
	if blocks, _ := reasoning.Content.([]responsesReasoningContentBlock); len(blocks) != 0 {
		t.Fatalf("reasoning content=%+v, want the empty reasoning_text entry dropped", reasoning.Content)
	}
	if reasoning.Summary == nil || len(*reasoning.Summary) != 0 {
		t.Fatalf("summary=%+v, want the empty summary entry dropped", reasoning.Summary)
	}
}
