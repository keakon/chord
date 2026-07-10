package imageutil

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write jpeg: %v", err)
	}
}

func TestReadImageFile(t *testing.T) {
	dir := t.TempDir()

	pngPath := filepath.Join(dir, "p.png")
	writePNG(t, pngPath, 64, 64)
	data, mime, err := ReadImageFile(pngPath)
	if err != nil {
		t.Fatalf("ReadImageFile(png): %v", err)
	}
	// A photographic 64x64 PNG should compress to JPEG and be smaller.
	if mime != "image/png" && mime != "image/jpeg" {
		t.Fatalf("png mime = %q", mime)
	}
	if len(data) == 0 {
		t.Fatal("png data empty")
	}

	jpgPath := filepath.Join(dir, "p.jpg")
	writeJPEG(t, jpgPath, 32, 32)
	_, mime, err = ReadImageFile(jpgPath)
	if err != nil {
		t.Fatalf("ReadImageFile(jpg): %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("jpeg mime = %q, want image/jpeg", mime)
	}

	txtPath := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txtPath, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadImageFile(txtPath); err == nil {
		t.Fatal("expected error for unsupported image format")
	}
}

func TestNormalizeClipboardImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 200, G: 100, B: 50, A: 255})

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	data, mimeType, err := NormalizeClipboardImage(pngBuf.Bytes(), "image/png")
	if err != nil {
		t.Fatalf("NormalizeClipboardImage(png): %v", err)
	}
	if len(data) == 0 || (mimeType != "image/png" && mimeType != "image/jpeg") {
		t.Fatalf("NormalizeClipboardImage(png) = %d bytes, %q", len(data), mimeType)
	}

	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	data, mimeType, err = NormalizeClipboardImage(jpegBuf.Bytes(), "image/jpeg")
	if err != nil {
		t.Fatalf("NormalizeClipboardImage(jpeg): %v", err)
	}
	if !bytes.Equal(data, jpegBuf.Bytes()) || mimeType != "image/jpeg" {
		t.Fatalf("NormalizeClipboardImage(jpeg) changed encoded JPEG")
	}

	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, img); err != nil {
		t.Fatal(err)
	}
	data, mimeType, err = NormalizeClipboardImage(bmpBuf.Bytes(), "image/bmp")
	if err != nil {
		t.Fatalf("NormalizeClipboardImage(bmp): %v", err)
	}
	if len(data) == 0 || (mimeType != "image/png" && mimeType != "image/jpeg") {
		t.Fatalf("NormalizeClipboardImage(bmp) = %d bytes, %q", len(data), mimeType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("normalized BMP output is not decodable: %v", err)
	}

	const tinyWebPBase64 = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="
	webpData, err := base64.StdEncoding.DecodeString(tinyWebPBase64)
	if err != nil {
		t.Fatal(err)
	}
	data, mimeType, err = NormalizeClipboardImage(webpData, "image/webp")
	if err != nil {
		t.Fatalf("NormalizeClipboardImage(webp): %v", err)
	}
	if len(data) == 0 || (mimeType != "image/png" && mimeType != "image/jpeg") {
		t.Fatalf("NormalizeClipboardImage(webp) = %d bytes, %q", len(data), mimeType)
	}
}

func TestNormalizeClipboardImageRejectsInvalidAndOversizedInputs(t *testing.T) {
	if _, _, err := NormalizeClipboardImage([]byte("not an image"), "image/png"); err == nil {
		t.Fatal("expected invalid PNG error")
	}
	if _, _, err := NormalizeClipboardImage([]byte("image"), "image/gif"); err == nil {
		t.Fatal("expected unsupported GIF error")
	}
	if _, _, err := NormalizeClipboardImage(make([]byte, MaxClipboardImageSourceBytes+1), "image/bmp"); err == nil {
		t.Fatal("expected oversized clipboard source error")
	}
}

func TestReadPDFFile(t *testing.T) {
	dir := t.TempDir()

	pdfPath := filepath.Join(dir, "doc.pdf")
	raw := []byte("%PDF-1.7\nfake pdf bytes")
	if err := os.WriteFile(pdfPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	data, mime, err := ReadPDFFile(pdfPath)
	if err != nil {
		t.Fatalf("ReadPDFFile: %v", err)
	}
	if mime != "application/pdf" {
		t.Fatalf("mime = %q, want application/pdf", mime)
	}
	if !bytes.Equal(data, raw) {
		t.Fatal("pdf data not returned verbatim (should not be compressed)")
	}

	// Wrong extension is rejected even with PDF-looking content.
	wrong := filepath.Join(dir, "doc.bin")
	if err := os.WriteFile(wrong, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPDFFile(wrong); err == nil {
		t.Fatal("expected error for non-.pdf extension")
	}

	// Oversize PDF is rejected.
	bigPath := filepath.Join(dir, "big.pdf")
	if err := os.WriteFile(bigPath, make([]byte, MaxPDFBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPDFFile(bigPath); err == nil {
		t.Fatal("expected error for oversize pdf")
	}
}

func TestReadAttachmentFileDispatch(t *testing.T) {
	dir := t.TempDir()

	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, mime, err := ReadAttachmentFile(pdfPath)
	if err != nil || mime != "application/pdf" {
		t.Fatalf("ReadAttachmentFile(pdf) = %q, %v", mime, err)
	}

	pngPath := filepath.Join(dir, "p.png")
	writePNG(t, pngPath, 8, 8)
	_, mime, err = ReadAttachmentFile(pngPath)
	if err != nil {
		t.Fatalf("ReadAttachmentFile(png): %v", err)
	}
	if mime != "image/png" && mime != "image/jpeg" {
		t.Fatalf("png attachment mime = %q", mime)
	}
}
