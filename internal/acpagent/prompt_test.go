package acpagent

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/message"
)

func TestMessagePartsConvertsTextAndImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	parts, err := messageParts([]acp.ContentBlock{
		acp.TextBlock("hello"),
		acp.ImageBlock(base64.StdEncoding.EncodeToString(png), "image/png"),
	})
	if err != nil {
		t.Fatalf("messageParts returned error: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %#v, want 2", parts)
	}
	if parts[0].Type != message.ContentPartText || parts[0].Text != "hello" {
		t.Fatalf("text part = %#v", parts[0])
	}
	if parts[1].Type != message.ContentPartImage || parts[1].MimeType != "image/png" {
		t.Fatalf("image part = %#v", parts[1])
	}
	if string(parts[1].Data) != string(png) {
		t.Fatalf("image data = %v, want %v", parts[1].Data, png)
	}
}

func TestMessagePartsDetectsImageMimeType(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 32)...)
	parts, err := messageParts([]acp.ContentBlock{
		acp.ImageBlock(base64.StdEncoding.EncodeToString(png), ""),
	})
	if err != nil {
		t.Fatalf("messageParts returned error: %v", err)
	}
	if len(parts) != 1 || parts[0].MimeType != "image/png" {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestMessagePartsRejectsUnsupportedBlocks(t *testing.T) {
	tests := []struct {
		name   string
		blocks []acp.ContentBlock
	}{
		{"empty prompt", nil},
		{"blank text", []acp.ContentBlock{acp.TextBlock("   ")}},
		{"audio", []acp.ContentBlock{{Audio: &acp.ContentBlockAudio{Data: "AAAA", MimeType: "audio/wav", Type: "audio"}}}},
		{"embedded resource without text", []acp.ContentBlock{{Resource: &acp.ContentBlockResource{Type: "resource"}}}},
		{"embedded blob resource", []acp.ContentBlock{{Resource: &acp.ContentBlockResource{
			Type:     "resource",
			Resource: acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{Uri: "file:///tmp/blob.bin", Blob: "AAAA"}},
		}}}},
		{"invalid base64 image", []acp.ContentBlock{acp.ImageBlock("not base64!", "image/png")}},
		{"image without data", []acp.ContentBlock{acp.ImageBlock("", "image/png")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := messageParts(tt.blocks)
			reqErr, ok := errors.AsType[*acp.RequestError](err)
			if !ok || reqErr.Code != -32602 {
				t.Fatalf("error = %v, want invalid params", err)
			}
		})
	}
}

func TestMessagePartsDowngradesEmbeddedTextResource(t *testing.T) {
	parts, err := messageParts([]acp.ContentBlock{{Resource: &acp.ContentBlockResource{
		Type: "resource",
		Resource: acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
			Uri:  "file:///tmp/notes.txt",
			Text: "remember this",
		}},
	}}})
	if err != nil {
		t.Fatalf("messageParts returned error: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != message.ContentPartText {
		t.Fatalf("parts = %#v, want one text part", parts)
	}
	if !strings.Contains(parts[0].Text, "remember this") || !strings.Contains(parts[0].Text, "file:///tmp/notes.txt") {
		t.Fatalf("text part = %q", parts[0].Text)
	}

	parts, err = messageParts([]acp.ContentBlock{{Resource: &acp.ContentBlockResource{
		Type:     "resource",
		Resource: acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{Text: "no uri"}},
	}}})
	if err != nil {
		t.Fatalf("messageParts returned error: %v", err)
	}
	if len(parts) != 1 || parts[0].Text != "no uri" {
		t.Fatalf("parts = %#v, want the bare text", parts)
	}
}

func TestResourceLinkReadsLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("remember this"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	blocks := []acp.ContentBlock{{
		ResourceLink: &acp.ContentBlockResourceLink{Uri: "file://" + path, Name: "notes.txt", Type: "resource_link"},
	}}
	parts, err := messageParts(blocks)
	if err != nil {
		t.Fatalf("messageParts returned error: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %#v, want 1", parts)
	}
	text := parts[0].Text
	if !strings.Contains(text, `<file path="`+path+`"`) || !strings.Contains(text, "remember this") {
		t.Fatalf("file part = %q", text)
	}
}

func TestResourceLinkFallsBackToText(t *testing.T) {
	tests := []struct {
		name string
		link acp.ContentBlockResourceLink
	}{
		{"remote uri", acp.ContentBlockResourceLink{Uri: "https://example.invalid/x", Name: "x"}},
		{"missing file", acp.ContentBlockResourceLink{Uri: "file:///nonexistent/definitely-missing.txt", Name: "missing"}},
		{"nameless", acp.ContentBlockResourceLink{Uri: "https://example.invalid/y"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := resourceLinkParts(tt.link)
			if len(parts) != 1 || parts[0].Type != message.ContentPartText {
				t.Fatalf("parts = %#v", parts)
			}
			if !strings.Contains(parts[0].Text, tt.link.Uri) {
				t.Fatalf("fallback text %q does not mention %q", parts[0].Text, tt.link.Uri)
			}
		})
	}
}

func TestFilePathFromURI(t *testing.T) {
	if path, ok := filePathFromURI("file:///tmp/a%20b.txt"); !ok || path != "/tmp/a b.txt" {
		t.Fatalf("filePathFromURI = %q, %v", path, ok)
	}
	for _, uri := range []string{"https://example.invalid/a", "file://remotehost/tmp/a", "file://", ""} {
		if path, ok := filePathFromURI(uri); ok {
			t.Fatalf("filePathFromURI(%q) = %q, want not ok", uri, path)
		}
	}
}
