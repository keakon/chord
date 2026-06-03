// Package imageutil provides image reading, compression, and size-limit
// helpers shared between the TUI attachment path and the tool runtime
// (e.g. the ViewImage tool). It deliberately has no TUI or tool dependencies
// so both layers can import it without creating an import cycle.
package imageutil

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// MaxImageBytes is the maximum allowed image size after any compression.
const MaxImageBytes = 5 * 1024 * 1024 // 5 MB

// MaxPDFBytes is the maximum allowed PDF attachment size. It is larger than
// MaxImageBytes to match provider PDF limits (e.g. Anthropic's 32 MB).
const MaxPDFBytes = 32 * 1024 * 1024 // 32 MB

// CompressIfPNG attempts to convert a PNG to JPEG (quality 85) when that
// yields a smaller file. For non-PNG inputs the data is returned unchanged.
func CompressIfPNG(data []byte, mimeType string) ([]byte, string) {
	if mimeType != "image/png" {
		return data, mimeType
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		// Not a decodable PNG – keep as-is.
		return data, mimeType
	}

	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		return data, mimeType
	}

	if jpegBuf.Len() < len(data) {
		return jpegBuf.Bytes(), "image/jpeg"
	}
	return data, mimeType
}

// CheckImageSize returns an error if the image exceeds MaxImageBytes.
func CheckImageSize(data []byte) error {
	if len(data) > MaxImageBytes {
		return fmt.Errorf("image too large (%.1f MB), max allowed %d MB",
			float64(len(data))/1024/1024, MaxImageBytes/1024/1024)
	}
	return nil
}

// ReadImageFile reads an image from the given file path.
// Supported formats: PNG, JPEG. PNG inputs are compressed to JPEG when smaller.
func ReadImageFile(path string) ([]byte, string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	var mimeType string
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	default:
		return nil, "", fmt.Errorf("unsupported image format %q, only PNG/JPEG are supported", ext)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	data, mimeType = CompressIfPNG(data, mimeType)
	if err := CheckImageSize(data); err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

// ReadPDFFile reads a PDF from the given file path without compression and
// enforces MaxPDFBytes. It returns the raw bytes and the "application/pdf"
// MIME type.
func ReadPDFFile(path string) ([]byte, string, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".pdf" {
		return nil, "", fmt.Errorf("unsupported document format %q, only PDF is supported", ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(data) > MaxPDFBytes {
		return nil, "", fmt.Errorf("pdf too large (%.1f MB), max allowed %d MB",
			float64(len(data))/1024/1024, MaxPDFBytes/1024/1024)
	}
	return data, "application/pdf", nil
}

// PDFAppearsEncrypted reports whether the PDF bytes contain an encryption
// dictionary marker. It is intentionally lightweight and used only for UI
// warnings; provider-side parsing remains authoritative.
func PDFAppearsEncrypted(data []byte) bool {
	return bytes.Contains(data, []byte("/Encrypt"))
}

// ReadAttachmentFile reads a user attachment, dispatching by file extension:
// PNG/JPEG go through ReadImageFile (PNG compressed to JPEG when smaller, 5 MB
// limit), and PDF goes through ReadPDFFile (uncompressed, 32 MB limit). Other
// extensions are rejected.
func ReadAttachmentFile(path string) ([]byte, string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return ReadPDFFile(path)
	default:
		return ReadImageFile(path)
	}
}
