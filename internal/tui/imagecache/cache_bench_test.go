package imagecache

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func benchmarkLargePNGPart(b *testing.B) Source {
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
	return Source{Data: buf.Bytes(), MIME: "image/png"}
}

// Lookup is used by layout and inline request capture before background work.
func BenchmarkImageRuntimeCacheLookup(b *testing.B) {
	part := benchmarkLargePNGPart(b)
	for _, cold := range []bool{false, true} {
		name := "Warm"
		if cold {
			name = "Cold"
		}
		b.Run(name, func(b *testing.B) {
			b.Cleanup(resetImageRuntimeCache)
			if _, err := imageRuntimeCache.Lookup(part, false); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if cold {
					b.StopTimer()
					resetImageRuntimeCache()
					resetImageRuntimeKeyMemo()
					b.StartTimer()
				}
				if _, err := imageRuntimeCache.Lookup(part, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func resetImageRuntimeKeyMemo() {
	imageRuntimeKeyMemo.mu.Lock()
	clear(imageRuntimeKeyMemo.entries)
	imageRuntimeKeyMemo.order.Init()
	imageRuntimeKeyMemo.mu.Unlock()
}
