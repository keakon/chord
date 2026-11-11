package llm

import "context"

type responseTimingResetKey struct{}

// restartResponseTiming excludes a failed physical request before a provider
// retries internally. Transport submission notifications run after WebSocket
// setup, so they cannot reset this clock without losing connection time.
func restartResponseTiming(ctx context.Context) {
	if reset, ok := ctx.Value(responseTimingResetKey{}).(func()); ok {
		reset()
	}
}
