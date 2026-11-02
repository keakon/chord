package llm

import (
	"time"

	"github.com/keakon/chord/internal/llm/mediacache"
)

func (c *wireImageCache) scheduleIdleReleaseLocked() {
	if c.idleTimer == nil && c.order.Len() > 0 {
		oldest := c.order.Front().Value.(*wireImage)
		c.idleTimer = time.AfterFunc(time.Until(oldest.lastUse.Add(mediacache.IdleTimeout)), c.releaseIdle)
	}
}

func (c *wireImageCache) releaseIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleTimer = nil
	cutoff := time.Now().Add(-mediacache.IdleTimeout)
	for c.order.Len() > 0 {
		oldest := c.order.Front()
		entry := oldest.Value.(*wireImage)
		if entry.lastUse.After(cutoff) {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, entry.key)
		c.bytes -= wireImageBytes(entry)
	}
	c.scheduleIdleReleaseLocked()
}
