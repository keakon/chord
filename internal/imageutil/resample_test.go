package imageutil

import (
	"image"
	"image/color"
	"math"
	"testing"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func TestResamplerPreservesCubicQualityAndAlpha(t *testing.T) {
	for _, name := range []string{"text", "gradient", "alpha"} {
		t.Run(name, func(t *testing.T) {
			src := image.NewNRGBA(image.Rect(0, 0, 600, 300))
			for y := range 300 {
				for x := range 600 {
					c := color.NRGBA{R: byte(x / 3), G: byte(y / 2), B: byte((x + y) / 4), A: 255}
					if name == "text" {
						c = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
					}
					if name == "alpha" {
						c.A = byte(x % 256)
					}
					src.SetNRGBA(x, y, c)
				}
			}
			if name == "text" {
				d := font.Drawer{Dst: src, Src: image.Black, Face: basicfont.Face7x13}
				for y := 15; y < 300; y += 20 {
					d.Dot = fixed.P(10, y)
					d.DrawString("Readable text, 0123456789: symbols / - + =")
				}
			}
			baseline := image.NewRGBA(image.Rect(0, 0, 200, 100))
			draw.CatmullRom.Scale(baseline, baseline.Bounds(), src, src.Bounds(), draw.Src, nil)
			got := scaleImage(src, 200, 100)
			var squared float64
			for y := range 100 {
				for x := range 200 {
					r1, g1, b1, a1 := baseline.At(x, y).RGBA()
					r2, g2, b2, a2 := got.At(x, y).RGBA()
					for _, pair := range [][2]uint32{{r1, r2}, {g1, g2}, {b1, b2}, {a1, a2}} {
						delta := (float64(pair[0]) - float64(pair[1])) / 257
						squared += delta * delta
					}
				}
			}
			rmse := math.Sqrt(squared / (200 * 100 * 4))
			t.Logf("premultiplied channel RMSE against previous cubic resampler: %.3f/255", rmse)
			if rmse > 3 {
				t.Fatalf("cubic quality changed: RMSE %.3f", rmse)
			}
		})
	}
}

func TestImagePixelBudgetDoesNotOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if err := CheckImageDimensions(image.Config{Width: maxInt, Height: maxInt}); err == nil {
		t.Fatal("huge dimensions bypassed the pixel budget")
	}
	if err := CheckImageDimensions(image.Config{Width: MaxImagePixels, Height: 1}); err != nil {
		t.Fatalf("pixel budget boundary rejected: %v", err)
	}
}
