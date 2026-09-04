package tools

import "testing"

// TestRestAfterFirstToleratesEmptyDiffs pins the guard on the closest-match
// diagnostic. The renderer prints Diffs[0] as the first mismatch and lists the
// remainder, but the diagnostic normalizer is more tolerant than the matcher
// that rejected the block, so a "closest" window can legitimately carry no
// differing line at all. Indexing the slice directly panicked there.
func TestRestAfterFirstToleratesEmptyDiffs(t *testing.T) {
	if got := restAfterFirst([]editDiffLine(nil)); got != nil {
		t.Fatalf("restAfterFirst(nil) = %v, want nil", got)
	}
	if got := restAfterFirst([]editDiffLine{}); got != nil {
		t.Fatalf("restAfterFirst(empty) = %v, want nil", got)
	}
	one := []editDiffLine{{FileLine: 1}}
	if got := restAfterFirst(one); got != nil {
		t.Fatalf("restAfterFirst(single) = %v, want nil (the first diff is rendered separately)", got)
	}
	two := []editDiffLine{{FileLine: 1}, {FileLine: 2}}
	got := restAfterFirst(two)
	if len(got) != 1 || got[0].FileLine != 2 {
		t.Fatalf("restAfterFirst(two) = %v, want only the second diff", got)
	}
}
