package llm

import (
	"runtime"
	"testing"
	"weak"
)

func TestPartDigestMemoIsStable(t *testing.T) {
	data := []byte("image-bytes-1")
	first := partDigest(data)
	if second := partDigest(data); second != first {
		t.Fatalf("digest changed between calls: %q vs %q", first, second)
	}
	// A fresh slice over the same bytes is a different payload identity and is
	// hashed independently; it must still agree with the digest of the bytes.
	if other := partDigest([]byte("image-bytes-1")); other != first {
		t.Fatalf("digest of equal bytes = %q, want %q", other, first)
	}
}

// The digest memo keys on payload identity weakly: an entry may not keep a
// multi-MB image resident after the session stops referencing it, or the memo
// would defeat the bounded read cache that re-reads attachments lazily.
func TestPartDigestMemoDoesNotPinPayloads(t *testing.T) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i)
	}
	partDigest(data)

	ref := weak.Make(&data[0])
	data = nil
	for range 3 {
		runtime.GC()
		if ref.Value() == nil {
			return
		}
	}
	t.Fatal("image digest memo retained the payload's backing array")
}
