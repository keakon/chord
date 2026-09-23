package llm

import (
	"container/list"
	"sync"
)

// wireImage is the validated, provider-ready form of one image part. The bytes
// and the MIME type are one inseparable value: the MIME type always describes
// the cached bytes, which is how normalization rewrites them together.
type wireImage struct {
	key  string
	data []byte
	mime string
}

// wireImageCache memoizes normalized image payloads keyed by the digest of the
// source bytes, so the same history part is not decoded and re-encoded on every
// request or fallback attempt. It is bounded by resident bytes rather than
// entry count because each entry can be several megabytes, and it keeps a
// bounded set of deterministic failures so corrupt or unsupported inputs are
// not re-decoded on every request either. Transient I/O failures never reach
// this cache: they surface as resolver errors before normalization runs.
type wireImageCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element // digest → element holding *wireImage
	order   *list.List               // front = least recently used
	failed  map[string]struct{}      // digest → deterministic normalization failure
	bytes   int64
	budget  int64
}

const (
	wireImageCacheBudget    = 64 << 20
	wireImageFailureBacklog = 256
)

var imageWireCache = &wireImageCache{
	entries: make(map[string]*list.Element),
	order:   list.New(),
	failed:  make(map[string]struct{}),
	budget:  wireImageCacheBudget,
}

func wireImageBytes(entry *wireImage) int64 {
	return int64(len(entry.key)) + int64(len(entry.data)) + int64(len(entry.mime))
}

func (c *wireImageCache) get(key string) (*wireImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToBack(elem)
	return elem.Value.(*wireImage), true
}

func (c *wireImageCache) isKnownFailure(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.failed[key]
	return ok
}

// putFailure remembers a deterministic failure. The set is bounded by dropping
// an arbitrary entry once full: this only decides whether a bad input is
// re-decoded occasionally, so exact LRU order buys nothing.
func (c *wireImageCache) putFailure(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.failed) >= wireImageFailureBacklog {
		for stale := range c.failed {
			delete(c.failed, stale)
			break
		}
	}
	c.failed[key] = struct{}{}
}

func (c *wireImageCache) put(key string, data []byte, mime string) {
	entry := &wireImage{key: key, data: data, mime: mime}
	cost := wireImageBytes(entry)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		c.order.MoveToBack(c.entries[key])
		return
	}
	if cost > c.budget {
		return
	}
	for c.bytes+cost > c.budget && c.order.Len() > 0 {
		oldest := c.order.Front()
		evicted := oldest.Value.(*wireImage)
		c.order.Remove(oldest)
		delete(c.entries, evicted.key)
		c.bytes -= wireImageBytes(evicted)
	}
	c.entries[key] = c.order.PushBack(entry)
	c.bytes += cost
}
