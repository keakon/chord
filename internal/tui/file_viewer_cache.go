package tui

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
)

type fileViewerCacheEntry struct {
	key   string
	value fileViewerLoadedMsg
	bytes int
}
type fileViewerCache struct {
	mu      sync.Mutex
	order   list.List
	entries map[string]*list.Element
	bytes   int
}

func filePreviewCacheKey(req fileViewerRequest, data []byte) string {
	locator, _ := json.Marshal(req.file.Locator)
	style, _ := json.Marshal(req.style)
	return fmt.Sprintf("%s/%x/%t/%d/%x", string(locator)+req.session, sha256.Sum256(data), req.markdown, req.width, sha256.Sum256(style))
}
func (c *fileViewerCache) get(key string) (fileViewerLoadedMsg, bool) {
	if c == nil {
		return fileViewerLoadedMsg{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return fileViewerLoadedMsg{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(fileViewerCacheEntry).value, true
}
func (c *fileViewerCache) put(key string, value fileViewerLoadedMsg) {
	if c == nil || value.err != nil || value.image != nil {
		return
	}
	size := cap(value.raw) + len(key) + len(value.note) + len(value.lines)*16 + len(value.plain)*16 + 256
	for _, line := range value.lines {
		size += len(line)
	}
	for _, line := range value.plain {
		size += len(line)
	}
	if size > fileViewerCacheBytes {
		return
	}
	value.request = fileViewerRequest{}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if prior, ok := c.entries[key]; ok {
		c.bytes -= prior.Value.(fileViewerCacheEntry).bytes
		c.order.Remove(prior)
		delete(c.entries, key)
	}
	for c.bytes+size > fileViewerCacheBytes {
		last := c.order.Back()
		entry := last.Value.(fileViewerCacheEntry)
		delete(c.entries, entry.key)
		c.bytes -= entry.bytes
		c.order.Remove(last)
	}
	c.entries[key] = c.order.PushFront(fileViewerCacheEntry{key: key, value: value, bytes: size})
	c.bytes += size
}
