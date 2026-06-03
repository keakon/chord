package llm

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// pdfTestParts builds a user message carrying a text part plus a PDF part.
func pdfTestParts() ([]message.Message, string) {
	raw := []byte("%PDF-1.7\nfake pdf bytes")
	msgs := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "summarize this"},
			{Type: "pdf", MimeType: "application/pdf", Data: raw, FileName: "report.pdf"},
		},
	}}
	return msgs, base64.StdEncoding.EncodeToString(raw)
}

func TestConvertMessagesToGemini_WithPDFPart(t *testing.T) {
	msgs, wantB64 := pdfTestParts()
	got := convertMessagesToGemini(msgs)
	if len(got) != 1 {
		t.Fatalf("convertMessagesToGemini() len = %d, want 1", len(got))
	}
	if len(got[0].Parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(got[0].Parts))
	}
	pdf := got[0].Parts[1].InlineData
	if pdf == nil {
		t.Fatalf("pdf part InlineData is nil: %#v", got[0].Parts[1])
	}
	if pdf.MimeType != "application/pdf" {
		t.Fatalf("pdf MimeType = %q, want application/pdf", pdf.MimeType)
	}
	if pdf.Data != wantB64 {
		t.Fatalf("pdf Data = %q, want %q", pdf.Data, wantB64)
	}
}

func TestConvertMessagesToAnthropic_WithPDFPart(t *testing.T) {
	msgs, wantB64 := pdfTestParts()
	got := convertMessages(msgs)
	if len(got) != 1 {
		t.Fatalf("convertMessages() len = %d, want 1", len(got))
	}
	blocks, ok := got[0].Content.([]anthropicContent)
	if !ok {
		t.Fatalf("content type = %T, want []anthropicContent", got[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "text" {
		t.Fatalf("block[0] type = %q, want text", blocks[0].Type)
	}
	doc := blocks[1]
	if doc.Type != "document" {
		t.Fatalf("pdf block type = %q, want document", doc.Type)
	}
	if doc.Source == nil || doc.Source.Type != "base64" || doc.Source.MediaType != "application/pdf" {
		t.Fatalf("pdf source = %#v", doc.Source)
	}
	if doc.Source.Data != wantB64 {
		t.Fatalf("pdf data = %q, want %q", doc.Source.Data, wantB64)
	}
}

func TestConvertMessagesToOpenAI_WithPDFPart(t *testing.T) {
	msgs, wantB64 := pdfTestParts()
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, msgs)

	var userMsg *openAIMessage
	for i := range out {
		if out[i].Role == "user" {
			userMsg = &out[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatal("no user message produced")
	}
	blocks, ok := userMsg.Content.([]openAIContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []openAIContentBlock", userMsg.Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	fileBlock := blocks[1]
	if fileBlock.Type != "file" {
		t.Fatalf("pdf block type = %q, want file", fileBlock.Type)
	}
	if fileBlock.File == nil {
		t.Fatalf("pdf block File is nil")
	}
	if fileBlock.File.Filename != "report.pdf" {
		t.Fatalf("pdf filename = %q, want report.pdf", fileBlock.File.Filename)
	}
	wantData := "data:application/pdf;base64," + wantB64
	if fileBlock.File.FileData != wantData {
		t.Fatalf("pdf file_data = %q, want %q", fileBlock.File.FileData, wantData)
	}
}

func TestConvertMessagesToResponses_WithPDFPart(t *testing.T) {
	msgs, wantB64 := pdfTestParts()
	items := convertMessagesToResponses("", modelcompat.WireFamilyOpenAIResponses, msgs)
	if len(items) != 1 {
		t.Fatalf("convertMessagesToResponses() len = %d, want 1", len(items))
	}
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []responsesContentBlock", items[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	fileBlock := blocks[1]
	if fileBlock.Type != "input_file" {
		t.Fatalf("pdf block type = %q, want input_file", fileBlock.Type)
	}
	if fileBlock.Filename != "report.pdf" {
		t.Fatalf("pdf filename = %q, want report.pdf", fileBlock.Filename)
	}
	if !strings.HasPrefix(fileBlock.FileData, "data:application/pdf;base64,") {
		t.Fatalf("pdf file_data = %q, want data URL prefix", fileBlock.FileData)
	}
	if !strings.HasSuffix(fileBlock.FileData, wantB64) {
		t.Fatalf("pdf file_data = %q, want suffix %q", fileBlock.FileData, wantB64)
	}
}

// TestConvertMessages_ImageAndPDFCoexist verifies image and PDF parts in one
// message both survive conversion with their distinct wire encodings.
func TestConvertMessages_ImageAndPDFCoexist(t *testing.T) {
	msgs := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "image", MimeType: "image/png", Data: []byte("png")},
			{Type: "pdf", MimeType: "application/pdf", Data: []byte("pdf"), FileName: "a.pdf"},
		},
	}}

	got := convertMessages(msgs)
	blocks, ok := got[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 {
		t.Fatalf("anthropic blocks = %#v", got[0].Content)
	}
	if blocks[0].Type != "image" || blocks[0].Source == nil || blocks[0].Source.MediaType != "image/png" {
		t.Fatalf("image block = %#v", blocks[0])
	}
	if blocks[1].Type != "document" || blocks[1].Source == nil || blocks[1].Source.MediaType != "application/pdf" {
		t.Fatalf("pdf block = %#v", blocks[1])
	}
}
