package composer

import (
	"testing"
)

func TestImagePartDisplayNameNormalizesClipboardName(t *testing.T) {
	if got := ImagePartDisplayName("clipboard.png", "", "image/png", 2); got != "image2.png" {
		t.Fatalf("imagePartDisplayName() = %q, want %q", got, "image2.png")
	}
	if got := ImagePartDisplayName("", "clipboard.png", "image/png", 3); got != "image3.png" {
		t.Fatalf("imagePartDisplayName() from path = %q, want %q", got, "image3.png")
	}
}

func TestImagePartDisplayNameKeepsRegularFileName(t *testing.T) {
	if got := ImagePartDisplayName("diagram.jpg", "", "image/jpeg", 1); got != "diagram.jpg" {
		t.Fatalf("imagePartDisplayName() = %q, want %q", got, "diagram.jpg")
	}
}
