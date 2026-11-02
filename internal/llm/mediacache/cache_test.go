package mediacache

import (
	"runtime"
	"strings"
	"testing"
	"weak"
)

func TestPartDigestMemoIsStable(t *testing.T) {
	data := []byte("image-bytes-1")
	first := Digest(data)
	if second := Digest(data); second != first {
		t.Fatalf("digest changed between calls: %q vs %q", first, second)
	}
	// A fresh slice over the same bytes is a different payload identity and is
	// hashed independently; it must still agree with the digest of the bytes.
	if other := Digest([]byte("image-bytes-1")); other != first {
		t.Fatalf("digest of equal bytes = %q, want %q", other, first)
	}
}

// The digest memo keys on payload identity weakly: an entry may not keep a
// multi-MB image resident after the session stops referencing it, or the memo
// would defeat the bounded read cache that re-reads attachments lazily.
func TestPartDigestMemoDoesNotPinPayloads(t *testing.T) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i)
	}
	Digest(data)

	ref := weak.Make(&data[0])
	data = nil
	for range 3 {
		runtime.GC()
		if ref.Value() == nil {
			return
		}
	}
	t.Fatal("image digest memo retained the payload's backing array")
}

// evictLocked bounds the cache by both entry count and resident bytes. A
// replacement must recalculate one entry's bytes instead of double-counting
// the previous value.
func TestImageLRUCacheKeepsBounds(t *testing.T) {
	c := newImageLRUCache(2, 16)
	c.insert("a", strings.Repeat("x", 8), 0)
	if c.bytes != 9 || c.order.Len() != 1 {
		t.Fatalf("after insert bytes=%d len=%d, want 9/1", c.bytes, c.order.Len())
	}
	c.insert("a", strings.Repeat("y", 4), 0)
	if got, _, ok := c.get("a"); !ok || got != strings.Repeat("y", 4) {
		t.Fatalf("same key replaced to %q ok=%v", got, ok)
	}
	if c.bytes != 5 || c.order.Len() != 1 {
		t.Fatalf("replacement bytes=%d len=%d, want 5/1", c.bytes, c.order.Len())
	}
	c.insert("b", strings.Repeat("z", 10), 0)
	if c.bytes != 16 || c.order.Len() != 2 {
		t.Fatalf("second insert bytes=%d len=%d, want 16/2", c.bytes, c.order.Len())
	}
	c.insert("c", strings.Repeat("d", 2), 0)
	if _, _, ok := c.get("a"); ok {
		t.Fatal("byte budget did not evict the oldest entry")
	}
}

func TestImageLRUCacheKeepsAHitEntry(t *testing.T) {
	c := newImageLRUCache(2, 32)
	c.insert("a", "1", 0)
	c.insert("b", "2", 0)
	if got, _, ok := c.get("a"); !ok || got != "1" {
		t.Fatalf("hit on a returned %q ok=%v", got, ok)
	}
	c.insert("c", strings.Repeat("3", 8), 0)
	if _, _, ok := c.get("b"); ok {
		t.Fatal("a hit must move its entry to the recent end before eviction")
	}
}

func TestImageLRUCacheBypassesOversizedEntry(t *testing.T) {
	c := newImageLRUCache(4, 8)
	c.insert("tiny", "x", 0)
	c.insert("huge", strings.Repeat("x", 40), 0)
	if _, _, ok := c.get("huge"); ok {
		t.Fatal("the oversized entry was cached")
	}
	if got, _, ok := c.get("tiny"); !ok || got != "x" {
		t.Fatal("bypassing an oversized entry evicted an existing entry")
	}
	if c.bytes != 5 || c.order.Len() != 1 {
		t.Fatalf("after oversized insert bytes=%d len=%d, want 5/1", c.bytes, c.order.Len())
	}
}

func TestBinaryPartDataURLDoesNotDuplicateCachedEncoding(t *testing.T) {
	previous := imageEncodingCache
	c := newImageLRUCache(4, 1<<20)
	imageEncodingCache = c
	t.Cleanup(func() {
		c.mu.Lock()
		if c.idleTimer != nil {
			c.idleTimer.Stop()
		}
		c.mu.Unlock()
		imageEncodingCache = previous
	})

	data := []byte("image-bytes-2")
	digest := Digest(data)
	first := DataURL("image/png", data)
	second := DataURL("image/png", data)
	if first != second {
		t.Fatalf("same payload encoded differently: %q vs %q", first, second)
	}
	if c.order.Len() != 1 {
		t.Fatalf("encoding cache entries=%d, want 1", c.order.Len())
	}
	if got, _, ok := c.get(digest); !ok || got != first {
		t.Fatalf("url cache miss or wrong value %q ok=%v", got, ok)
	}
}
