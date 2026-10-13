package executiongate

import (
	"context"
	"testing"
)

func TestLeaseContextOnlyProvesExistingExecution(t *testing.T) {
	var gate Gate
	lease, err := gate.TryBegin(Tool, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithLease(context.Background(), lease)
	gate.Freeze()
	if !HasActiveLease(ctx, &gate, Tool) {
		t.Fatal("freeze invalidated already executing tool")
	}
	if HasActiveLease(ctx, &Gate{}, Tool) || HasActiveLease(ctx, &gate, LLM) {
		t.Fatal("foreign gate or wrong kind accepted")
	}
	if _, err := Begin(WithContext(ctx, &gate, "task-1"), LLM); !IsPaused(err) {
		t.Fatal("existing tool lease authorized a new model request")
	}
	lease.Finish()
	if HasActiveLease(ctx, &gate, Tool) {
		t.Fatal("finished tool lease still accepted")
	}
}
