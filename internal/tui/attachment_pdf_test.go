package tui

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestAttachmentExtForMimeType_PDF(t *testing.T) {
	if got := attachmentExtForMimeType("application/pdf"); got != ".pdf" {
		t.Fatalf("attachmentExtForMimeType(pdf) = %q, want .pdf", got)
	}
	if got := attachmentExtForMimeType("image/png"); got != ".png" {
		t.Fatalf("attachmentExtForMimeType(png) = %q, want .png", got)
	}
}

func TestAttachmentContentPart_TypeFromMime(t *testing.T) {
	pdf := attachmentContentPart(Attachment{FileName: "a.pdf", MimeType: "application/pdf", Data: []byte("x")})
	if pdf.Type != "pdf" {
		t.Fatalf("pdf attachment part type = %q, want pdf", pdf.Type)
	}
	img := attachmentContentPart(Attachment{FileName: "a.png", MimeType: "image/png", Data: []byte("x")})
	if img.Type != "image" {
		t.Fatalf("image attachment part type = %q, want image", img.Type)
	}
}

func TestAttachmentsFromParts_IncludesPDF(t *testing.T) {
	parts := []message.ContentPart{
		{Type: "text", Text: "hi"},
		{Type: "image", MimeType: "image/png", Data: []byte("png"), FileName: "shot.png"},
		{Type: "pdf", MimeType: "application/pdf", Data: []byte("pdf"), FileName: "report.pdf"},
	}
	atts := attachmentsFromParts(parts)
	if len(atts) != 2 {
		t.Fatalf("attachmentsFromParts len = %d, want 2", len(atts))
	}
	if atts[1].MimeType != "application/pdf" || atts[1].FileName != "report.pdf" {
		t.Fatalf("pdf attachment = %#v", atts[1])
	}
}

func TestPDFNamesFromContentParts(t *testing.T) {
	parts := []message.ContentPart{
		{Type: "image", MimeType: "image/png", Data: []byte("png"), FileName: "shot.png"},
		{Type: "pdf", MimeType: "application/pdf", Data: []byte("pdf"), FileName: "report.pdf"},
		{Type: "pdf", MimeType: "application/pdf", Data: []byte("pdf2"), FileName: "spec.pdf"},
	}
	names := pdfNamesFromContentParts(parts)
	if len(names) != 2 {
		t.Fatalf("pdfNamesFromContentParts len = %d, want 2", len(names))
	}
	if names[0] != "report.pdf" || names[1] != "spec.pdf" {
		t.Fatalf("pdf names = %v", names)
	}
}

func TestInterleaveImageAttachments_AppendsPDF(t *testing.T) {
	parts := []message.ContentPart{{Type: "text", Text: "see attached"}}
	atts := []Attachment{
		{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")},
	}
	out := interleaveAttachments(parts, atts)
	if len(out) != 2 {
		t.Fatalf("interleave len = %d, want 2", len(out))
	}
	if out[0].Type != "text" {
		t.Fatalf("out[0] type = %q, want text", out[0].Type)
	}
	if out[1].Type != "pdf" || out[1].FileName != "report.pdf" {
		t.Fatalf("out[1] = %#v, want pdf report.pdf", out[1])
	}
}

func TestInterleaveImageAttachments_MapsInlineImagesAfterPDF(t *testing.T) {
	parts := []message.ContentPart{{Type: "text", Text: imagePlaceholder(1), DisplayText: inlineImagePlaceholderDisplay, InlineToken: inlineImageTokenMarker}}
	atts := []Attachment{
		{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("pdf")},
		{FileName: "shot.png", MimeType: "image/png", Data: []byte("png"), InlineImagePlaceholder: true},
	}
	out := interleaveAttachments(parts, atts)
	if len(out) != 2 {
		t.Fatalf("interleave len = %d, want 2", len(out))
	}
	if out[0].Type != "image" || out[0].FileName != "shot.png" {
		t.Fatalf("out[0] = %#v, want image shot.png", out[0])
	}
	if out[1].Type != "pdf" || out[1].FileName != "report.pdf" {
		t.Fatalf("out[1] = %#v, want appended pdf report.pdf", out[1])
	}
}

func TestInterleaveImageAttachments_KeepsLiteralImageTextForRegularAttachment(t *testing.T) {
	parts := []message.ContentPart{{Type: "text", Text: "literal [image] text"}}
	atts := []Attachment{
		{FileName: "shot.png", MimeType: "image/png", Data: []byte("png")},
	}
	out := interleaveAttachments(parts, atts)
	if len(out) != 2 {
		t.Fatalf("interleave len = %d, want 2", len(out))
	}
	if out[0].Type != "text" || out[0].Text != "literal [image] text" {
		t.Fatalf("out[0] = %#v, want literal text", out[0])
	}
	if out[1].Type != "image" || out[1].FileName != "shot.png" {
		t.Fatalf("out[1] = %#v, want appended image shot.png", out[1])
	}
}
