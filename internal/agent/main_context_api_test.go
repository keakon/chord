package agent

import (
	"strings"
	"testing"
)

func TestGetResidentMemoryBreakdownIgnoresParkedTaskHistory(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-resident")
	sub.appendPendingUserMessage(pendingUserMessage{Content: strings.Repeat("resident-history-", 256)})
	a.flushPersist()
	sub.setState(SubAgentStateCancelled, "done")
	a.syncTaskRecordFromSub(sub, "done")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}
	a.SwitchFocus(sub.instanceID)
	if target := a.focusedAgentSnapshot(); !target.parked {
		t.Fatalf("focused target = %#v, want parked", target)
	}
	if got := a.GetResidentMemoryBreakdown(); got.Total() != 0 {
		t.Fatalf("parked resident breakdown = %#v, want zero", got)
	}
	if got := a.GetContextBytes(); got == 0 {
		t.Fatal("parked context bytes = 0, want the transcript readable from disk")
	}
}
