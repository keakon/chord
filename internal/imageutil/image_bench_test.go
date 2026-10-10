package imageutil

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// Covers decode, model-input resize and encode, including the shared admission.
func BenchmarkNormalizeLargeImage(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 3000, 3000))
	for y := range 3000 {
		for x := range 3000 {
			i := y*img.Stride + x*4
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(x/8), byte(y/8), byte((x+y)/16), 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	data := buf.Bytes()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := NormalizeImage(b.Context(), data, "image/png"); err != nil {
			b.Fatal(err)
		}
	}
}
