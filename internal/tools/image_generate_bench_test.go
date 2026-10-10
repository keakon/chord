package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
)

// Includes generation response validation, session persistence, summary
// verification and original delivery. Fixture encoding is outside the timer.
func BenchmarkImageGenerationDelivery(b *testing.B) {
	benchmarkImageGenerationDelivery(b, false)
}

func BenchmarkImageGenerationDeliveryCold(b *testing.B) {
	benchmarkImageGenerationDelivery(b, true)
}

func benchmarkImageGenerationDelivery(b *testing.B, cold bool) {
	for _, edge := range []int{1024, 2048} {
		b.Run(strconv.Itoa(edge), func(b *testing.B) {
			imageData := image.NewNRGBA(image.Rect(0, 0, edge, edge))
			for y := range edge {
				for x := range edge {
					imageData.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
				}
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, imageData); err != nil {
				b.Fatal(err)
			}
			data := buf.Bytes()
			target, err := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1", "")
			if err != nil {
				b.Fatal(err)
			}
			backend := &imageBackendFixture{target: target}
			backend.run = func(ctx context.Context, _ imagegen.Request, before func() error) (*imagegen.Result, error) {
				if err := before(); err != nil {
					return nil, err
				}
				img, err := imagegen.ValidateImage(ctx, data, "")
				if err != nil {
					return nil, err
				}
				return &imagegen.Result{Images: []imagegen.Candidate{{Image: img}}}, nil
			}
			tool := &GenerateImageTool{Backend: backend}
			dir := b.TempDir()
			base := WithSessionDir(b.Context(), dir)
			args := json.RawMessage(`{"prompt":"A landscape","operation":"generate"}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := range b.N {
				if cold {
					b.StopTimer()
					// A different valid bitmap per operation exercises first-image
					// decoding. Its encoding and identity construction are untimed.
					marker := sha256.Sum256([]byte(dir + strconv.Itoa(i)))
					for j := range 8 {
						imageData.SetNRGBA(j, 0, color.NRGBA{R: marker[j*4], G: marker[j*4+1], B: marker[j*4+2], A: marker[j*4+3]})
					}
					buf.Reset()
					if err := png.Encode(&buf, imageData); err != nil {
						b.Fatal(err)
					}
					data = buf.Bytes()
					b.StartTimer()
				}
				sink := &ImageCollector{}
				ctx := WithToolCallID(WithImageSink(base, sink), strconv.Itoa(i))
				result, err := tool.Execute(ctx, args)
				if err != nil {
					b.Fatal(err)
				}
				if err := VisitGeneratedOriginals(ctx, result, sink.Drain(), func(_ GeneratedImage, _ imagegen.Image) error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
