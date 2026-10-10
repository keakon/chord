package imageutil

import (
	"context"
	"errors"
	"testing"
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
