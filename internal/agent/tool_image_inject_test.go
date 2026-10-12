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

func TestUnsupportedToolResultParts_CountsUnsupportedImages(t *testing.T) {
	images := []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("a"), FileName: "image.png"},
		{Type: "pdf", MimeType: "application/pdf", Data: []byte("%PDF"), FileName: "doc.pdf"},
	}

	if dropped := unsupportedToolResultParts(images, stubInputCapability{"image": false, "pdf": true}); dropped.Images != 1 || dropped.PDFs != 0 {
		t.Fatalf("dropped = %#v, want one image", dropped)
	}
	if dropped := unsupportedToolResultParts(images, stubInputCapability{"image": false, "pdf": false}); dropped.Images != 1 || dropped.PDFs != 1 {
		t.Fatalf("dropped = %#v, want one image and one PDF", dropped)
	}
	// Counting is for the toast only: the parts themselves are never dropped.
	if parts := toolResultParts("Loaded 1 image", images); len(parts) != 3 {
		t.Fatalf("parts len = %d, want 3 (text + image + PDF)", len(parts))
	}
}

func TestUnsupportedToolResultParts_UsesToolResultReplayCapability(t *testing.T) {
	images := []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("a"), FileName: "image.png"},
	}

	// Input modality is supported, but tool-result replay is not: the image is
	// still counted (and still kept — the request wire strips it per target).
	if dropped := unsupportedToolResultParts(images, stubToolResultCapability{stubInputCapability{"image": true}}); dropped.Images != 1 {
		t.Fatalf("dropped images = %d, want 1", dropped.Images)
	}
}

func TestUnsupportedToolResultParts_ExemptsArtifactsAndSupportedModalities(t *testing.T) {
	images := []message.ContentPart{
		// Artifact-backed originals are re-resolved per request, so a text-only
		// current model never counts them as unsupported.
		{Type: "image", ArtifactID: "sha256-sample", ImagePath: "sample.png"},
	}

	if dropped := unsupportedToolResultParts(images, stubInputCapability{"image": false}); dropped.any() {
		t.Fatalf("dropped = %#v, want none for artifact-backed parts", dropped)
	}
	if dropped := unsupportedToolResultParts(images, stubInputCapability{"image": true}); dropped.any() {
		t.Fatalf("dropped = %#v, want none for supported modalities", dropped)
	}
	if dropped := unsupportedToolResultParts(images, nil); dropped.any() {
		t.Fatalf("dropped = %#v, want none for a nil capability", dropped)
	}
}

type stubToolResultCapability struct {
	stubInputCapability
}

func (s stubToolResultCapability) SupportsToolResultModalities([]string) bool { return false }
