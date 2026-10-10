package imageutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDecodeAdmissionCancellation(t *testing.T) {
	for range maxConcurrentDecodes {
		release, err := AcquireDecodeSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	ctx, cancel := context.WithCancel(t.Context())
	waiting := make(chan error, 1)
	go func() {
		release, err := AcquireDecodeSlot(ctx)
		if release != nil {
			release()
		}
		waiting <- err
	}()
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatal("blocked decode did not cancel", err)
	}
	if len(decodeSlots) != maxConcurrentDecodes {
		t.Fatal("cancelled waiter consumed a slot")
	}
}

func TestNormalizeImageCancelsWhileAdmissionIsFull(t *testing.T) {
	for range maxConcurrentDecodes {
		release, err := AcquireDecodeSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	data := encodeTestPNG(t, gradientImage(13, 7))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := NormalizeImage(ctx, data, "image/png"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("normalization did not cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("normalization ignored cancellation while waiting")
	}
	if len(decodeSlots) != maxConcurrentDecodes {
		t.Fatal("cancelled normalization consumed a slot")
	}
}
