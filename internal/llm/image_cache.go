package llm

import (
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"weak"
)

// imageLRUCache is a bounded LRU cache for derived image encodings, backed by
// container/list so a hit is O(1) instead of a slice scan. Entries are bounded
// by both count and resident bytes: the values are base64 encodings of whole
// images, so a count-only cap says nothing about memory — 64 multi-MB images
// is hundreds of MB.
type imageLRUCache struct {
	mu       sync.Mutex
	capacity int
	budget   int64
	bytes    int64
	order    *list.List               // front = least recently used
	m        map[string]*list.Element // key → element holding lruEntry
}

type lruEntry struct {
	key   string
	value string
}

func newImageLRUCache(capacity int, budget int64) *imageLRUCache {
	return &imageLRUCache{
		capacity: capacity,
		budget:   budget,
		order:    list.New(),
		m:        make(map[string]*list.Element),
	}
}

// entryBytes is the resident cost of one entry; the value dominates, the key is
// a short digest.
func entryBytes(e *lruEntry) int64 {
	return int64(len(e.key)) + int64(len(e.value))
}

func (c *imageLRUCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.m[key]; ok {
		c.order.MoveToBack(elem)
		return elem.Value.(*lruEntry).value, true
	}
	return "", false
}

func (c *imageLRUCache) insert(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.m[key]; ok {
		c.order.MoveToBack(elem)
		entry := elem.Value.(*lruEntry)
		c.bytes -= entryBytes(entry)
		entry.value = value
		c.bytes += entryBytes(entry)
		c.evictLocked()
		return
	}
	entry := &lruEntry{key: key, value: value}
	c.m[key] = c.order.PushBack(entry)
	c.bytes += entryBytes(entry)
	c.evictLocked()
}

// evictLocked drops least-recently-used entries until both bounds hold. The
// most recent insert is always kept, even when it alone exceeds the budget:
// evicting it would make the cache a pure cost for the one image the caller is
// currently converting.
func (c *imageLRUCache) evictLocked() {
	for c.order.Len() > 1 && (c.order.Len() > c.capacity || c.bytes > c.budget) {
		oldest := c.order.Front()
		if oldest == nil {
			return
		}
		entry := oldest.Value.(*lruEntry)
		c.order.Remove(oldest)
		delete(c.m, entry.key)
		c.bytes -= entryBytes(entry)
	}
}

// imageEncodingCacheBudget is the resident-byte budget of each encoding cache.
// The two together match the 64 MiB the attachment read cache and the TUI image
// cache each hold, so the wire layer's share of a session's image memory stays
// comparable to theirs.
const imageEncodingCacheBudget = 32 << 20

// imageCache is a package-level LRU cache for base64-encoded images.
// Capacity of 64 entries is sufficient for typical conversation contexts.
var imageCache = newImageLRUCache(64, imageEncodingCacheBudget)

// imageDataURLCache caches the fully assembled data URL (media type included)
// so per-request wire conversion stops re-copying multi-MB base64 strings.
var imageDataURLCache = newImageLRUCache(64, imageEncodingCacheBudget)

// partDigestMemo memoizes the SHA-256 digest per inline payload identity. The
// same message slices are re-encoded on every request and every fallback
// attempt; re-hashing multi-MB images each time is a measurable per-turn tax.
//
// The identity is a weak pointer to the payload's backing array, not a strong
// one: the memo must not keep every image that was ever hashed resident (the
// lazily resolved attachments are re-read through a bounded read cache, and a
// strong key would pin each evicted copy). Reclaimed entries stop matching —
// weak pointers compare by object identity, so a recycled address cannot hit a
// stale entry — and the wholesale reset below reclaims the map itself.
type partDigestRef struct {
	ref weak.Pointer[byte]
	n   int
}

var (
	partDigestMemo    atomic.Pointer[sync.Map]
	partDigestMemoN   atomic.Int64
	partDigestMemoCap int64 = 4096
)

func partDigest(data []byte) string {
	var ref partDigestRef
	if len(data) > 0 {
		ref.ref = weak.Make(&data[0])
		ref.n = len(data)
	}
	m := partDigestMemo.Load()
	if m == nil {
		m = &sync.Map{}
		if !partDigestMemo.CompareAndSwap(nil, m) {
			m = partDigestMemo.Load()
		}
	}
	if v, ok := m.Load(ref); ok {
		return v.(string)
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	if _, loaded := m.LoadOrStore(ref, key); !loaded && partDigestMemoN.Add(1) > partDigestMemoCap {
		// Reset the memo wholesale once it overflows; the next request
		// repopulates it from scratch.
		partDigestMemo.Store(&sync.Map{})
		partDigestMemoN.Store(0)
	}
	return key
}

// encodeBase64Cached returns the base64 encoding of data, using a cache
// keyed on the SHA-256 hash of the raw bytes to avoid re-encoding identical
// images that appear multiple times in a conversation.
func encodeBase64Cached(data []byte) string {
	key := partDigest(data)

	if encoded, ok := imageCache.get(key); ok {
		return encoded
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	imageCache.insert(key, encoded)
	return encoded
}

// binaryPartDataURL assembles and caches the "data:<mime>;base64," URL the
// OpenAI-family wires embed per image part. The concatenation copies the
// whole multi-MB base64 string, so it is cached alongside the encoding.
func binaryPartDataURL(mimeType string, data []byte) string {
	digest := partDigest(data)
	key := digest + "\x00" + mimeType
	if url, ok := imageDataURLCache.get(key); ok {
		return url
	}
	// Reuse an encoding the Anthropic or Gemini wire already cached, but do not
	// add one: the URL assembled below embeds the whole base64 string, so
	// caching both would keep two copies of every image resident for the wires
	// that only ever ask for the URL.
	encoded, ok := imageCache.get(digest)
	if !ok {
		encoded = base64.StdEncoding.EncodeToString(data)
	}
	url := "data:" + mimeType + ";base64," + encoded
	imageDataURLCache.insert(key, url)
	return url
}

// defaultPDFMediaType returns the PDF media type, falling back to the canonical
// "application/pdf" when a content part carries no explicit MIME type.
func defaultPDFMediaType(mimeType string) string {
	if mimeType == "" {
		return "application/pdf"
	}
	return mimeType
}

// defaultPDFFilename returns a usable filename for providers (OpenAI Chat/
// Responses) that require one on file blocks, defaulting when none is set.
func defaultPDFFilename(name string) string {
	if name == "" {
		return "document.pdf"
	}
	return name
}
