package agent

import (
	"testing"
)

func TestCompactionStatusStartedCarriesPlanID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.startCompactionState(9, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeIdle})
	defer a.resetCompactionState()

	event := a.compactionStatusEvent(CompactionStatusStarted, "")
	if event.Status != CompactionStatusStarted {
		t.Fatalf("status = %q, want %q", event.Status, CompactionStatusStarted)
	}
	if event.Trigger != compactionTriggerUsageDriven.analyticsName() {
		t.Fatalf("trigger = %q, want %q", event.Trigger, compactionTriggerUsageDriven.analyticsName())
	}
	if event.PlanID != "9" {
		t.Fatalf("plan id = %q, want %q", event.PlanID, "9")
	}
}
