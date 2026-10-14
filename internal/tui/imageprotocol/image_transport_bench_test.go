package imageprotocol

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tui/imagecache"
)

// Cold inline preparation includes decode, PNG conversion and protocol framing.
func BenchmarkInlineImageTransport(b *testing.B) {
	part := benchmarkInlineImagePart(b)

	b.Cleanup(resetImageCache)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		resetImageCache()
		if _, _, err := KittyInline(imageCache, part, 48, 12, false); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkInlineImagePart(b *testing.B) imagecache.Source {
	b.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3000, 1500))
	for y := range 1500 {
		for x := range 3000 {
			i := y*img.Stride + x*4
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(x/8), byte(y/8), byte((x+y)/16), 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		b.Fatal(err)
	}

	return imagecache.Source{Data: buf.Bytes(), MIME: "image/jpeg"}
}

// Repeated transport preparation models an iTerm2 redraw or a Kitty placement
// rebuild. Short visits reuse encoded bytes; revisits after expiry decode once.
func BenchmarkInlineImageCacheLifecycle(b *testing.B) {
	part := benchmarkInlineImagePart(b)
	for _, expired := range []bool{false, true} {
		name := "Warm"
		if expired {
			name = "AfterExpiry"
		}
		b.Run(name, func(b *testing.B) {
			resetImageCache()
			b.Cleanup(resetImageCache)
			if _, _, err := KittyInline(imageCache, part, 48, 12, false); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if expired {
					imageCache.ReleaseIdle(time.Now().Add(imagecache.PayloadIdleTTL))
				}
				if _, _, err := KittyInline(imageCache, part, 48, 12, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
