package imagegen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sync"

	"github.com/keakon/chord/internal/imageutil"

	_ "golang.org/x/image/webp"
)

func ReadBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("image response exceeds %d bytes", max)
	}
	return data, nil
}

// ValidateImage decodes without re-encoding, preserving the original bytes.
func ValidateImage(ctx context.Context, data []byte, declaredMIME string) (Image, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return Image{}, fmt.Errorf("image must contain 1 to %d bytes", MaxImageBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Image{}, fmt.Errorf("decode image dimensions: %w", err)
	}
	mime := "image/" + format
	if format != "png" && format != "jpeg" && format != "webp" {
		return Image{}, fmt.Errorf("unsupported generated image format %q", format)
	}
	if declaredMIME != "" && declaredMIME != mime {
		return Image{}, fmt.Errorf("image MIME does not match content")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxImageDimension || cfg.Height > MaxImageDimension || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return Image{}, fmt.Errorf("generated image exceeds pixel budget")
	}
	digest := sha256.Sum256(data)
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	if !verifiedImage(digest) {
		release, err := imageutil.AcquireDecodeSlot(ctx)
		if err != nil {
			return Image{}, err
		}
		if !verifiedImage(digest) {
			_, _, err = image.Decode(bytes.NewReader(data))
			if err == nil {
				rememberVerifiedImage(digest)
			}
		}
		release()
		if err != nil {
			return Image{}, fmt.Errorf("decode full image: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	return Image{Data: data, MIME: mime, Width: cfg.Width, Height: cfg.Height}, nil
}

// A digest proves that these exact bytes passed a complete decode in this
// process. The fixed-size cache retains no source or decoded image memory.
var verifiedImages = struct {
	sync.Mutex
	entries map[[32]byte]struct{}
	order   [256][32]byte
	next    int
}{entries: make(map[[32]byte]struct{})}

func verifiedImage(digest [32]byte) bool {
	verifiedImages.Lock()
	defer verifiedImages.Unlock()
	_, ok := verifiedImages.entries[digest]
	return ok
}

func rememberVerifiedImage(digest [32]byte) {
	verifiedImages.Lock()
	defer verifiedImages.Unlock()
	if _, ok := verifiedImages.entries[digest]; ok {
		return
	}
	delete(verifiedImages.entries, verifiedImages.order[verifiedImages.next])
	verifiedImages.order[verifiedImages.next] = digest
	verifiedImages.next = (verifiedImages.next + 1) % len(verifiedImages.order)
	verifiedImages.entries[digest] = struct{}{}
}
