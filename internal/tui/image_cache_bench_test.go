package tui

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func benchmarkLargePNGPart(b *testing.B) BlockImagePart {
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
	return BlockImagePart{Data: buf.Bytes(), MimeType: "image/png", FileName: "sample.png"}
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
			if _, err := imageRuntimeEntryForPart(part); err != nil {
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
				if _, err := imageRuntimeEntryForPart(part); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Framing reuses prepared payloads, as fullscreen and iTerm2 redraws do.
func BenchmarkImageProtocolFraming(b *testing.B) {
	part := benchmarkLargePNGPart(b)
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		b.Fatal(err)
	}
	if _, _, err := entry.base64TransportPNG(part); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(resetImageRuntimeCache)
	for _, backend := range []string{"Kitty", "ITerm2"} {
		b.Run(backend, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if backend == "Kitty" {
					_, err = encodeKittyTransmit(part, 1)
				} else {
					_, err = iterm2ViewerSequence(part, 48, 12)
				}
				if err != nil {
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
