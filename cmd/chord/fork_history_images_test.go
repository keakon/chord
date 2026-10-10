package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestForkHistoryOwnsGeneratedOriginals(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "local"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "source")
			original := image.NewNRGBA(image.Rect(0, 0, 2, 2))
			original.SetNRGBA(0, 0, color.NRGBA{G: 200, A: 80})
			var b bytes.Buffer
			if err := png.Encode(&b, original); err != nil {
				t.Fatal(err)
			}
			img, err := imagegen.ValidateImage(t.Context(), b.Bytes(), "")
			if err != nil {
				t.Fatal(err)
			}
			ref, err := tools.SaveImageArtifact(t.Context(), src, img)
			if err != nil {
				t.Fatal(err)
			}
			reference := tools.ImageArtifactPrefix + ref.RelPath
			manifestRel := "images/operations/operation-1.json"
			manifestPath := filepath.Join(src, filepath.FromSlash(manifestRel))
			if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
				t.Fatal(err)
			}
			manifest := []byte(`{"state":"saved"}`)
			if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(src, ref.RelPath)
			summary, err := json.Marshal(tools.ImageGenerationSummary{State: imagegen.StateSaved, Manifest: tools.ImageArtifactPrefix + manifestRel, Images: []tools.GeneratedImage{{ArtifactRef: ref, Reference: reference, Width: 2, Height: 2}}})
			if err != nil {
				t.Fatal(err)
			}
			part := message.ContentPart{Type: message.ContentPartImage, MimeType: img.MIME, ImagePath: path, ArtifactID: ref.ID}
			msg := message.Message{Role: message.RoleTool, Content: string(summary) + "\nDelivery complete.", ToolPayload: string(summary), Parts: []message.ContentPart{{Type: message.ContentPartText, Text: string(summary)}, part}}
			if native {
				msg = message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Calls: []message.HostedCall{{ID: "image-1", Kind: message.HostedCallKindImageGeneration, Result: summary, Parts: []message.ContentPart{part}}}}}
			}
			writeForkFixtureJSONL(t, src, "main.pre-compress-1.jsonl", []message.Message{msg})
			writeForkHistoryStatus(t, src, 1, compactionHistoryStatusApplied)
			writeForkFixtureJSONL(t, src, "main.jsonl", []message.Message{msg})
			dst, _, _, err := forkSessionAtHistory(src, filepath.Dir(src), t.TempDir(), 0)
			if err != nil {
				t.Fatal(err)
			}
			forked := readForkFixtureMessages(t, dst)[0]
			if err := os.RemoveAll(src); err != nil {
				t.Fatal(err)
			}
			got, err := tools.ReadGeneratedOriginal(t.Context(), dst, reference)
			if err != nil || !bytes.Equal(got.Data, img.Data) {
				t.Fatalf("fork original differs or depends on source: %v", err)
			}
			var payload string
			var parts []message.ContentPart
			if native {
				payload, parts = string(forked.NativeTools.Calls[0].Result), forked.NativeTools.Calls[0].Parts
			} else {
				payload, parts = forked.ToolPayload, forked.Parts
				if strings.Contains(forked.Content, src) || !strings.HasSuffix(forked.Content, "\nDelivery complete.") {
					t.Fatal("fork content has stale paths or lost diagnostic notes")
				}
			}
			visited := 0
			if err := tools.VisitGeneratedOriginals(t.Context(), payload, parts, func(ref tools.GeneratedImage, original imagegen.Image) error {
				visited++
				if ref.Path != "" || !bytes.Equal(original.Data, img.Data) {
					t.Fatal("delivery does not use fork original")
				}
				return nil
			}); err != nil || visited != 1 {
				t.Fatalf("fork delivery: visited=%d err=%v", visited, err)
			}
			gotManifest, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(manifestRel)))
			if err != nil || !bytes.Equal(gotManifest, manifest) {
				t.Fatalf("fork lost saved manifest: %v", err)
			}
		})
	}
}
