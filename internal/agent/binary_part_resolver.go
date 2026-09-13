package agent

import (
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
type binaryPartReadCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	order   []string
	bytes   int64
	budget  int64
}

const binaryPartReadCacheBudget = 64 << 20 // 64 MiB

func newBinaryPartReadCache() *binaryPartReadCache {
	return &binaryPartReadCache{
		entries: make(map[string][]byte),
		budget:  binaryPartReadCacheBudget,
	}
}

func (c *binaryPartReadCache) get(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.entries[path]
	if ok {
		c.touchLocked(path)
	}
	return data, ok
}

func (c *binaryPartReadCache) put(path string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[path]; ok {
		c.touchLocked(path)
		return
	}
	if int64(len(data)) > c.budget {
		return
	}
	for c.bytes+int64(len(data)) > c.budget && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.bytes -= int64(len(c.entries[oldest]))
		delete(c.entries, oldest)
	}
	c.entries[path] = data
	c.order = append(c.order, path)
	c.bytes += int64(len(data))
}

func (c *binaryPartReadCache) touchLocked(path string) {
	for i, p := range c.order {
		if p == path {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, path)
			break
		}
	}
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
