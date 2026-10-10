package tools

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/imagegen"
)

type imageCandidateReceipt struct {
	Image GeneratedImage `json:"image"`
	URL   string         `json:"download_url,omitempty"`
	Saved bool           `json:"saved"`
}

func generatedImageRef(img imagegen.Image, op imageOperation, revised string) GeneratedImage {
	ref := imageArtifactRef(img)
	ref.CreatedByAgent, ref.CreatedByTask = op.AgentID, op.TaskID
	return GeneratedImage{ArtifactRef: ref, Reference: ImageArtifactPrefix + ref.RelPath, Width: img.Width, Height: img.Height, RevisedPrompt: revised}
}

// Every candidate retains its identity until all originals have been verified.
// Download failure cannot hide an unsaved inline candidate or reorder outputs.
func (t *GenerateImageTool) completeImageCandidates(ctx context.Context, dir, path string, op *imageOperation) error {
	if len(op.Candidates) == 0 || len(op.Candidates) > imagegen.MaxImages {
		return fmt.Errorf("completed receipt has no valid candidate collection; do not regenerate")
	}
	images := make([]GeneratedImage, 0, len(op.Candidates))
	for i := range op.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidate := &op.Candidates[i]
		if candidate.Image.Reference == "" {
			if candidate.Saved || candidate.URL == "" {
				return fmt.Errorf("candidate %d has no recoverable original; do not regenerate", i+1)
			}
			img, err := t.Backend.Download(ctx, candidate.URL)
			if err != nil {
				return fmt.Errorf("download generated image: %w", err)
			}
			// Commit the content identity before publishing the original, so a crash
			// after publication resumes from that file without downloading again.
			candidate.Image = generatedImageRef(img, *op, candidate.Image.RevisedPrompt)
			if err := saveImageOperation(ctx, dir, path, *op); err != nil {
				return err
			}
			if _, err := SaveImageArtifact(ctx, dir, img); err != nil {
				return err
			}
		}
		img, err := ReadGeneratedOriginal(ctx, dir, candidate.Image.Reference)
		if err != nil {
			// A URL can still recover an identity whose publication failed. An inline
			// candidate with no file is irrecoverable and never treated as saved.
			if candidate.Saved || candidate.URL == "" {
				return fmt.Errorf("candidate %d original is missing or changed: %w", i+1, err)
			}
			img, err = t.Backend.Download(ctx, candidate.URL)
			if err != nil {
				return err
			}
			if imageDigest(img.Data) != candidate.Image.SHA256 {
				return fmt.Errorf("downloaded original no longer matches receipt")
			}
			if _, err = SaveImageArtifact(ctx, dir, img); err != nil {
				return err
			}
		}
		expected := candidate.Image
		if expected.MimeType != img.MIME || expected.Width != img.Width || expected.Height != img.Height || expected.SizeBytes != int64(len(img.Data)) || expected.SHA256 != imageDigest(img.Data) {
			return fmt.Errorf("candidate %d metadata does not match original", i+1)
		}
		if !candidate.Saved || candidate.URL != "" {
			candidate.Saved = true
			candidate.URL = ""
			if err := saveImageOperation(ctx, dir, path, *op); err != nil {
				return err
			}
		}
		images = append(images, expected)
	}
	op.Images = images
	for _, img := range images {
		if op.OutputFormat != "" && img.MimeType != "image/"+op.OutputFormat {
			op.Warnings = append(op.Warnings, fmt.Sprintf("Requested %s output; provider returned %s. The original was saved unchanged.", op.OutputFormat, img.MimeType))
		}
		if op.Background == "transparent" && img.MimeType == "image/jpeg" {
			op.Warnings = append(op.Warnings, "The provider returned JPEG, which cannot preserve the requested transparency. The original was saved unchanged.")
		}
	}
	if len(images) > 1 {
		op.Warnings = append(op.Warnings, fmt.Sprintf("Provider returned %d images for a one-image request; all originals were saved; billing may reflect the actual count.", len(images)))
	}
	return nil
}
