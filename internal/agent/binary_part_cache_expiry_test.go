package agent

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/llm/mediacache"
)

func TestBinaryPartReadCacheExpiresOnlyIdleEntries(t *testing.T) {
	for _, refresh := range []string{"get", "put"} {
		t.Run(refresh, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := newBinaryPartReadCache()
				c.put(&binaryPartCacheEntry{path: "cold", data: []byte("cold payload"), mime: "image/png"})
				c.put(&binaryPartCacheEntry{path: "active", data: []byte("active payload"), mime: "image/png"})
				c.mu.Lock()
				timer := c.idleTimer
				c.mu.Unlock()
				time.Sleep(20 * time.Second)
				c.mu.Lock()
				entry := c.entries["active"].Value.(*binaryPartCacheEntry)
				c.mu.Unlock()
				if refresh == "get" {
					c.get("active")
				} else {
					c.put(&binaryPartCacheEntry{path: "active", data: entry.data, mime: entry.mime})
				}
				c.mu.Lock()
				sameTimer := c.idleTimer == timer
				c.mu.Unlock()
				if !sameTimer {
					t.Fatal("refresh reset the timer")
				}
				time.Sleep(10 * time.Second)
				synctest.Wait()
				if _, ok := c.get("cold"); ok {
					t.Fatal("idle entry survived its deadline")
				}
				c.mu.Lock()
				active := c.entries["active"] != nil && c.order.Len() == 1 && c.idleTimer != nil
				c.mu.Unlock()
				if !active {
					t.Fatal("expiry removed the recently used entry")
				}
				time.Sleep(20 * time.Second)
				synctest.Wait()
				c.mu.Lock()
				empty := c.bytes == 0 && len(c.entries) == 0 && c.order.Len() == 0 && c.idleTimer == nil
				c.mu.Unlock()
				if !empty || string(entry.data) != "active payload" || entry.mime != "image/png" {
					t.Fatal("empty cache kept its timer or eviction changed a returned payload")
				}
				c.put(&binaryPartCacheEntry{path: "next", data: []byte("next payload"), mime: "image/png"})
				time.Sleep(mediacache.IdleTimeout)
				synctest.Wait()
				if c.residentBytes() != 0 {
					t.Fatal("reinsertion did not start a new expiry")
				}
			})
		})
	}
}

func TestBinaryPartReadCacheConcurrentIdleRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newBinaryPartReadCache()
		const workers = 8
		done := make(chan struct{}, workers)
		for i := range workers {
			go func() {
				path := fmt.Sprintf("attachment-%d", i)
				for range 32 {
					c.put(&binaryPartCacheEntry{path: path, data: []byte("payload"), mime: "image/png"})
					c.get(path)
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
		time.Sleep(mediacache.IdleTimeout)
		synctest.Wait()
		if c.residentBytes() != 0 {
			t.Fatal("concurrent use left idle payloads resident")
		}
	})
}
