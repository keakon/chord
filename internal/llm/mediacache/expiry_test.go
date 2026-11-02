package mediacache

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestImageLRUCacheExpiresOnlyIdleEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newImageLRUCache(4, 128)
		c.insert("cold", "cold payload", 0)
		c.insert("active", "active payload", 0)
		c.mu.Lock()
		timer := c.idleTimer
		c.mu.Unlock()
		time.Sleep(20 * time.Second)
		value, _, ok := c.get("active")
		c.mu.Lock()
		sameTimer := c.idleTimer == timer
		c.mu.Unlock()
		if !ok || !sameTimer {
			t.Fatal("hit missed or reset the timer")
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if _, _, ok := c.get("cold"); ok {
			t.Fatal("idle entry survived its deadline")
		}
		c.mu.Lock()
		active := c.m["active"] != nil && c.order.Len() == 1 && c.idleTimer != nil
		c.mu.Unlock()
		if !active {
			t.Fatal("expiry removed the recently used entry")
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		c.mu.Lock()
		empty := c.bytes == 0 && len(c.m) == 0 && c.order.Len() == 0 && c.idleTimer == nil
		c.mu.Unlock()
		if !empty || value != "active payload" {
			t.Fatal("empty cache kept its timer or eviction changed a returned payload")
		}
		c.insert("next", "next payload", 0)
		time.Sleep(IdleTimeout)
		synctest.Wait()
		c.mu.Lock()
		reinserted := c.bytes
		c.mu.Unlock()
		if reinserted != 0 {
			t.Fatal("reinsertion did not start a new expiry")
		}
	})
}

func TestImageLRUCacheConcurrentIdleRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newImageLRUCache(4, 128)
		const workers = 8
		done := make(chan struct{}, workers)
		for i := range workers {
			go func() {
				key := fmt.Sprintf("payload-%d", i)
				for range 32 {
					c.insert(key, "payload", 0)
					c.get(key)
					time.Sleep(time.Second)
				}
				done <- struct{}{}
			}()
		}
		deadline := time.After(time.Minute)
		for range workers {
			select {
			case <-done:
			case <-deadline:
				t.Fatal("cache workers did not finish")
			}
		}
		time.Sleep(IdleTimeout)
		synctest.Wait()
		c.mu.Lock()
		resident := c.bytes
		c.mu.Unlock()
		if resident != 0 {
			t.Fatal("concurrent use left idle payloads resident")
		}
	})
}
