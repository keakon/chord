package imagegen

import (
	"context"
	"io"
	"testing"
)

// Request construction and consumption must not duplicate reference payloads.
func BenchmarkBuildImageEditRequest(b *testing.B) {
	refs := make([]Image, 3)
	for i := range refs {
		refs[i] = Image{Data: make([]byte, 8<<20), MIME: "image/png", Width: 3000, Height: 3000}
	}
	target := Target{Preset: PresetOpenAI, BaseURL: "https://image.example/v1/images", Model: "sample-image-model"}
	request := Request{Operation: Edit, Prompt: "Adjust the colors", References: refs}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req, _, err := BuildRequest(context.Background(), target, request, "sample-key")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			b.Fatal(err)
		}
		req.Body.Close()
	}
}
