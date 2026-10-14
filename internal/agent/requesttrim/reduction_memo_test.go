package requesttrim

import (
	"fmt"
	"testing"
)

// A full memo is dropped and rebuilt from immutable inputs on demand.
func TestReductionMemoCapDropsWholesale(t *testing.T) {
	var memo Memo
	for i := range reductionToolCallMemoMaxEntries {
		memo.toolResultDigest(fmt.Sprintf("cap-%d", i), "x")
	}
	want := ContentHash("next output")
	if got := memo.toolResultDigest("next", "next output"); got != want {
		t.Fatal("digest changed after the capacity reset")
	}
	if len(memo.digests) != 1 {
		t.Fatalf("digest memo has %d entries after reset, want 1", len(memo.digests))
	}
	memo.Reset()
	if memo.digests != nil {
		t.Fatal("reset retained digest state")
	}
}
