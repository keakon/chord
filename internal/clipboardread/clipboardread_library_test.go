//go:build !darwin

package clipboardread

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	clipboard "golang.design/x/clipboard"
	"golang.org/x/image/bmp"
)

// stubClipboard replaces the native clipboard boundary for one test, so the
// probe order can be exercised without touching the real clipboard.
func stubClipboard(t *testing.T, formats []clipboard.Format, read func(context.Context, clipboard.Format) ([]byte, error)) {
	t.Helper()
	origInit := clipboardInit
	origFormats := clipboardFormats
	origRead := clipboardRead
	clipboardInit = func() error { return nil }
	clipboardFormats = func(context.Context) ([]clipboard.Format, error) { return formats, nil }
	clipboardRead = read
	t.Cleanup(func() {
		clipboardInit = origInit
		clipboardFormats = origFormats
		clipboardRead = origRead
	})
}

func TestReadFallsBackToBMP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(1, 1, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var encoded bytes.Buffer
	if err := bmp.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}

	bmpFormat := clipboard.Register("image/bmp")
	stubClipboard(t, []clipboard.Format{bmpFormat}, func(_ context.Context, format clipboard.Format) ([]byte, error) {
		if format == bmpFormat {
			return encoded.Bytes(), nil
		}
		return nil, clipboard.ErrNoData
	})

	data, mimeType, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mimeType != "image/png" && mimeType != "image/jpeg" {
		t.Fatalf("mime type = %q, want normalized PNG/JPEG", mimeType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("normalized clipboard BMP is not decodable: %v", err)
	}
}

func TestReadPrefersPNGOverOtherImageMIMEs(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, nil); err != nil {
		t.Fatal(err)
	}

	pngFormat := clipboard.Register("image/png")
	jpegFormat := clipboard.Register("image/jpeg")
	stubClipboard(t, []clipboard.Format{jpegFormat, pngFormat}, func(_ context.Context, format clipboard.Format) ([]byte, error) {
		switch format {
		case pngFormat:
			return pngBuf.Bytes(), nil
		case jpegFormat:
			return jpegBuf.Bytes(), nil
		default:
			return nil, clipboard.ErrNoData
		}
	})

	data, mimeType, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(data) == 0 || (mimeType != "image/png" && mimeType != "image/jpeg") {
		t.Fatalf("read clipboard PNG = %d bytes, %q", len(data), mimeType)
	}
}

func TestReadFallsBackWhenFmtImageIsInvalid(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, nil); err != nil {
		t.Fatal(err)
	}

	jpegFormat := clipboard.Register("image/jpeg")
	stubClipboard(t, []clipboard.Format{clipboard.FmtImage, jpegFormat}, func(_ context.Context, format clipboard.Format) ([]byte, error) {
		if format == clipboard.FmtImage {
			return []byte("invalid PNG"), nil
		}
		if format == jpegFormat {
			return jpegBuf.Bytes(), nil
		}
		return nil, clipboard.ErrNoData
	})

	data, mimeType, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mimeType != "image/jpeg" || !bytes.Equal(data, jpegBuf.Bytes()) {
		t.Fatalf("clipboard fallback = %d bytes, %q; want original JPEG", len(data), mimeType)
	}
}

func TestReadReportsUnreachableClipboard(t *testing.T) {
	pngFormat := clipboard.Register("image/png")
	stubClipboard(t, []clipboard.Format{pngFormat}, func(context.Context, clipboard.Format) ([]byte, error) {
		return nil, clipboard.ErrUnavailable
	})

	// A clipboard that advertises a format but cannot be read is not an empty
	// clipboard; reporting it as one sends the user looking for the wrong thing.
	if _, _, err := Read(); !errors.Is(err, clipboard.ErrUnavailable) {
		t.Fatalf("Read() error = %v, want %v", err, clipboard.ErrUnavailable)
	}
}

func TestReadTreatsMissingDataAsEmpty(t *testing.T) {
	pngFormat := clipboard.Register("image/png")
	stubClipboard(t, []clipboard.Format{pngFormat}, func(context.Context, clipboard.Format) ([]byte, error) {
		return nil, clipboard.ErrNoData
	})

	if _, _, err := Read(); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("Read() error = %v, want %v", err, ErrNoAttachment)
	}
}
