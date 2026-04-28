package tui

import (
	"strings"
	"testing"

	xkitty "github.com/charmbracelet/x/ansi/kitty"
)

func TestKittyImageIDForVariantUses24BitRange(t *testing.T) {
	part := BlockImagePart{MimeType: "image/png", Data: makeTestPNG(t)}
	variant := "inline:12:1"
	key, err := imageRuntimeCacheKey(part)
	if err != nil {
		t.Fatalf("imageRuntimeCacheKey() error = %v", err)
	}
	raw := fnv32a([]byte(key + ":" + variant))
	if raw <= 0x00FFFFFF {
		t.Fatalf("test setup expected raw hash to exceed 24 bits, got %#x", raw)
	}

	got, err := kittyImageIDForVariant(part, variant)
	if err != nil {
		t.Fatalf("kittyImageIDForVariant() error = %v", err)
	}
	want := int(raw & 0x00FFFFFF)
	if want == 0 {
		want = 1
	}
	if got != want {
		t.Fatalf("kittyImageIDForVariant() = %#x, want %#x", got, want)
	}
	if got <= 0 || got > 0x00FFFFFF {
		t.Fatalf("kittyImageIDForVariant() = %#x, want 1..0x00FFFFFF", got)
	}
}

func TestKittyPlaceholderRowUsesExplicitRowColumnForFirstCells(t *testing.T) {
	got := kittyPlaceholderRow(1, 3)
	want := strings.Builder{}
	want.WriteRune(xkitty.Placeholder)
	want.WriteRune(xkitty.Diacritic(1))
	want.WriteRune(xkitty.Placeholder)
	want.WriteRune(xkitty.Diacritic(1))
	want.WriteRune(xkitty.Diacritic(1))
	want.WriteRune(xkitty.Placeholder)
	if got != want.String() {
		t.Fatalf("kittyPlaceholderRow() = %q, want %q", got, want.String())
	}
}
