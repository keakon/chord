package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
)

// ReadGeneratedOriginal accepts only immutable image refs, never arbitrary paths.
func ReadGeneratedOriginal(ctx context.Context, sessionDir, reference string) (imagegen.Image, error) {
	if err := ctx.Err(); err != nil {
		return imagegen.Image{}, fmt.Errorf("read generated image: %w", err)
	}
	rel := strings.TrimPrefix(reference, ImageArtifactPrefix)
	name := filepath.Base(rel)
	if filepath.ToSlash(filepath.Dir(rel)) != "images" || !strings.HasPrefix(name, "sha256-") {
		return imagegen.Image{}, fmt.Errorf("expected a generated image artifact reference")
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, "sha256-"), filepath.Ext(name))
	if len(digest) != 64 {
		return imagegen.Image{}, fmt.Errorf("invalid image artifact digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return imagegen.Image{}, fmt.Errorf("invalid image artifact digest")
	}
	path, err := ResolveSessionArtifactPath(sessionDir, rel)
	if err != nil {
		return imagegen.Image{}, err
	}
	f, err := openImageSnapshot(sessionDir, path)
	if err != nil {
		return imagegen.Image{}, fmt.Errorf("open generated image: %w", err)
	}
	defer f.Close()
	data, err := imagegen.ReadBounded(f, imagegen.MaxImageBytes)
	if err != nil {
		return imagegen.Image{}, err
	}
	if imageDigest(data) != digest {
		return imagegen.Image{}, fmt.Errorf("generated image digest changed")
	}
	return imagegen.ValidateImage(ctx, data, "")
}

// VisitGeneratedOriginals resolves only image references in the tool's saved summary.
// The event's existing image part supplies the session root to ACP consumers.
func VisitGeneratedOriginals(ctx context.Context, payload string, parts []message.ContentPart, visit func(GeneratedImage, imagegen.Image) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("deliver generated images: %w", err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(payload), &summary); err != nil {
		return fmt.Errorf("decode generated image summary: %w", err)
	}
	var sessionDir string
	for _, part := range parts {
		if part.ArtifactID != "" && part.ImagePath != "" {
			sessionDir = filepath.Dir(filepath.Dir(part.ImagePath))
			break
		}
	}
	if sessionDir == "" {
		return fmt.Errorf("generated image session is unavailable")
	}
	if len(summary.Images) > imagegen.MaxImages {
		return fmt.Errorf("too many generated image references")
	}
	for _, ref := range summary.Images {
		img, err := ReadGeneratedOriginal(ctx, sessionDir, ref.Reference)
		if err != nil {
			return err
		}
		if err := visit(ref, img); err != nil {
			return err
		}
	}
	return nil
}

// ReadGeneratedImagePart resolves a canonical original, without preview
// normalization. Its content identity and MIME must still match the receipt.
func ReadGeneratedImagePart(ctx context.Context, sessionDir string, part message.ContentPart) (imagegen.Image, error) {
	if part.Type != message.ContentPartImage || part.ArtifactID == "" {
		return imagegen.Image{}, fmt.Errorf("expected a generated image part")
	}
	// The content identity is authoritative within the captured session. Resolve
	// it there even when a restored path uses a different spelling of that root.
	rel := filepath.Join("images", part.ArtifactID+imagegen.Extension(part.MimeType))
	img, err := ReadGeneratedOriginal(ctx, sessionDir, ImageArtifactPrefix+filepath.ToSlash(rel))
	if err != nil {
		return imagegen.Image{}, err
	}
	if part.MimeType != img.MIME {
		return imagegen.Image{}, fmt.Errorf("generated image part does not match original")
	}
	return img, nil
}
