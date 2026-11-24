package llm

import (
	"context"
	"time"
)

type responseTimingKey struct{}
type discardedFirstOutputKey struct{}

type responseTiming struct {
	startedAt time.Time
	tracker   *visibleStreamTracker
	modelRef  string
	report    func(string, int64)
}

func responseTimingFromContext(ctx context.Context) *responseTiming {
	timing, _ := ctx.Value(responseTimingKey{}).(*responseTiming)
	return timing
}

func firstOutputLatencyNs(start, first time.Time) int64 {
	if !start.IsZero() && !first.IsZero() && first.After(start) {
		return first.Sub(start).Nanoseconds()
	}
	return 0
}

func (t *responseTiming) takeFirstOutput() time.Time {
	first := t.tracker.firstContentAt
	t.tracker.firstContentAt = time.Time{}
	return first
}

func (t *responseTiming) discard() {
	if ns := firstOutputLatencyNs(t.startedAt, t.takeFirstOutput()); ns > 0 && t.report != nil {
		t.report(t.modelRef, ns)
	}
}

// Settle the failed request before starting the next one's clock. WebSocket
// submission notifications run after connection setup, too late to start it.
func restartResponseTiming(ctx context.Context) {
	if timing := responseTimingFromContext(ctx); timing != nil {
		timing.discard()
		timing.startedAt = time.Now()
	}
}
