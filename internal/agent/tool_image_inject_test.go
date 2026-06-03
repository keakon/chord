package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestBuildToolImageMessage_OrderingAndShape(t *testing.T) {
	parts := []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("a"), FileName: "first.png"},
		{Type: "image", MimeType: "image/jpeg", Data: []byte("b"), FileName: "second.jpg"},
	}
	msg := buildToolImageMessage(parts)

	if msg.Role != "user" {
		t.Fatalf("role = %q, want user", msg.Role)
	}
	if len(msg.Parts) != 3 {
		t.Fatalf("parts len = %d, want 3 (1 text + 2 images)", len(msg.Parts))
	}
	// The synthetic message leads with a human-readable text part.
	if msg.Parts[0].Type != "text" {
		t.Fatalf("first part type = %q, want text", msg.Parts[0].Type)
	}
	if !strings.Contains(msg.Parts[0].Text, "2 image") {
		t.Fatalf("text part %q should mention the image count", msg.Parts[0].Text)
	}
	if msg.Content != msg.Parts[0].Text {
		t.Fatalf("Content (%q) should match the leading text part (%q)", msg.Content, msg.Parts[0].Text)
	}
	// Image parts follow in their original tool-call completion order.
	if msg.Parts[1].FileName != "first.png" || msg.Parts[2].FileName != "second.jpg" {
		t.Fatalf("image parts out of order: %q, %q", msg.Parts[1].FileName, msg.Parts[2].FileName)
	}
	if msg.Parts[1].Type != "image" || msg.Parts[2].Type != "image" {
		t.Fatalf("image parts lost their type: %#v", msg.Parts[1:])
	}
}
