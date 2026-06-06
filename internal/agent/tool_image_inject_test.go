package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestToolResultParts_OrderingAndShape(t *testing.T) {
	parts := []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("a"), FileName: "first.png"},
		{Type: "image", MimeType: "image/jpeg", Data: []byte("b"), FileName: "second.jpg"},
	}
	got := toolResultParts("Loaded 2 images", parts)

	if len(got) != 3 {
		t.Fatalf("parts len = %d, want 3 (1 text + 2 images)", len(got))
	}
	if got[0].Type != "text" {
		t.Fatalf("first part type = %q, want text", got[0].Type)
	}
	if !strings.Contains(got[0].Text, "2 image") {
		t.Fatalf("text part %q should mention the image count", got[0].Text)
	}
	if got[1].FileName != "first.png" || got[2].FileName != "second.jpg" {
		t.Fatalf("image parts out of order: %q, %q", got[1].FileName, got[2].FileName)
	}
	if got[1].Type != "image" || got[2].Type != "image" {
		t.Fatalf("image parts lost their type: %#v", got[1:])
	}
}

func TestToolResultParts_NoImages(t *testing.T) {
	if got := toolResultParts("text only", nil); got != nil {
		t.Fatalf("toolResultParts without images = %#v, want nil", got)
	}
}

func TestToolResultPartsForCapability_DropsUnsupportedImages(t *testing.T) {
	parts, dropped := toolResultPartsForCapability("Loaded 1 image", []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("a"), FileName: "image.png"},
	}, stubInputCapability{"image": false})

	if dropped.Images != 1 || dropped.PDFs != 0 {
		t.Fatalf("dropped = %#v, want one image", dropped)
	}
	if parts != nil {
		t.Fatalf("parts = %#v, want nil after unsupported image drop", parts)
	}
}

func TestToolResultPartsForCapability_UsesToolResultReplayCapability(t *testing.T) {
	parts, dropped := toolResultPartsForCapability("Loaded 1 image", []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("a"), FileName: "image.png"},
	}, stubToolResultCapability{stubInputCapability{"image": true}})

	if dropped.Images != 1 {
		t.Fatalf("dropped images = %d, want 1", dropped.Images)
	}
	if parts != nil {
		t.Fatalf("parts = %#v, want nil when tool-result replay is unsupported", parts)
	}
}

type stubToolResultCapability struct {
	stubInputCapability
}

func (s stubToolResultCapability) SupportsToolResultModalities([]string) bool { return false }
