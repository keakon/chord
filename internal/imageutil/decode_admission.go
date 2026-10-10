package imageutil

import "context"

// AcquireDecodeSlot bounds pixel-heavy work across all image entry points.
// Waiting callers can cancel without consuming a slot.
func AcquireDecodeSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case decodeSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-decodeSlots
			return nil, err
		}
		return func() { <-decodeSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
