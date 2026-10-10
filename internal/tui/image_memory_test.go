package tui

import (
	"bytes"
	"image"
	"image/png"
	"runtime"
	"testing"
	"weak"

	"github.com/keakon/chord/internal/imagegen"
)

func TestImageIdentityMemoDoesNotRetainPayload(t *testing.T) {
	ref := func() weak.Pointer[byte] {
		data := make([]byte, 4<<20)
		if _, err := imageRuntimeCacheKeyCached(BlockImagePart{Data: data, MimeType: "image/png"}); err != nil {
			t.Fatal(err)
		}
		return weak.Make(&data[0])
	}()
	for range 10 {
		runtime.GC()
		if ref.Value() == nil {
			return
		}
	}
	t.Fatal("identity memo retained an otherwise unreachable image")
}

func TestImageBudgetAppliesToFewLargeEntries(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	for i := range 3 {
		data := make([]byte, 24<<20)
		data[0] = byte(i)
		part := BlockImagePart{Data: data, MimeType: "image/png"}
		entry, err := imageRuntimeEntryForPart(part)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.raw(part); err != nil {
			t.Fatal(err)
		}
	}
	imageRuntimeCache.mu.Lock()
	defer imageRuntimeCache.mu.Unlock()
	var total int64
	for _, entry := range imageRuntimeCache.entries {
		total += entry.residentBytes.Load()
	}
	if total > imageRuntimeCacheBudget {
		t.Fatalf("published cache retained %d bytes beyond its budget", total)
	}
}

func TestImageBudgetCountsRetainedBackingArray(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := BlockImagePart{Data: make([]byte, 1, imageRuntimeCacheBudget+1), MimeType: "image/png"}
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.raw(part); err != nil {
		t.Fatal(err)
	}
	imageRuntimeCache.mu.Lock()
	defer imageRuntimeCache.mu.Unlock()
	if len(imageRuntimeCache.entries) != 0 {
		t.Fatal("a tiny slice retained an oversized backing array in the cache")
	}
}

func TestImagePreviewIsBoundedAndViewerKeepsOriginal(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2400, 1200))); err != nil {
		t.Fatal(err)
	}
	part := BlockImagePart{Data: buf.Bytes(), MimeType: "image/png"}
	if _, err := imagegen.ValidateImage(t.Context(), part.Data, "image/png"); err != nil {
		t.Fatal(err)
	}
	decode := imageCacheDecode
	defer func() { imageCacheDecode = decode }()
	calls := 0
	imageCacheDecode = func(data []byte) (image.Image, string, error) { calls++; return decode(data) }
	preview, err := imageRuntimeEntryForVariant(part, true)
	if err != nil {
		t.Fatal(err)
	}
	_, width, height, err := preview.transportPNG(part)
	if err != nil || width != 1024 || height != 512 {
		t.Fatalf("preview = %dx%d, err=%v", width, height, err)
	}
	if calls != 1 {
		t.Fatalf("verified large preview must still decode to resize: calls=%d", calls)
	}
	original, _ := imageRuntimeEntryForPart(part)
	data, width, height, err := original.transportPNG(part)
	if err != nil || width != 2400 || height != 1200 || !bytes.Equal(data, part.Data) {
		t.Fatalf("viewer changed original: %dx%d, err=%v", width, height, err)
	}
}

func TestVerifiedPNGTransportDoesNotRepeatDecode(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	data := makeTestPNG(t)
	if _, err := imagegen.ValidateImage(t.Context(), data, "image/png"); err != nil {
		t.Fatal(err)
	}
	decode := imageCacheDecode
	defer func() { imageCacheDecode = decode }()
	imageCacheDecode = func(data []byte) (image.Image, string, error) {
		t.Fatal("verified PNG was decoded again")
		return nil, "", nil
	}
	part := BlockImagePart{Data: data, MimeType: "image/png"}
	entry, _ := imageRuntimeEntryForPart(part)
	if _, _, err := entry.base64TransportPNG(part); err != nil {
		t.Fatal(err)
	}
}
