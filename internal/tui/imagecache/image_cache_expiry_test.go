package imagecache

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
	part := Source{Data: makeTestPNG(t), MIME: "image/png"}
	entry, _ := imageRuntimeCache.Lookup(part, false)
	encoded, size, err := entry.Base64PNG(part)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := entry.Config(part)
	if err != nil {
		t.Fatal(err)
	}
	lastUse := entry.lastPayloadAccess
	imageRuntimeCache.ReleaseIdle(lastUse.Add(PayloadIdleTTL - time.Nanosecond))
	if entry.residentBytes.Load() == 0 {
		t.Fatal("warm payload was released before its grace elapsed")
	}
	// Layout activity must not extend payload lifetime.
	_, _ = imageRuntimeCache.Lookup(part, false)
	_, _, _ = entry.Config(part)
	imageRuntimeCache.ReleaseIdle(lastUse.Add(PayloadIdleTTL))
	if entry.residentBytes.Load() != 0 || entry.rawData != nil || entry.pngData != nil || entry.base64PNG != "" {
		t.Fatal("expired payload remains referenced")
	}
	got, gotFormat, err := entry.Config(part)
	if err != nil || got != cfg || gotFormat != format || entry.residentBytes.Load() != 0 {
		t.Fatal("layout metadata was lost or repopulated the payload")
	}
	rebuilt, rebuiltSize, err := entry.Base64PNG(part)
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
	part := Source{Data: makeTestPNG(t), MIME: "image/png"}
	entry, _ := imageRuntimeCache.Lookup(part, false)
	data, err := entry.Raw(part)
	if err != nil {
		t.Fatal(err)
	}
	now := entry.lastPayloadAccess.Add(PayloadIdleTTL)
	entry.mu.Lock()
	imageRuntimeCache.ReleaseIdle(now)
	if entry.residentBytes.Load() == 0 {
		t.Fatal("active entry was reclaimed")
	}
	entry.mu.Unlock()
	// Another consumer's real use refreshes the shared payload lease.
	_, _ = entry.Raw(part)
	imageRuntimeCache.ReleaseIdle(entry.lastPayloadAccess.Add(PayloadIdleTTL - time.Nanosecond))
	if entry.residentBytes.Load() == 0 {
		t.Fatal("another consumer's warm payload was reclaimed")
	}
	imageRuntimeCache.ReleaseIdle(entry.lastPayloadAccess.Add(PayloadIdleTTL))
	if !bytes.Equal(data, part.Data) {
		t.Fatal("eviction invalidated returned bytes")
	}
}

func TestImageLayoutDoesNotRetainInlinePayload(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	part := Source{Data: makeTestPNG(t), MIME: "image/png"}
	entry, _ := imageRuntimeCache.Lookup(part, false)
	if _, _, err := entry.Config(part); err != nil {
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
	part := Source{Path: path, MIME: "image/png"}
	entry, _ := imageRuntimeCache.Lookup(part, false)
	ref := func() weak.Pointer[byte] {
		if _, _, err := entry.Base64PNG(part); err != nil {
			t.Fatal(err)
		}
		return weak.Make(&entry.rawData[0])
	}()
	imageRuntimeCache.ReleaseIdle(entry.lastPayloadAccess.Add(PayloadIdleTTL))
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
	part := Source{Data: makeTestPNG(t), MIME: "image/png"}
	entry, _ := imageRuntimeCache.Lookup(part, false)
	expected, size, err := entry.Base64PNG(part)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				got, gotSize, err := entry.Base64PNG(part)
				if err != nil || got != expected || gotSize != size {
					t.Errorf("concurrent transport changed: %v", err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 80 {
			imageRuntimeCache.ReleaseIdle(time.Now().Add(PayloadIdleTTL))
		}
	})
	wg.Wait()
}
