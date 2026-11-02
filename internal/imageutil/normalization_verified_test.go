package imageutil

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestNormalizeVerifiedImageDoesNotWaitForDecodeSlot(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			data := encodeTestPNG(t, gradientImage(19, 11))
			if format == "jpeg" {
				data = encodeTestJPEG(t, gradientImage(19, 11))
			}
			first, err := NormalizeImage(t.Context(), data, "image/"+format)
			if err != nil {
				t.Fatal(err)
			}
			for range maxConcurrentDecodes {
				release, err := AcquireDecodeSlot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			got, err := NormalizeImage(ctx, bytes.Clone(data), "image/other")
			if err != nil {
				t.Fatalf("verified pass-through waited for a decode slot: %v", err)
			}
			if !bytes.Equal(got.Data, data) || got.MimeType != first.MimeType || got.Width != 19 || got.Height != 11 {
				t.Fatalf("verified normalization changed output: %+v", got)
			}
			cancel()
			if _, err := NormalizeImage(ctx, data, first.MimeType); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled verified normalization error = %v", err)
			}
		})
	}
}

func TestNormalizeVerifiedImageStillTransforms(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		w, h int
	}{
		{"scale", encodeTestPNG(t, gradientImage(4000, 10)), 2000, 5},
		{"orientation", insertExifOrientation(encodeTestJPEG(t, gradientImage(6, 3)), binary.LittleEndian, 6), 3, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release, err := AcquireDecodeSlot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = DecodeImage(tc.data)
			release()
			if err != nil || !HasVerifiedImage(tc.data) {
				t.Fatalf("validate source: %v", err)
			}
			out, err := NormalizeImage(t.Context(), tc.data, "")
			if err != nil {
				t.Fatal(err)
			}
			if out.Width != tc.w || out.Height != tc.h || bytes.Equal(out.Data, tc.data) {
				t.Fatalf("verified input bypassed required transform: %dx%d", out.Width, out.Height)
			}
		})
	}
}

func TestNormalizeVerifiedImageRejectsChangedPixelStream(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(23, 13))
	if _, err := NormalizeImage(t.Context(), data, "image/png"); err != nil {
		t.Fatal(err)
	}
	truncated := bytes.Clone(data[:len(data)-8])
	if HasVerifiedImage(truncated) {
		t.Fatal("different bytes matched the validation cache")
	}
	if _, err := NormalizeImage(t.Context(), truncated, "image/png"); err == nil {
		t.Fatal("truncated pixel stream was accepted after validating the original")
	}
}
