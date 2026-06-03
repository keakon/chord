package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
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
