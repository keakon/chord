package imageutil

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"testing"
)

func TestImageValidationCacheBoundedConcurrent(t *testing.T) {
	var workers sync.WaitGroup
	for i := range 512 {
		workers.Go(func() {
			var data [8]byte
			binary.LittleEndian.PutUint64(data[:], uint64(i))
			digest := sha256.Sum256(data[:])
			rememberVerifiedImage(digest)
			verifiedImage(digest)
		})
	}
	workers.Wait()
	verifiedImages.Lock()
	size := len(verifiedImages.entries)
	verifiedImages.Unlock()
	if size != len(verifiedImages.order) {
		t.Fatalf("cache size %d exceeds or underfills capacity", size)
	}
}
