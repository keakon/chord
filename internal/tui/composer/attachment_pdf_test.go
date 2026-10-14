package composer

import (
	"testing"
)

func TestAttachmentExtForMimeType_PDF(t *testing.T) {
	if got := AttachmentExtForMimeType("application/pdf"); got != ".pdf" {
		t.Fatalf("attachmentExtForMimeType(pdf) = %q, want .pdf", got)
	}
	if got := AttachmentExtForMimeType("image/png"); got != ".png" {
		t.Fatalf("attachmentExtForMimeType(png) = %q, want .png", got)
	}
}
