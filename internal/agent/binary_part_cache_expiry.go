package agent

import (
	"time"

	"github.com/keakon/chord/internal/llm/mediacache"
)

func (c *binaryPartReadCache) scheduleIdleReleaseLocked() {
	if c.idleTimer == nil && c.order.Len() > 0 {
		oldest := c.order.Front().Value.(*binaryPartCacheEntry)
		c.idleTimer = time.AfterFunc(time.Until(oldest.lastUse.Add(mediacache.IdleTimeout)), c.releaseIdle)
	}
}

func (c *binaryPartReadCache) releaseIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleTimer = nil
	cutoff := time.Now().Add(-mediacache.IdleTimeout)
	for c.order.Len() > 0 {
		oldest := c.order.Front()
		entry := oldest.Value.(*binaryPartCacheEntry)
		if entry.lastUse.After(cutoff) {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, entry.path)
		c.bytes -= int64(len(entry.data))
	}
	c.scheduleIdleReleaseLocked()
}
