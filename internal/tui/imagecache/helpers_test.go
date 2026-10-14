package imagecache

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

var imageRuntimeCache = New()

func resetImageRuntimeCache() { imageRuntimeCache = New() }
func (e *Entry) transportPNG(part Source) ([]byte, int, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ensureTransportPNGUnlocked(part)
}
func cachedRaw(part Source) ([]byte, error) {
	e, err := imageRuntimeCache.Lookup(part, false)
	if err != nil {
		return nil, err
	}
	return e.Raw(part)
}
func cacheTransport(part Source) (string, error) {
	e, err := imageRuntimeCache.Lookup(part, false)
	if err != nil {
		return "", err
	}
	s, _, err := e.Base64PNG(part)
	return s, err
}
func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
