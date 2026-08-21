package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

type stubInputCapability map[string]bool

func (s stubInputCapability) SupportsInput(modality string) bool { return s[modality] }

func TestToastGateDeduplicatesPerModelCategorySummary(t *testing.T) {
	var g toastGate

	// First toast for a (model, category, summary) key is emitted.
	if !g.first("model-a", toastCategoryInput, "image") {
		t.Fatalf("first input image toast for model-a should emit")
	}
	// Same key repeats are suppressed.
	if g.first("model-a", toastCategoryInput, "image") {
		t.Fatalf("repeated input image toast for model-a should be suppressed")
	}
	// Different summary (modality) still emits.
	if !g.first("model-a", toastCategoryInput, "PDF") {
		t.Fatalf("input PDF toast for model-a should emit")
	}
	// Different category still emits.
	if !g.first("model-a", toastCategoryToolResult, "image") {
		t.Fatalf("tool-result image toast for model-a should emit")
	}
	// Different model still emits.
	if !g.first("model-b", toastCategoryInput, "image") {
		t.Fatalf("input image toast for model-b should emit")
	}
	// Same model+category+summary as model-b is suppressed again.
	if g.first("model-b", toastCategoryInput, "image") {
		t.Fatalf("repeated input image toast for model-b should be suppressed")
	}
}

func TestToastGateFallsBackToPlaceholderForEmptyModelName(t *testing.T) {
	var g toastGate

	if !g.first("", toastCategoryInput, "image") {
		t.Fatalf("first toast with empty model name should emit")
	}
	if g.first("", toastCategoryInput, "image") {
		t.Fatalf("repeated toast with empty model name should be suppressed")
	}
}

func TestDroppedSummaryNormalizes(t *testing.T) {
	if got := droppedSummary(nil); got != "" {
		t.Fatalf("droppedSummary(nil) = %q, want empty", got)
	}
	if got := droppedSummary([]string{"image"}); got != "image" {
		t.Fatalf("droppedSummary([image]) = %q, want image", got)
	}
	if got := droppedSummary([]string{"image", "pdf"}); got != "image/PDF" {
		t.Fatalf("droppedSummary([image pdf]) = %q, want image/PDF", got)
	}
	if got := droppedSummary([]string{"image", "image", "pdf"}); got != "image/PDF" {
		t.Fatalf("droppedSummary(duplicates) = %q, want image/PDF", got)
	}
	if got := droppedSummary([]string{"unknown"}); got != "" {
		t.Fatalf("droppedSummary([unknown]) = %q, want empty", got)
	}
}

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
