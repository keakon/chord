package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

type stubInputCapability map[string]bool

func (s stubInputCapability) SupportsInput(modality string) bool { return s[modality] }

func TestFilterUnsupportedBinaryPartsForModelDropsOnlyUnsupportedParts(t *testing.T) {
	messages := []message.Message{
		{Role: "user", Content: "prompt", Parts: []message.ContentPart{
			{Type: "text", Text: "prompt"},
			{Type: "image", Data: []byte("image")},
			{Type: "pdf", Data: []byte("pdf")},
		}},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "view_image"}}},
		{Role: "tool", ToolCallID: "call-1", Content: "Loaded image"},
	}

	filtered, dropped := filterUnsupportedBinaryPartsForModel(messages, stubInputCapability{"pdf": true})
	if dropped.Images != 1 || dropped.PDFs != 0 {
		t.Fatalf("dropped = %+v, want one image", dropped)
	}
	if len(filtered) != len(messages) {
		t.Fatalf("filtered len = %d, want %d", len(filtered), len(messages))
	}
	parts := filtered[0].Parts
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "pdf" {
		t.Fatalf("filtered parts = %+v, want text+pdf", parts)
	}
	if got := filtered[1].ToolCalls[0].Name; got != "view_image" {
		t.Fatalf("historical tool call name = %q", got)
	}
}

func TestFilterUnsupportedBinaryPartsForModelDropsEmptySyntheticMessage(t *testing.T) {
	messages := []message.Message{{Role: "user", Parts: []message.ContentPart{{Type: "image", Data: []byte("image")}}}}

	filtered, dropped := filterUnsupportedBinaryPartsForModel(messages, stubInputCapability{})
	if dropped.Images != 1 {
		t.Fatalf("dropped images = %d, want 1", dropped.Images)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered len = %d, want empty", len(filtered))
	}
}
