package tui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"testing"
)

func TestImageRuntimeCacheReusesDecodeConfig(t *testing.T) {
	resetImageRuntimeCache()
	origDecodeConfig := imageCacheDecodeConfig
	defer func() {
		imageCacheDecodeConfig = origDecodeConfig
		resetImageRuntimeCache()
	}()

	pngData := makeTestPNG(t)
	part := BlockImagePart{MimeType: "image/png", Data: pngData}
	calls := 0
	imageCacheDecodeConfig = func(r io.Reader) (image.Config, string, error) {
		calls++
		return origDecodeConfig(r)
	}

	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		t.Fatalf("imageRuntimeEntryForPart() error = %v", err)
	}
	for range 2 {
		cfg, format, err := entry.decodeConfig(part)
		if err != nil {
			t.Fatalf("decodeConfig() error = %v", err)
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || format == "" {
			t.Fatalf("decodeConfig() = %#v, %q", cfg, format)
		}
	}
	if calls != 1 {
		t.Fatalf("decode config calls = %d, want 1", calls)
	}
}

func TestImageRuntimeCacheReusesTransportPNGAndBase64(t *testing.T) {
	resetImageRuntimeCache()
	origDecode := imageCacheDecode
	origEncodePNG := imageCacheEncodePNG
	defer func() {
		imageCacheDecode = origDecode
		imageCacheEncodePNG = origEncodePNG
		resetImageRuntimeCache()
	}()

	pngData := makeTestPNG(t)
	part := BlockImagePart{MimeType: "image/png", Data: pngData}
	decodeCalls := 0
	encodeCalls := 0
	imageCacheDecode = func(r io.Reader) (image.Image, string, error) {
		decodeCalls++
		return origDecode(r)
	}
	imageCacheEncodePNG = func(w io.Writer, m image.Image) error {
		encodeCalls++
		return origEncodePNG(w, m)
	}

	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		t.Fatalf("imageRuntimeEntryForPart() error = %v", err)
	}
	png1, w1, h1, err := entry.transportPNG(part)
	if err != nil {
		t.Fatalf("transportPNG() error = %v", err)
	}
	png2, w2, h2, err := entry.transportPNG(part)
	if err != nil {
		t.Fatalf("second transportPNG() error = %v", err)
	}
	if !bytes.Equal(png1, png2) || w1 != w2 || h1 != h2 {
		t.Fatal("cached transport PNG result mismatch")
	}
	base641, size1, err := entry.base64TransportPNG(part)
	if err != nil {
		t.Fatalf("base64TransportPNG() error = %v", err)
	}
	base642, size2, err := entry.base64TransportPNG(part)
	if err != nil {
		t.Fatalf("second base64TransportPNG() error = %v", err)
	}
	if base641 != base642 || size1 != size2 {
		t.Fatal("cached base64 transport result mismatch")
	}
	if decodeCalls != 1 {
		t.Fatalf("decode calls = %d, want 1", decodeCalls)
	}
	if encodeCalls != 1 {
		t.Fatalf("png encode calls = %d, want 1", encodeCalls)
	}
}

func TestThumbnailPreviewDataCachesDiskReads(t *testing.T) {
	resetImageRuntimeCache()
	origReadFile := imageCacheReadFile
	defer func() {
		imageCacheReadFile = origReadFile
		resetImageRuntimeCache()
	}()

	dir := t.TempDir()
	path := dir + "/sample.png"
	pngData := makeTestPNG(t)
	calls := 0
	imageCacheReadFile = func(name string) ([]byte, error) {
		calls++
		if name != path {
			return nil, fmt.Errorf("unexpected path %q", name)
		}
		return pngData, nil
	}
	part := BlockImagePart{MimeType: "image/png", ImagePath: path}

	for range 2 {
		got, err := thumbnailPreviewData(part)
		if err != nil {
			t.Fatalf("thumbnailPreviewData() error = %v", err)
		}
		if !bytes.Equal(got, pngData) {
			t.Fatal("thumbnailPreviewData() returned unexpected bytes")
		}
	}
	if calls != 1 {
		t.Fatalf("read file calls = %d, want 1", calls)
	}
}

func TestImageRuntimeCacheMemoizesReadErrors(t *testing.T) {
	resetImageRuntimeCache()
	origReadFile := imageCacheReadFile
	defer func() {
		imageCacheReadFile = origReadFile
		resetImageRuntimeCache()
	}()

	calls := 0
	imageCacheReadFile = func(name string) ([]byte, error) {
		calls++
		return nil, errors.New("boom")
	}
	part := BlockImagePart{MimeType: "image/png", ImagePath: "/tmp/missing.png"}

	for range 2 {
		_, err := thumbnailPreviewData(part)
		if err == nil || err.Error() != "read image file: boom" {
			t.Fatalf("thumbnailPreviewData() error = %v, want wrapped boom", err)
		}
	}
	if calls != 1 {
		t.Fatalf("read error calls = %d, want 1", calls)
	}
}
