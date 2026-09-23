// Package imageutil provides image normalization, reading, and size-limit
// helpers shared between the TUI attachment path, MCP clients and the tool
// runtime (e.g. the ViewImage tool). It deliberately has no TUI or tool
// dependencies so every layer can import it without creating an import cycle.
package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// MaxImageBytes is the maximum allowed size of an encoded image in bytes.
const MaxImageBytes = 5 * 1024 * 1024 // 5 MB

// MaxImageSourceBytes bounds encoded data before decoding; MaxImagePixels
// bounds decoded memory. Both budgets apply to every image entry point.
const (
	MaxImageSourceBytes = 32 * 1024 * 1024
	MaxImagePixels      = 40_000_000
)

// MaxImageEdge is the longest edge an image part may have. Normalization only
// shrinks images: a smaller image is never scaled up. It is a conservative
// default quality/compatibility tradeoff, not a hard provider limit.
const MaxImageEdge = 2000

// jpegQuality is the fixed quality used whenever normalization has to
// re-encode pixels as JPEG. Normalization never retries with lower quality.
const jpegQuality = 85

// maxConcurrentDecodes bounds how many images are decoded at once. Entry
// points such as a read-only view_image batch or one MCP result can carry
// several images, and a decoded image can be up to MaxImagePixels, which is
// ~160 MB of RGBA at the limit; serializing most of them keeps the transient
// decoder peak near ~320 MB instead of letting it scale with the batch size,
// without meaningfully slowing the common single-image case.
const maxConcurrentDecodes = 2

var decodeSlots = make(chan struct{}, maxConcurrentDecodes)

// MaxPDFBytes is the maximum allowed PDF attachment size. It is larger than
// MaxImageBytes to match provider PDF limits (e.g. Anthropic's 32 MB).
const MaxPDFBytes = 32 * 1024 * 1024 // 32 MB

// NormalizedImage is the outcome of normalizing one image. Data and MimeType
// are one inseparable result: the MIME type always matches the bytes.
type NormalizedImage struct {
	Data           []byte
	MimeType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
}

// WasScaled reports whether normalization shrank the image.
func (n NormalizedImage) WasScaled() bool {
	return n.Width != n.OriginalWidth || n.Height != n.OriginalHeight
}

// NormalizeImageBytes is NormalizeImage without the size report, for callers
// that only need the provider-ready bytes.
func NormalizeImageBytes(data []byte, declaredMime string) ([]byte, string, error) {
	out, err := NormalizeImage(data, declaredMime)
	if err != nil {
		return nil, "", err
	}
	return out.Data, out.MimeType, nil
}

