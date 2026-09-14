package agent

import (
	"container/list"
	"fmt"
	"os"
	"sync"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// binaryPartReadCache is a bounded LRU of lazily resolved attachment payloads,
// keyed by the persisted file path. Attachment files are write-once (their
// names carry a nanosecond timestamp), so entries never need invalidation;
// the budget only bounds how many recently used blobs stay resident for the
// wire converter, which re-reads the same attachments on every request of a
// turn.
// The recency order is a container/list, so a hit is O(1). A slice of paths
// costs a linear scan per hit, and this cache is keyed by attachment rather
// than by byte budget: a session with many small attachments accumulates
// entries far past what a per-request scan should pay for.
type binaryPartReadCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element // path → element holding *binaryPartCacheEntry
	order   *list.List               // front = least recently used
	bytes   int64
	budget  int64
}

type binaryPartCacheEntry struct {
	path string
	data []byte
}

const binaryPartReadCacheBudget = 64 << 20 // 64 MiB

func newBinaryPartReadCache() *binaryPartReadCache {
	return &binaryPartReadCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		budget:  binaryPartReadCacheBudget,
	}
}

func (c *binaryPartReadCache) get(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[path]
	if !ok {
		return nil, false
	}
	c.order.MoveToBack(elem)
	return elem.Value.(*binaryPartCacheEntry).data, true
}

func (c *binaryPartReadCache) put(path string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[path]; ok {
		c.order.MoveToBack(elem)
		return
	}
	if int64(len(data)) > c.budget {
		return
	}
	for c.bytes+int64(len(data)) > c.budget && c.order.Len() > 0 {
		oldest := c.order.Front()
		entry := oldest.Value.(*binaryPartCacheEntry)
		c.order.Remove(oldest)
		delete(c.entries, entry.path)
		c.bytes -= int64(len(entry.data))
	}
	c.entries[path] = c.order.PushBack(&binaryPartCacheEntry{path: path, data: data})
	c.bytes += int64(len(data))
}

// resolveBinaryPart loads a persisted attachment for the wire converter. The
// resolver is installed once per process; the paths inside a part are absolute
// session paths, so reads need no session-dir context and stay valid across a
// session switch for messages that still reference the previous session.
func (a *MainAgent) resolveBinaryPart(part message.ContentPart) ([]byte, error) {
	path := part.ImagePath
	if path == "" {
		return nil, fmt.Errorf("binary part has no image path")
	}
	if a.binaryPartCache != nil {
		if data, ok := a.binaryPartCache.get(path); ok {
			return data, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	if a.binaryPartCache != nil {
		a.binaryPartCache.put(path, data)
	}
	return data, nil
}

func (a *MainAgent) installBinaryPartResolver() {
	a.binaryPartCache = newBinaryPartReadCache()
	llm.SetBinaryPartResolver(a.resolveBinaryPart)
}
