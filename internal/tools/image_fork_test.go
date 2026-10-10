package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
)

func TestRelocateGeneratedImagesPreservesSavedOperation(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "local"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			backend := imageFixture(t)
			ctx, src, sink := imageToolContext(t, "call-1")
			tool := &GenerateImageTool{Backend: backend, BaseDir: t.TempDir()}
			payload, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"A tree","operation":"generate"}`))
			if err != nil {
				t.Fatal(err)
			}
			parts := sink.Drain()
			msg := message.Message{Role: message.RoleTool, Content: "Display summary", ToolPayload: payload, Parts: parts}
			if native {
				msg = message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Calls: []message.HostedCall{{ID: "image-1", Kind: message.HostedCallKindImageGeneration, Result: json.RawMessage(payload), Parts: parts}}}}
			}
			dst := t.TempDir()
			before, _ := json.Marshal(msg)
			fork, err := RelocateGeneratedImages(ctx, msg, src, dst)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(msg)
			if !bytes.Equal(before, after) || backend.calls.Load() != 1 {
				t.Fatal("fork mutated source message or regenerated image")
			}
			if native {
				payload, parts = string(fork.NativeTools.Calls[0].Result), fork.NativeTools.Calls[0].Parts
			} else {
				if fork.Content != msg.Content || fork.ToolPayload != payload {
					t.Fatal("fork rewrote text or stable image references")
				}
				payload, parts = fork.ToolPayload, fork.Parts
			}
			var summary ImageGenerationSummary
			if err := json.Unmarshal([]byte(payload), &summary); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains([]byte(payload), []byte(src)) || bytes.Contains([]byte(payload), []byte(dst)) {
				t.Fatal("canonical summary depends on an absolute session path")
			}
			if err := os.RemoveAll(src); err != nil {
				t.Fatal(err)
			}
			manifestPath, err := ResolveSessionArtifactPath(dst, strings.TrimPrefix(summary.Manifest, ImageArtifactPrefix))
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var op imageOperation
			if err := json.Unmarshal(data, &op); err != nil || op.OperationID != summary.OperationID || op.State != imagegen.StateSaved {
				t.Fatalf("saved operation lost in fork: %v", err)
			}
			if err := VisitGeneratedOriginals(ctx, payload, parts, func(_ GeneratedImage, img imagegen.Image) error {
				if !bytes.Equal(img.Data, backend.image.Data) {
					t.Fatal("original changed during fork")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadGeneratedImagePartRejectsChangedReceipt(t *testing.T) {
	backend := imageFixture(t)
	dir := t.TempDir()
	ref, err := SaveImageArtifact(t.Context(), dir, backend.image)
	if err != nil {
		t.Fatal(err)
	}
	part := message.ContentPart{Type: message.ContentPartImage, MimeType: backend.image.MIME, ImagePath: filepath.Join(dir, ref.RelPath), ArtifactID: ref.ID}
	for _, invalid := range []message.ContentPart{
		{Type: message.ContentPartText},
		{Type: part.Type, MimeType: part.MimeType, ImagePath: part.ImagePath, ArtifactID: "sha256-invalid"},
		{Type: part.Type, MimeType: "image/jpeg", ImagePath: part.ImagePath, ArtifactID: part.ArtifactID},
	} {
		if _, err := ReadGeneratedImagePart(t.Context(), dir, invalid); err == nil {
			t.Fatal("changed original identity was accepted")
		}
	}
	if err := os.WriteFile(part.ImagePath, []byte("invalid image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGeneratedImagePart(t.Context(), dir, part); err == nil {
		t.Fatal("changed original bytes were accepted")
	}
	msg := message.Message{Role: message.RoleTool, Parts: []message.ContentPart{part}}
	if _, err := RelocateGeneratedImages(t.Context(), msg, dir, t.TempDir()); err == nil {
		t.Fatal("fork accepted changed original")
	}
}
