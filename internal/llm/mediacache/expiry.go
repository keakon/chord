package mediacache

import "time"

// IdleTimeout bounds how long unused derived media stays resident.
const IdleTimeout = 30 * time.Second

func (c *imageLRUCache) scheduleIdleReleaseLocked() {
	if c.idleTimer == nil && c.order.Len() > 0 {
		oldest := c.order.Front().Value.(*lruEntry)
		c.idleTimer = time.AfterFunc(time.Until(oldest.lastUse.Add(IdleTimeout)), c.releaseIdle)
	}
}

func (c *imageLRUCache) releaseIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleTimer = nil
	cutoff := time.Now().Add(-IdleTimeout)
	for c.order.Len() > 0 {
		oldest := c.order.Front()
		entry := oldest.Value.(*lruEntry)
		if entry.lastUse.After(cutoff) {
			break
		}
		c.order.Remove(oldest)
		delete(c.m, entry.key)
		c.bytes -= entryBytes(entry)
	}
	c.scheduleIdleReleaseLocked()
}
