package tui

import (
	"bytes"
	"fmt"
	"image"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/imageutil"
)

func TestImageTransportDecodeValidationSurvivesPayloadExpiry(t *testing.T) {
	resetImageRuntimeCache()
	defer resetImageRuntimeCache()
	data := unverifiedTestPNG(t)
	if imageutil.HasVerifiedImage(data) {
		t.Fatal("fixture was already verified")
	}
	part := BlockImagePart{Data: data, MimeType: "image/png"}
	decode := imageCacheDecode
	defer func() { imageCacheDecode = decode }()
	calls := 0
	imageCacheDecode = func(data []byte) (image.Image, string, error) { calls++; return decode(data) }
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := entry.base64TransportPNG(part)
	if err != nil {
		t.Fatal(err)
	}
	if !imageutil.HasVerifiedImage(data) {
		t.Fatal("successful transport decode did not publish validation")
	}
	imageRuntimeCache.releaseIdlePayloads(entry.lastPayloadAccess.Add(imagePayloadIdleTTL))
	rebuilt, _, err := entry.base64TransportPNG(part)
	if err != nil || rebuilt != original || calls != 1 {
		t.Fatalf("expired transport repeated validation or changed bytes: calls=%d err=%v", calls, err)
	}
}

func TestImageRuntimeKeyMemoKeepsHotEntriesAcrossChurn(t *testing.T) {
	resetImageRuntimeKeyMemo()
	defer resetImageRuntimeKeyMemo()
	hot := BlockImagePart{Data: []byte("hot payload"), MimeType: "image/png"}
	cold := BlockImagePart{Data: []byte("cold payload"), MimeType: "image/png"}
	expected, err := imageRuntimeCacheKeyCached(hot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imageRuntimeCacheKeyCached(cold); err != nil {
		t.Fatal(err)
	}
	parts := []BlockImagePart{hot, cold}
	for i := range imageRuntimeKeyMemoMaxEntries + 1 {
		if got, err := imageRuntimeCacheKeyCached(hot); err != nil || got != expected {
			t.Fatal("hot key changed", err)
		}
		part := BlockImagePart{Data: []byte{byte(i), byte(i >> 8)}, MimeType: "image/png"}
		parts = append(parts, part)
		if _, err := imageRuntimeCacheKeyCached(part); err != nil {
			t.Fatal(err)
		}
	}
	imageRuntimeKeyMemo.mu.Lock()
	defer imageRuntimeKeyMemo.mu.Unlock()
	if len(imageRuntimeKeyMemo.entries) != imageRuntimeKeyMemoMaxEntries {
		t.Fatal("memo did not stay at its bounded capacity")
	}
	if _, ok := imageRuntimeKeyMemo.entries[imagePartKeyRefFor(hot)]; !ok {
		t.Fatal("hot key was evicted")
	}
	if _, ok := imageRuntimeKeyMemo.entries[imagePartKeyRefFor(cold)]; ok {
		t.Fatal("cold key was not evicted")
	}
	runtime.KeepAlive(parts)
}

func TestImageRuntimeKeyConcurrentContentIdentity(t *testing.T) {
	resetImageRuntimeKeyMemo()
	defer resetImageRuntimeKeyMemo()
	part := BlockImagePart{Data: []byte("shared payload"), MimeType: "image/png"}
	expected, err := imageRuntimeCacheKey(part)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			got, err := imageRuntimeCacheKeyCached(part)
			if err != nil || got != expected {
				t.Error("concurrent key mismatch", err)
			}
			clone := part
			clone.Data = bytes.Clone(part.Data)
			got, err = imageRuntimeCacheKeyCached(clone)
			if err != nil || got != expected {
				t.Error("identical bytes have different keys", err)
			}
		})
	}
	wg.Wait()
}

var imageCacheTestSequence atomic.Uint64

func unverifiedTestPNG(t *testing.T) []byte {
	t.Helper()
	return fmt.Appendf(makeTestPNG(t), "validation-%d", imageCacheTestSequence.Add(1))
}
