package requesttrim

import (
	"fmt"
	"testing"
)

// Crossing the cap halves the memo instead of dropping it wholesale: most
// verdicts survive, everything that remains stays correct, and entries are
// recomputable from immutable inputs.
func TestReductionMemoCapHalvesInsteadOfDropping(t *testing.T) {
	var memo Memo
	for i := range reductionToolCallMemoMaxEntries {
		memo.toolResultDigest(fmt.Sprintf("cap-%d", i), "x")
	}
	want := ContentHash("next output")
	if got := memo.toolResultDigest("next", "next output"); got != want {
		t.Fatal("digest changed after the capacity eviction")
	}
	if len(memo.digests) > reductionToolCallMemoMaxEntries/2+1 {
		t.Fatalf("digest memo has %d entries after eviction, want <=%d", len(memo.digests), reductionToolCallMemoMaxEntries/2+1)
	}
	if len(memo.digests) < 2 {
		t.Fatal("digest memo lost all entries at the cap; eviction must keep half")
	}
	memo.Reset()
	if memo.digests != nil {
		t.Fatal("reset retained digest state")
	}
}
