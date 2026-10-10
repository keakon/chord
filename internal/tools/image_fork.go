package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
)

func hasGeneratedImageParts(parts []message.ContentPart) bool {
	return slices.ContainsFunc(parts, func(part message.ContentPart) bool {
		return part.Type == message.ContentPartImage && part.ArtifactID != ""
	})
}

// HasGeneratedImages includes originals held inside native tool receipts.
func HasGeneratedImages(msg message.Message) bool {
	if hasGeneratedImageParts(msg.Parts) {
		return true
	}
	if msg.NativeTools != nil {
		for _, call := range msg.NativeTools.Calls {
			if hasGeneratedImageParts(call.Parts) {
				return true
			}
		}
	}
	return false
}

// RelocateGeneratedImages makes a fork own its originals and saved summaries.
// Content-addressed names and bytes stay intact, including native receipts.
// Ordinary attachments are left to the existing session persistence path.
func RelocateGeneratedImages(ctx context.Context, msg message.Message, srcDir, dstDir string) (message.Message, error) {
	if !HasGeneratedImages(msg) {
		return msg, nil
	}
	relocateParts := func(parts []message.ContentPart) ([]message.ContentPart, error) {
		parts = slices.Clone(parts)
		for i, part := range parts {
			if part.Type != message.ContentPartImage || part.ArtifactID == "" {
				continue
			}
			img, err := ReadGeneratedImagePart(ctx, srcDir, part)
			if err != nil {
				return nil, err
			}
			ref, err := SaveImageArtifact(ctx, dstDir, img)
			if err != nil {
				return nil, err
			}
			parts[i].Data = nil
			parts[i].ImagePath = filepath.Join(dstDir, filepath.FromSlash(ref.RelPath))
			parts[i].FileName = filepath.Base(ref.RelPath)
			parts[i].DataBytes = ref.SizeBytes
		}
		return parts, nil
	}
	copySummary := func(raw string) error {
		var summary ImageGenerationSummary
		if err := json.Unmarshal([]byte(raw), &summary); err != nil {
			return fmt.Errorf("decode fork image summary: %w", err)
		}
		if summary.State != imagegen.StateSaved || len(summary.Images) == 0 {
			return fmt.Errorf("fork image summary has no saved originals")
		}
		for _, img := range summary.Images {
			if _, err := ReadGeneratedOriginal(ctx, dstDir, img.Reference); err != nil {
				return err
			}
		}
		if summary.Manifest != "" {
			rel := strings.TrimPrefix(summary.Manifest, ImageArtifactPrefix)
			source, err := ResolveSessionArtifactPath(srcDir, rel)
			if err != nil {
				return err
			}
			f, err := openImageSnapshot(srcDir, source)
			if err != nil {
				return fmt.Errorf("open fork image manifest: %w", err)
			}
			data, readErr := imagegen.ReadBounded(f, imagegen.MaxImageBytes)
			_ = f.Close()
			if readErr != nil {
				return readErr
			}
			destination := filepath.Join(dstDir, filepath.FromSlash(rel))
			if err := privatefs.EnsureDir(dstDir, filepath.Dir(destination)); err != nil {
				return err
			}
			if err := privatefs.WriteFileSynced(dstDir, destination, data); err != nil {
				return err
			}
		}
		if _, _, err := SaveImmutableResult(dstDir, "image_generation", []byte(raw)); err != nil {
			return err
		}
		return nil
	}
	if hasGeneratedImageParts(msg.Parts) {
		original := msg.ToolPayload
		if original == "" {
			original = msg.Content
		}
		parts, err := relocateParts(msg.Parts)
		if err != nil {
			return msg, err
		}
		if err := copySummary(original); err != nil {
			return msg, err
		}
		msg.Parts = parts
	}
	if msg.NativeTools != nil {
		msg.NativeTools = msg.NativeTools.Clone()
		for i := range msg.NativeTools.Calls {
			call := &msg.NativeTools.Calls[i]
			if !hasGeneratedImageParts(call.Parts) {
				continue
			}
			parts, err := relocateParts(call.Parts)
			if err != nil {
				return msg, err
			}
			if err := copySummary(string(call.Result)); err != nil {
				return msg, err
			}
			call.Parts = parts
		}
	}
	return msg, nil
}
