package agent

import (
	"strings"
	"testing"
)

// The cache is bounded by resident bytes and evicts least-recently-used first,
// with a hit refreshing recency.
func TestBinaryPartReadCacheEvictsLeastRecentlyUsedWithinBudget(t *testing.T) {
	c := newBinaryPartReadCache()
	c.budget = 30
	blob := func(n int) []byte { return []byte(strings.Repeat("x", n)) }

	c.put("a", blob(10))
	c.put("b", blob(10))
	c.put("c", blob(10))
	for _, path := range []string{"a", "b", "c"} {
		if _, ok := c.get(path); !ok {
			t.Fatalf("%s evicted while the cache was within budget", path)
		}
	}

	// "a" is now the least recently used ("b" and "c" were read after it in the
	// loop above, and reading "a" first made it the oldest again).
	c.get("b")
	c.get("c")
	c.put("d", blob(10))
	if _, ok := c.get("a"); ok {
		t.Fatal("a survived, want the least recently used entry evicted")
	}
	for _, path := range []string{"b", "c", "d"} {
		if _, ok := c.get(path); !ok {
			t.Fatalf("%s evicted, want it retained", path)
		}
	}
	if c.bytes != 30 {
		t.Fatalf("resident bytes = %d, want 30", c.bytes)
	}
	if c.order.Len() != len(c.entries) {
		t.Fatalf("order/entries out of sync: %d vs %d", c.order.Len(), len(c.entries))
	}
}

// A blob larger than the whole budget is not cached, and does not evict the
// entries that do fit.
func TestBinaryPartReadCacheRejectsOversizedPayload(t *testing.T) {
	c := newBinaryPartReadCache()
	c.budget = 10
	c.put("small", []byte("12345"))
	c.put("huge", []byte(strings.Repeat("x", 100)))
	if _, ok := c.get("huge"); ok {
		t.Fatal("oversized payload cached, want rejected")
	}
	if _, ok := c.get("small"); !ok {
		t.Fatal("oversized payload evicted the entry that fits")
	}
}

// Re-putting a known path refreshes recency without double-counting its bytes.
func TestBinaryPartReadCacheRePutDoesNotDoubleCount(t *testing.T) {
	c := newBinaryPartReadCache()
	c.put("a", []byte("1234567890"))
	before := c.bytes
	c.put("a", []byte("1234567890"))
	if c.bytes != before {
		t.Fatalf("resident bytes = %d after re-put, want %d", c.bytes, before)
	}
	if c.order.Len() != 1 {
		t.Fatalf("order length = %d after re-put, want 1", c.order.Len())
	}
}
