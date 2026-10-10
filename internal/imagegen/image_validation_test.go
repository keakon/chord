package imagegen

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/keakon/chord/internal/imageutil"
)

func TestImageValidationCachePreservesChecks(t *testing.T) {
	data := samplePNG(t)
	if _, err := ValidateImage(t.Context(), data, "image/png"); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateImage(t.Context(), data, "image/jpeg"); err == nil {
		t.Fatal("cache bypassed MIME validation")
	}
	broken := slices.Clone(data[:len(data)-15])
	for range 2 {
		if _, err := ValidateImage(t.Context(), broken, ""); err == nil {
			t.Fatal("truncated image accepted")
		}
		if imageutil.HasVerifiedImage(broken) {
			t.Fatal("failed decode entered cache")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ValidateImage(ctx, data, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("cache bypassed cancellation", err)
	}
}
