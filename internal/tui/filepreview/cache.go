package filepreview

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
)

type cacheEntry struct {
	key   string
	value Result
	bytes int
}

// Cache is a concurrency-safe, byte-bounded LRU of rendered previews.
// Its zero value is ready to use.
type Cache struct {
	mu      sync.Mutex
	order   list.List
	entries map[string]*list.Element
	bytes   int
}

func filePreviewCacheKey(req Request, data []byte) string {
	locator, _ := json.Marshal(req.File.Locator)
	style, _ := json.Marshal(req.Style)
	return fmt.Sprintf("%s/%x/%t/%d/%x", string(locator)+req.Session, sha256.Sum256(data), req.Markdown, req.Width, sha256.Sum256(style))
}
func (c *Cache) get(key string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return Result{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(cacheEntry).value, true
}
func (c *Cache) put(key string, value Result) {
	if c == nil || value.Err != nil || value.Image != nil {
		return
	}
	size := cap(value.Raw) + len(key) + len(value.Note) + len(value.Lines)*16 + len(value.Plain)*16 + 256
	for _, line := range value.Lines {
		size += len(line)
	}
	for _, line := range value.Plain {
		size += len(line)
	}
	if size > fileViewerCacheBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if prior, ok := c.entries[key]; ok {
		c.bytes -= prior.Value.(cacheEntry).bytes
		c.order.Remove(prior)
		delete(c.entries, key)
	}
	for c.bytes+size > fileViewerCacheBytes {
		last := c.order.Back()
		entry := last.Value.(cacheEntry)
		delete(c.entries, entry.key)
		c.bytes -= entry.bytes
		c.order.Remove(last)
	}
	c.entries[key] = c.order.PushFront(cacheEntry{key: key, value: value, bytes: size})
	c.bytes += size
}
