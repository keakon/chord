package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

// imageDecoder describes one accepted input format.
type imageDecoder struct {
	name         string
	mimeType     string
	match        func([]byte) bool
	decodeConfig func(io.Reader) (image.Config, error)
	decode       func(io.Reader) (image.Image, error)
}

// imageDecoders is ordered by how common the format is in practice.
var imageDecoders = []imageDecoder{
	{
		name:         "PNG",
		mimeType:     "image/png",
		match:        func(data []byte) bool { return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) },
		decodeConfig: png.DecodeConfig,
		decode:       png.Decode,
	},
	{
		name:         "JPEG",
		mimeType:     "image/jpeg",
		match:        func(data []byte) bool { return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF },
		decodeConfig: jpeg.DecodeConfig,
		decode:       jpeg.Decode,
	},
	{
		name:         "WebP",
		mimeType:     "image/webp",
		match:        matchWebP,
		decodeConfig: webp.DecodeConfig,
		// Only the first frame of an animation is used; animation is never
		// forwarded.
		decode: decodeWebPImage,
	},
	{
		name:     "GIF",
		mimeType: "image/gif",
		match: func(data []byte) bool {
			return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
		},
		decodeConfig: gif.DecodeConfig,
		// Only the first frame is used; animation is never forwarded.
		decode: gif.Decode,
	},
	{
		name:         "BMP",
		mimeType:     "image/bmp",
		match:        func(data []byte) bool { return bytes.HasPrefix(data, []byte("BM")) && len(data) >= 14 },
		decodeConfig: bmp.DecodeConfig,
		decode:       bmp.Decode,
	},
	{
		name:     "TIFF",
		mimeType: "image/tiff",
		match: func(data []byte) bool {
			return bytes.HasPrefix(data, []byte("II*\x00")) || bytes.HasPrefix(data, []byte("MM\x00*"))
		},
		decodeConfig: tiff.DecodeConfig,
		// Only the first page is used; multi-page TIFF is never forwarded.
		decode: tiff.Decode,
	},
}

func matchWebP(data []byte) bool {
	return len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
}

// detectImageFormat identifies the content format. A declared MIME type or
// file extension is never trusted: it only shows up in the error message when
// the content itself is not an accepted format.
func detectImageFormat(data []byte, declaredMime string) (imageDecoder, error) {
	for _, decoder := range imageDecoders {
		if decoder.match(data) {
			return decoder, nil
		}
	}
	if name, ok := rejectedImageFormat(data); ok {
		return imageDecoder{}, fmt.Errorf("%s images are not supported; convert to PNG or JPEG first", name)
	}
	if declaredMime != "" {
		return imageDecoder{}, fmt.Errorf("unrecognized image data (declared %s); supported inputs are PNG, JPEG, WebP, GIF, BMP and TIFF", declaredMime)
	}
	return imageDecoder{}, fmt.Errorf("unrecognized image data; supported inputs are PNG, JPEG, WebP, GIF, BMP and TIFF")
}

// rejectedImageFormat names formats Chord recognizes but cannot decode
// locally, so users get an actionable message instead of a generic failure.
func rejectedImageFormat(data []byte) (string, bool) {
	if brand, ok := isoBMFFBrand(data); ok {
		switch brand {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "HEIC/HEIF", true
		case "avif", "avis":
			return "AVIF", true
		}
	}
	if sniffSVG(data) {
		return "SVG", true
	}
	return "", false
}

// isoBMFFBrand extracts the major brand of an ISO base media file (HEIC,
// HEIF, AVIF and friends share the "ftyp" box layout).
func isoBMFFBrand(data []byte) (string, bool) {
	if len(data) < 12 || !bytes.Equal(data[4:8], []byte("ftyp")) {
		return "", false
	}
	return string(data[8:12]), true
}

// sniffSVG looks for an SVG root element in the leading bytes; text formats
// have no magic number, so this stays a bounded heuristic.
func sniffSVG(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("<svg")) ||
		bytes.HasPrefix(trimmed, []byte("<?xml")) && bytes.Contains(trimmed, []byte("<svg"))
}

// checkImageDimensions validates decoded dimensions and enforces the pixel
// budget with an int64 multiplication so huge header values cannot overflow.
func checkImageDimensions(cfg image.Config) error {
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("image has invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return fmt.Errorf("image dimensions %dx%d exceed the %d megapixel limit",
			cfg.Width, cfg.Height, MaxImagePixels/1_000_000)
	}
	return nil
}
