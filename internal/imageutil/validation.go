package imageutil

import (
	"bytes"
	"crypto/sha256"
	"image"
	"sync"
)

// DecodeImage validates the full pixel stream and remembers successful bytes.
// Callers check their own dimensions and format limits and hold a decode slot.
func DecodeImage(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err == nil && len(data) <= MaxImageSourceBytes {
		rememberVerifiedImage(sha256.Sum256(data))
	}
	return img, format, err
}

// A digest proves that these exact bytes passed a complete decode in this
// process. The fixed-size cache retains no source or decoded image memory.
var verifiedImages = struct {
	sync.Mutex
	entries map[[32]byte]struct{}
	order   [256][32]byte
	next    int
}{entries: make(map[[32]byte]struct{})}

func verifiedImage(digest [32]byte) bool {
	verifiedImages.Lock()
	defer verifiedImages.Unlock()
	_, ok := verifiedImages.entries[digest]
	return ok
}

// HasVerifiedImage reports whether these exact original bytes have passed a
// complete decode in this process. Callers must still check header constraints;
// a hit only avoids repeating pixel validation, and retains no image bytes.
func HasVerifiedImage(data []byte) bool {
	return len(data) != 0 && len(data) <= MaxImageSourceBytes && verifiedImage(sha256.Sum256(data))
}

func rememberVerifiedImage(digest [32]byte) {
	verifiedImages.Lock()
	defer verifiedImages.Unlock()
	if _, ok := verifiedImages.entries[digest]; ok {
		return
	}
	delete(verifiedImages.entries, verifiedImages.order[verifiedImages.next])
	verifiedImages.order[verifiedImages.next] = digest
	verifiedImages.next = (verifiedImages.next + 1) % len(verifiedImages.order)
	verifiedImages.entries[digest] = struct{}{}
}
