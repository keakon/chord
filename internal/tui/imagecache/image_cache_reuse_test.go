package imagecache

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
	part := Source{Data: data, MIME: "image/png"}
	decode := imageCacheDecode
	defer func() { imageCacheDecode = decode }()
	calls := 0
	imageCacheDecode = func(data []byte) (image.Image, string, error) { calls++; return decode(data) }
	entry, err := imageRuntimeCache.Lookup(part, false)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := entry.Base64PNG(part)
	if err != nil {
		t.Fatal(err)
	}
	if !imageutil.HasVerifiedImage(data) {
		t.Fatal("successful transport decode did not publish validation")
	}
	imageRuntimeCache.ReleaseIdle(entry.lastPayloadAccess.Add(PayloadIdleTTL))
	rebuilt, _, err := entry.Base64PNG(part)
	if err != nil || rebuilt != original || calls != 1 {
		t.Fatalf("expired transport repeated validation or changed bytes: calls=%d err=%v", calls, err)
	}
}

func TestImageRuntimeKeyMemoKeepsHotEntriesAcrossChurn(t *testing.T) {
	resetImageRuntimeKeyMemo()
	defer resetImageRuntimeKeyMemo()
	hot := Source{Data: []byte("hot payload"), MIME: "image/png"}
	cold := Source{Data: []byte("cold payload"), MIME: "image/png"}
	expected, err := Key(hot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Key(cold); err != nil {
		t.Fatal(err)
	}
	parts := []Source{hot, cold}
	for i := range imageRuntimeKeyMemoMaxEntries + 1 {
		if got, err := Key(hot); err != nil || got != expected {
			t.Fatal("hot key changed", err)
		}
		part := Source{Data: []byte{byte(i), byte(i >> 8)}, MIME: "image/png"}
		parts = append(parts, part)
		if _, err := Key(part); err != nil {
			t.Fatal(err)
		}
	}
	imageRuntimeKeyMemo.mu.Lock()
	defer imageRuntimeKeyMemo.mu.Unlock()
	if len(imageRuntimeKeyMemo.entries) != imageRuntimeKeyMemoMaxEntries {
		t.Fatal("memo did not stay at its bounded capacity")
	}
	if _, ok := imageRuntimeKeyMemo.entries[IdentityOf(hot)]; !ok {
		t.Fatal("hot key was evicted")
	}
	if _, ok := imageRuntimeKeyMemo.entries[IdentityOf(cold)]; ok {
		t.Fatal("cold key was not evicted")
	}
	runtime.KeepAlive(parts)
}

func TestImageRuntimeKeyConcurrentContentIdentity(t *testing.T) {
	resetImageRuntimeKeyMemo()
	defer resetImageRuntimeKeyMemo()
	part := Source{Data: []byte("shared payload"), MIME: "image/png"}
	expected, err := FreshKey(part)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			got, err := Key(part)
			if err != nil || got != expected {
				t.Error("concurrent key mismatch", err)
			}
			clone := part
			clone.Data = bytes.Clone(part.Data)
			got, err = Key(clone)
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
