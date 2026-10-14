package imageprotocol

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/keakon/chord/internal/tui/imagecache"
)

func benchmarkLargePNGPart(b *testing.B) imagecache.Source {
	b.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1600, 800))
	value := uint32(1)
	for i := range img.Pix {
		value = value*1664525 + 1013904223
		img.Pix[i] = byte(value >> 24)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	return imagecache.Source{Data: buf.Bytes(), MIME: "image/png"}
}

// Framing reuses prepared payloads, as fullscreen and iTerm2 redraws do.
func BenchmarkImageProtocolFraming(b *testing.B) {
	part := benchmarkLargePNGPart(b)
	entry, err := imageCache.Lookup(part, false)
	if err != nil {
		b.Fatal(err)
	}
	if _, _, err := entry.Base64PNG(part); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(resetImageCache)
	for _, backend := range []string{"Kitty", "ITerm2"} {
		b.Run(backend, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if backend == "Kitty" {
					_, err = kittyTransmit(imageCache, part, 1, false)
				} else {
					_, err = ITerm2(imageCache, part, "sample.png", 48, 12, false)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
