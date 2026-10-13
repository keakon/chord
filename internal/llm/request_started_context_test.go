package llm

import "testing"

func TestNestedRequestStartedObserversBothReceiveTransportEvidence(t *testing.T) {
	parent, child := 0, 0
	ctx := WithRequestStarted(t.Context(), func() { parent++ })
	ctx = WithRequestStarted(ctx, func() { child++ })
	markRequestStarted(ctx)
	markRequestStarted(ctx)
	if parent != 2 || child != 2 {
		t.Fatalf("parent=%d child=%d", parent, child)
	}
}
