package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
	"weak"
)

func TestImagePayloadExpiryPreservesLayoutAndRebuilds(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := BlockImagePart{Data: makeTestPNG(t), MimeType: "image/png"}
	entry, _ := imageRuntimeEntryForPart(part)
	encoded, size, err := entry.base64TransportPNG(part)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := entry.decodeConfig(part)
	if err != nil {
		t.Fatal(err)
	}
	lastUse := entry.lastPayloadAccess
	imageRuntimeCache.releaseIdlePayloads(lastUse.Add(imagePayloadIdleTTL - time.Nanosecond))
	if entry.residentBytes.Load() == 0 {
		t.Fatal("warm payload was released before its grace elapsed")
	}
	// Layout activity must not extend payload lifetime.
	_, _ = imageRuntimeEntryForPart(part)
	_, _, _ = entry.decodeConfig(part)
	imageRuntimeCache.releaseIdlePayloads(lastUse.Add(imagePayloadIdleTTL))
	if entry.residentBytes.Load() != 0 || entry.rawData != nil || entry.pngData != nil || entry.base64PNG != "" {
		t.Fatal("expired payload remains referenced")
	}
	got, gotFormat, err := entry.decodeConfig(part)
	if err != nil || got != cfg || gotFormat != format || entry.residentBytes.Load() != 0 {
		t.Fatal("layout metadata was lost or repopulated the payload")
	}
	rebuilt, rebuiltSize, err := entry.base64TransportPNG(part)
	if err != nil || rebuilt != encoded || rebuiltSize != size {
		t.Fatalf("rebuild changed transport: %v", err)
	}
	if !bytes.Equal(entry.rawData, part.Data) {
		t.Fatal("canonical attachment was changed")
	}
}

func TestImagePayloadExpirySkipsActiveEntryAndOtherConsumers(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := BlockImagePart{Data: makeTestPNG(t), MimeType: "image/png"}
	entry, _ := imageRuntimeEntryForPart(part)
	data, err := entry.raw(part)
	if err != nil {
		t.Fatal(err)
	}
	now := entry.lastPayloadAccess.Add(imagePayloadIdleTTL)
	entry.mu.Lock()
	imageRuntimeCache.releaseIdlePayloads(now)
	if entry.residentBytes.Load() == 0 {
		t.Fatal("active entry was reclaimed")
	}
	entry.mu.Unlock()
	// Another consumer's real use refreshes the shared payload lease.
	_, _ = entry.raw(part)
	imageRuntimeCache.releaseIdlePayloads(entry.lastPayloadAccess.Add(imagePayloadIdleTTL - time.Nanosecond))
	if entry.residentBytes.Load() == 0 {
		t.Fatal("another consumer's warm payload was reclaimed")
	}
	imageRuntimeCache.releaseIdlePayloads(entry.lastPayloadAccess.Add(imagePayloadIdleTTL))
	if !bytes.Equal(data, part.Data) {
		t.Fatal("eviction invalidated returned bytes")
	}
}

func TestImageLayoutDoesNotRetainInlinePayload(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := BlockImagePart{Data: makeTestPNG(t), MimeType: "image/png"}
	entry, _ := imageRuntimeEntryForPart(part)
	if _, _, err := entry.decodeConfig(part); err != nil {
		t.Fatal(err)
	}
	if entry.rawData != nil || entry.residentBytes.Load() != 0 {
		t.Fatal("layout pinned the canonical payload")
	}
}

func TestImageFilePayloadCollectibleAfterExpiry(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	path := filepath.Join(t.TempDir(), "sample.png")
	if err := os.WriteFile(path, makeTestPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	part := BlockImagePart{ImagePath: path, MimeType: "image/png"}
	entry, _ := imageRuntimeEntryForPart(part)
	ref := func() weak.Pointer[byte] {
		if _, _, err := entry.base64TransportPNG(part); err != nil {
			t.Fatal(err)
		}
		return weak.Make(&entry.rawData[0])
	}()
	imageRuntimeCache.releaseIdlePayloads(entry.lastPayloadAccess.Add(imagePayloadIdleTTL))
	for range 10 {
		runtime.GC()
		if ref.Value() == nil {
			return
		}
	}
	t.Fatal("expired cache still retained file-backed image bytes")
}

func TestImagePayloadExpiryConcurrentTransport(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := BlockImagePart{Data: makeTestPNG(t), MimeType: "image/png"}
	entry, _ := imageRuntimeEntryForPart(part)
	expected, size, err := entry.base64TransportPNG(part)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				got, gotSize, err := entry.base64TransportPNG(part)
				if err != nil || got != expected || gotSize != size {
					t.Errorf("concurrent transport changed: %v", err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 80 {
			imageRuntimeCache.releaseIdlePayloads(time.Now().Add(imagePayloadIdleTTL))
		}
	})
	wg.Wait()
}
