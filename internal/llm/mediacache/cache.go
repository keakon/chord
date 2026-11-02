// Package mediacache caches derived encodings of immutable media payloads.
package mediacache

import (
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
	"weak"
)

// imageLRUCache is a bounded LRU cache for derived image encodings, backed by
// container/list so a hit is O(1) instead of a slice scan. Entries are bounded
// by both count and resident bytes: the values are base64 encodings of whole
// images, so a count-only cap says nothing about memory — 64 multi-MB images
// is hundreds of MB.
type imageLRUCache struct {
	mu        sync.Mutex
	capacity  int
	budget    int64
	bytes     int64
	order     *list.List               // front = least recently used
	m         map[string]*list.Element // key → element holding lruEntry
	idleTimer *time.Timer
}

type lruEntry struct {
	key          string
	value        string
	base64Offset int
	lastUse      time.Time
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

func (c *imageLRUCache) get(key string) (string, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.m[key]; ok {
		c.order.MoveToBack(elem)
		entry := elem.Value.(*lruEntry)
		entry.lastUse = time.Now()
		return entry.value, entry.base64Offset, true
	}
	return "", 0, false
}

func (c *imageLRUCache) insert(key, value string, base64Offset int) {
	if int64(len(key))+int64(len(value)) > c.budget {
		// Large documents are returned uncached: repeated encoding costs CPU,
		// but retaining one must not exceed the entire resident cache budget.
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.m[key]; ok {
		c.order.MoveToBack(elem)
		entry := elem.Value.(*lruEntry)
		c.bytes -= entryBytes(entry)
		entry.value = value
		entry.base64Offset = base64Offset
		entry.lastUse = time.Now()
		c.bytes += entryBytes(entry)
		c.evictLocked()
		c.scheduleIdleReleaseLocked()
		return
	}
	entry := &lruEntry{key: key, value: value, base64Offset: base64Offset, lastUse: time.Now()}
	c.m[key] = c.order.PushBack(entry)
	c.bytes += entryBytes(entry)
	c.evictLocked()
	c.scheduleIdleReleaseLocked()
}

// evictLocked drops least-recently-used entries until both bounds hold.
func (c *imageLRUCache) evictLocked() {
	for c.order.Len() > 0 && (c.order.Len() > c.capacity || c.bytes > c.budget) {
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

// Both wires share one budget and one backing string per payload. A base64
// result can borrow the suffix of a cached data URL instead of retaining a
// second complete encoding.
const imageEncodingCacheBudget = 32 << 20

var imageEncodingCache = newImageLRUCache(64, imageEncodingCacheBudget)

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

// Digest returns the content digest, reusing weakly memoized payload identities.
func Digest(data []byte) string {
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

// EncodeBase64 returns the base64 encoding of data, using a cache
// keyed on the SHA-256 hash of the raw bytes to avoid re-encoding identical
// images that appear multiple times in a conversation.
func EncodeBase64(data []byte) string {
	key := Digest(data)

	if value, offset, ok := imageEncodingCache.get(key); ok {
		return value[offset:]
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	imageEncodingCache.insert(key, encoded, 0)
	return encoded
}

// DataURL assembles and caches the "data:<mime>;base64," URL the
// OpenAI-family wires embed per image part. The concatenation copies the
// whole multi-MB base64 string, so the URL replaces any standalone encoding.
func DataURL(mimeType string, data []byte) string {
	digest := Digest(data)
	prefix := "data:" + mimeType + ";base64,"
	value, offset, ok := imageEncodingCache.get(digest)
	if ok && offset == len(prefix) && value[:offset] == prefix {
		return value
	}
	encoded := value[offset:]
	if !ok {
		encoded = base64.StdEncoding.EncodeToString(data)
	}
	url := prefix + encoded
	imageEncodingCache.insert(digest, url, len(prefix))
	return url
}

// ResidentBytes reports standalone base64 and data URL backing strings held by
// the process-wide encoding cache. Base64 views of URLs are counted only as
// URLs. Diagnostics only: it must not run on a per-request path.
func ResidentBytes() (encoded, dataURL int64) {
	c := imageEncodingCache
	c.mu.Lock()
	defer c.mu.Unlock()
	for elem := c.order.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*lruEntry)
		if entry.base64Offset == 0 {
			encoded += entryBytes(entry)
		} else {
			dataURL += entryBytes(entry)
		}
	}
	return encoded, dataURL
}

func (c *imageLRUCache) residentBytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}
