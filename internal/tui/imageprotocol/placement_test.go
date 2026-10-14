package imageprotocol

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/imagecache"
)

func TestPlacedKittyImageSkipsPayloadPreparation(t *testing.T) {
	source := imagecache.Source{Data: []byte("not an image"), MIME: "image/png"}
	cache := imagecache.New()
	sequence, imageID, err := KittyInline(cache, source, 12, 4, true)
	if err != nil || sequence != "" || imageID <= 0 {
		t.Fatalf("placed image = %q %d %v", sequence, imageID, err)
	}
	if _, _, err := KittyInline(cache, source, 12, 4, false); err == nil {
		t.Fatal("unplaced image skipped payload validation")
	}
}

func TestKittyInlineAndViewerPlacementContracts(t *testing.T) {
	source := imagecache.Source{Data: makeTestPNG(t), MIME: "image/png"}
	cache := imagecache.New()
	inline, inlineID, err := KittyInline(cache, source, 12, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inline, "a=p") || !strings.Contains(inline, "U=1") {
		t.Fatal("inline image did not create a virtual placement")
	}
	viewer, viewerID, err := KittyViewer(cache, source, 7, 12, 4, -1, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inline, fmt.Sprintf("i=%d", inlineID)) || !strings.Contains(viewer, fmt.Sprintf("i=%d", viewerID)) {
		t.Fatal("placement and returned image identities differ")
	}
	for _, want := range []string{"a=p", "p=7", "C=1", "c=12", "r=4", "z=-1", "X=3", "Y=5"} {
		if !strings.Contains(viewer, want) {
			t.Fatalf("viewer placement missing %q", want)
		}
	}
	if strings.Contains(viewer, "U=1") {
		t.Fatal("viewer created a virtual placement")
	}
}

func TestKittyDeleteKeepsPlacementScope(t *testing.T) {
	if KittyDelete(0, 7) != "" || KittyDelete(-1, 0) != "" {
		t.Fatal("invalid image ID produced a deletion")
	}
	image := KittyDelete(11, 0)
	placement := KittyDelete(11, 7)
	for _, sequence := range []string{image, placement} {
		for _, want := range []string{"a=d", "q=2", "i=11", "d=I"} {
			if !strings.Contains(sequence, want) {
				t.Fatalf("deletion missing %q", want)
			}
		}
	}
	if strings.Contains(image, "p=") || !strings.Contains(placement, "p=7") {
		t.Fatal("deletion changed placement scope")
	}
}
