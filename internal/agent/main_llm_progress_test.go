package agent

import (
	"testing"
	"time"
)

func TestShouldEmitRequestProgress(t *testing.T) {
	now := time.Unix(100, 0)
	if !shouldEmitRequestProgress(now, time.Time{}, 10, 1, 0, 0) {
		t.Fatal("first progress update should emit")
	}
	if shouldEmitRequestProgress(now.Add(50*time.Millisecond), now, 10, 1, 10, 1) {
		t.Fatal("unchanged progress should not emit")
	}
	if shouldEmitRequestProgress(now.Add(50*time.Millisecond), now, 20, 2, 10, 1) {
		t.Fatal("changed progress before min interval should not emit")
	}
	if !shouldEmitRequestProgress(now.Add(requestProgressEmitMinInterval), now, 20, 2, 10, 1) {
		t.Fatal("changed progress at min interval should emit")
	}
}
