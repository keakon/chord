package llm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"
)

// imageLRUCache is a simple bounded LRU cache for base64-encoded images.
// It uses SHA-256 of the raw bytes as the key.
type imageLRUCache struct {
	mu       sync.Mutex
	capacity int
	order    []string          // insertion order (oldest first)
	m        map[string]string // hash → base64 string
}

// newImageLRUCache creates a cache with the given capacity.
func newImageLRUCache(capacity int) *imageLRUCache {
	return &imageLRUCache{
		capacity: capacity,
		order:    make([]string, 0, capacity),
		m:        make(map[string]string),
	}
}

// get returns the cached base64 string and whether it was found.
func (c *imageLRUCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	if !ok {
		return "", false
	}
	// Move to end (most recently used).
	c.moveToEnd(key)
	return v, true
}

// insert adds a key-value pair, evicting the oldest entry if at capacity.
func (c *imageLRUCache) insert(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		c.m[key] = value
		c.moveToEnd(key)
		return
	}
	if len(c.m) >= c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
	c.m[key] = value
	c.order = append(c.order, key)
}

// moveToEnd moves an existing key to the end of the order slice.
func (c *imageLRUCache) moveToEnd(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, key)
			return
		}
	}
}

// imageCache is a package-level LRU cache for base64-encoded images.
// Capacity of 64 entries is sufficient for typical conversation contexts.
var imageCache = newImageLRUCache(64)

// encodeBase64Cached returns the base64 encoding of data, using a cache
// keyed on the SHA-256 hash of the raw bytes to avoid re-encoding identical
// images that appear multiple times in a conversation.
func encodeBase64Cached(data []byte) string {
	hash := sha256.Sum256(data)
	key := hex.EncodeToString(hash[:])

	if encoded, ok := imageCache.get(key); ok {
		return encoded
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	imageCache.insert(key, encoded)
	return encoded
}