// NormalizeImage validates image bytes and converts them into a provider-ready
// PNG or JPEG. The format is detected from the content; the declared MIME type
// is only used for diagnostics in error messages.
//
// The processing order is fixed: content detection, size pre-check, pixel
// budget, full decode, EXIF orientation, scaling, encoding, output budget.
// Inputs that are already a conforming PNG/JPEG are returned unchanged, which
// makes normalization idempotent; anything else is decoded and re-encoded
// exactly once.
func NormalizeImage(data []byte, declaredMime string) (NormalizedImage, error) {
	if len(data) == 0 {
		return NormalizedImage{}, fmt.Errorf("image data is empty")
	}
	if len(data) > MaxImageSourceBytes {
		return NormalizedImage{}, fmt.Errorf("image source too large (%.1f MB), max allowed %d MB",
			float64(len(data))/1024/1024, MaxImageSourceBytes/1024/1024)
	}

	decoder, err := detectImageFormat(data, declaredMime)
	if err != nil {
		return NormalizedImage{}, err
	}

	cfg, err := decoder.decodeConfig(bytes.NewReader(data))
	if err != nil {
		return NormalizedImage{}, fmt.Errorf("invalid %s image: %w", decoder.name, err)
	}
	if err := checkImageDimensions(cfg); err != nil {
		return NormalizedImage{}, err
	}

	// Only the pixel-heavy work is bounded: several independent entry points
	// (a read-only tool batch can carry many view_image calls, MCP can return
	// several images at once) would otherwise decode up to MaxImagePixels each
	// in parallel, which is hundreds of megabytes of transient decoder buffers.
	// Acquiring here, after the cheap header checks, keeps rejects immediate.
	decodeSlots <- struct{}{}
	defer func() { <-decodeSlots }()

	img, err := decoder.decode(bytes.NewReader(data))
	if err != nil {
		return NormalizedImage{}, fmt.Errorf("failed to decode %s image: %w", decoder.name, err)
	}

	orientation := 1
	if decoder.mimeType == "image/jpeg" {
		orientation = jpegOrientation(data)
	}

	out := NormalizedImage{
		OriginalWidth:  cfg.Width,
		OriginalHeight: cfg.Height,
		Width:          cfg.Width,
		Height:         cfg.Height,
	}
	needsScale := cfg.Width > MaxImageEdge || cfg.Height > MaxImageEdge

	if orientation == 1 && !needsScale {
		switch decoder.mimeType {
		case "image/png":
			if len(data) <= MaxImageBytes {
				out.Data, out.MimeType = data, "image/png"
				return out, nil
			}
			// A conforming PNG may still be too large to upload; re-encode it
			// once as JPEG instead of silently shipping it.
			encoded, err := encodeJPEG(img)
			if err != nil {
				return NormalizedImage{}, err
			}
			if len(encoded) > MaxImageBytes {
				return NormalizedImage{}, outputTooLargeError(len(encoded))
			}
			out.Data, out.MimeType = encoded, "image/jpeg"
			return out, nil
		case "image/jpeg":
			if len(data) <= MaxImageBytes {
				out.Data, out.MimeType = data, "image/jpeg"
				return out, nil
			}
			// A conforming JPEG may still be too large to upload. Re-encoding
			// it at the same size would only degrade an already-lossy source,
			// so resolution is what gets traded away: the image still reaches
			// the provider instead of being dropped like a byte-budget
			// failure, matching the PNG branch above.
			encoded, scaled, ok := fitJPEGBudget(img)
			if !ok {
				return NormalizedImage{}, outputTooLargeError(len(data))
			}
			out.Data, out.MimeType = encoded, "image/jpeg"
			out.Width, out.Height = scaled.Bounds().Dx(), scaled.Bounds().Dy()
			return out, nil
		}
	}

	if orientation != 1 {
		// Orientation swaps the axes for values 5-8, so the scaled size must be
		// derived from the oriented image rather than the original config.
		img = applyOrientation(img, orientation)
		out.Width, out.Height = img.Bounds().Dx(), img.Bounds().Dy()
	}
	if bounds := img.Bounds(); bounds.Dx() > MaxImageEdge || bounds.Dy() > MaxImageEdge {
		width, height := scaledDimensions(bounds.Dx(), bounds.Dy())
		img = scaleImage(img, width, height)
		out.Width, out.Height = width, height
	}

	if decoder.mimeType == "image/jpeg" {
		// JPEG sources must not gain transparency or lose their lossy
		// character: one decode, all transforms, one JPEG encode.
		encoded, err := encodeJPEG(img)
		if err != nil {
			return NormalizedImage{}, err
		}
		if len(encoded) > MaxImageBytes {
			return NormalizedImage{}, outputTooLargeError(len(encoded))
		}
		out.Data, out.MimeType = encoded, "image/jpeg"
		return out, nil
	}

	// Lossless sources prefer PNG, falling back to JPEG only when the PNG
	// result exceeds the byte budget.
	pngOut, err := encodePNG(img)
	if err != nil {
		return NormalizedImage{}, err
	}
	if len(pngOut) <= MaxImageBytes {
		out.Data, out.MimeType = pngOut, "image/png"
		return out, nil
	}
	jpegOut, err := encodeJPEG(img)
	if err != nil {
		return NormalizedImage{}, err
	}
	if len(jpegOut) <= MaxImageBytes {
		out.Data, out.MimeType = jpegOut, "image/jpeg"
		return out, nil
	}
	return NormalizedImage{}, outputTooLargeError(len(jpegOut))
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode image as PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// encodeJPEG flattens the image onto a white background so that fully or
// partially transparent pixels do not turn into dark blocks, then encodes it
// at the fixed normalization quality.
func encodeJPEG(img image.Image) ([]byte, error) {
	bounds := img.Bounds()
	flat := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	xdraw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
	xdraw.Draw(flat, flat.Bounds(), img, bounds.Min, xdraw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("encode image as JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// jpegBudgetScaleSteps are the longest-edge factors tried in order when a JPEG
// that needs no other transform still exceeds the upload budget. The ladder is
// deliberately short: past the last step a photo is already too small to be
// useful, and reporting the budget failure beats shipping a thumbnail.
var jpegBudgetScaleSteps = []float64{0.85, 0.7, 0.55, 0.4, 0.25}

// fitJPEGBudget shrinks an already-lossy JPEG until its re-encoded size fits
// MaxImageBytes. Every step scales the original rather than the previous step's
// result, so the factors stay exact and repeated resampling does not compound.
func fitJPEGBudget(img image.Image) ([]byte, image.Image, bool) {
	bounds := img.Bounds()
	for _, factor := range jpegBudgetScaleSteps {
		width := max(int(float64(bounds.Dx())*factor), 1)
		height := max(int(float64(bounds.Dy())*factor), 1)
		if width == bounds.Dx() && height == bounds.Dy() {
			continue
		}
		scaled := scaleImage(img, width, height)
		encoded, err := encodeJPEG(scaled)
		if err != nil {
			return nil, nil, false
		}
		if len(encoded) <= MaxImageBytes {
			return encoded, scaled, true
		}
	}
	return nil, nil, false
}

func outputTooLargeError(size int) error {
	return fmt.Errorf("normalized image is %.1f MB, over the %d MB limit; scale it down or re-export as a lower quality JPEG",
		float64(size)/1024/1024, MaxImageBytes/1024/1024)
}

// scaledDimensions returns the size an image must be shrunk to so that its
// longest edge is MaxImageEdge. The short edge keeps the aspect ratio and
// rounds up, and neither edge ever drops below 1 pixel.
func scaledDimensions(width, height int) (int, int) {
	if width <= MaxImageEdge && height <= MaxImageEdge {
		return width, height
	}
	if width >= height {
		return MaxImageEdge, max(ceilDiv(height*MaxImageEdge, width), 1)
	}
	return max(ceilDiv(width*MaxImageEdge, height), 1), MaxImageEdge
}

func scaleImage(img image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	return dst
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 1
	}
	return (a + b - 1) / b
}

// CheckPDFSize returns an error if the PDF exceeds MaxPDFBytes.
func CheckPDFSize(data []byte) error {
	if len(data) > MaxPDFBytes {
		return pdfTooLargeError(len(data))
	}
	return nil
}

func pdfTooLargeError(size int) error {
	return fmt.Errorf("pdf too large (%.1f MB), max allowed %d MB",
		float64(size)/1024/1024, MaxPDFBytes/1024/1024)
}

// ReadImageFile reads and normalizes an image from the given file path.
// Supported inputs are PNG, JPEG, WebP, GIF, BMP and TIFF; the format is
// detected from the content, so a misleading extension is only reported as a
// diagnostic.
func ReadImageFile(path string) (NormalizedImage, error) {
	data, err := readFileBounded(path, MaxImageSourceBytes)
	if err != nil {
		return NormalizedImage{}, err
	}
	return NormalizeImage(data, declaredMimeType(path))
}

// ReadPDFFile reads a PDF from the given file path without compression and
// enforces MaxPDFBytes. It returns the raw bytes and the "application/pdf"
// MIME type.
func ReadPDFFile(path string) ([]byte, string, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".pdf" {
		return nil, "", fmt.Errorf("unsupported document format %q, only PDF is supported", ext)
	}
	data, err := readFileBounded(path, MaxPDFBytes)
	if err != nil {
		return nil, "", err
	}
	return data, "application/pdf", nil
}

// readFileBounded rejects obviously oversized files via Stat and still limits
// the actual read to limit+1 bytes, so the budget never depends on Stat alone.
func readFileBounded(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if info, err := file.Stat(); err == nil && info.Size() > int64(limit) {
		return nil, fileTooLargeError(info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fileTooLargeError(int64(len(data)), limit)
	}
	return data, nil
}

func fileTooLargeError(size int64, limit int) error {
	return fmt.Errorf("file too large (%.1f MB), max allowed %d MB",
		float64(size)/1024/1024, limit/1024/1024)
}

// declaredMimeType maps a file extension to a diagnostic MIME type. The
// extension never decides whether a file is accepted.
func declaredMimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	case ".avif":
		return "image/avif"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

// PDFAppearsEncrypted reports whether the PDF bytes contain an encryption
// dictionary marker. It is intentionally lightweight and used only for UI
// warnings; provider-side parsing remains authoritative.
func PDFAppearsEncrypted(data []byte) bool {
	return bytes.Contains(data, []byte("/Encrypt"))
}

// ReadAttachmentFile reads a user attachment, dispatching by file extension:
// PDF goes through ReadPDFFile (uncompressed, 32 MB limit), everything else
// through ReadImageFile (normalized to PNG/JPEG, 5 MB limit). Non-image
// extensions are rejected by content detection.
func ReadAttachmentFile(path string) ([]byte, string, error) {
	if strings.ToLower(filepath.Ext(path)) == ".pdf" {
		return ReadPDFFile(path)
	}
	image, err := ReadImageFile(path)
	if err != nil {
		return nil, "", err
	}
	return image.Data, image.MimeType, nil
}
