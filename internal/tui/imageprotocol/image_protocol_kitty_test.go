package imageprotocol

import (
	"strings"
	"testing"

	xkitty "github.com/keakon/x/ansi/kitty"

	"github.com/keakon/chord/internal/tui/imagecache"
)

func TestKittyImageIDForVariantUses24BitRange(t *testing.T) {
	part := imagecache.Source{MIME: "image/png", Data: makeTestPNG(t)}
	variant := "inline:12:1"
	key, err := imagecache.FreshKey(part)
	if err != nil {
		t.Fatalf("imagecache.FreshKey() error = %v", err)
	}
	raw := fnv32a([]byte(key + ":" + variant))

	got, err := imageIDForVariant(part, variant)
	if err != nil {
		t.Fatalf("imageIDForVariant() error = %v", err)
	}
	want := int(raw & 0x00FFFFFF)
	if want == 0 {
		want = 1
	}
	if got != want {
		t.Fatalf("imageIDForVariant() = %#x, want %#x", got, want)
	}
	if got <= 0 || got > 0x00FFFFFF {
		t.Fatalf("imageIDForVariant() = %#x, want 1..0x00FFFFFF", got)
	}
}

func TestKittyPlaceholderRowUsesExplicitRowColumnForFirstCells(t *testing.T) {
	got := KittyPlaceholderRow(1, 3)
	want := strings.Builder{}
	want.WriteRune(xkitty.Placeholder)
	want.WriteRune(xkitty.Diacritic(1))
	want.WriteRune(xkitty.Placeholder)
	want.WriteRune(xkitty.Diacritic(1))
	want.WriteRune(xkitty.Diacritic(1))
	want.WriteRune(xkitty.Placeholder)
	if got != want.String() {
		t.Fatalf("KittyPlaceholderRow() = %q, want %q", got, want.String())
	}
}
