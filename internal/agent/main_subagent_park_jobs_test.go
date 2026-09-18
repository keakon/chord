package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// Parking releases a worker's hot runtime but must leave its detached jobs
// running: the backgroundJobHandle promise ("you will be notified when it
// finishes") is what let the worker end its turn, and the completion path
// attributes a finished job whose owner has no live runtime to the main
// transcript, so the orchestrator still sees the result. Only session
// switches, shutdown, and terminal settles tear jobs down.
func TestParkSubAgentLeavesDetachedJobsRunning(t *testing.T) {
	restore := tools.ResetJobRegistryForTest()
	defer restore()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-jobs")
	sub.setState(SubAgentStateIdle, "idle after starting a background job")

	ctx := tools.WithAgentID(context.Background(), sub.instanceID)
	jobID, err := tools.ExecuteJobForTest(ctx, "sleep 60", "long build", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want the idle worker parked")
	}
	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("parked worker still has a live runtime")
	}
	peek, ok := tools.PeekJobForDisplay(jobID, 16)
	if !ok || (peek.Status != "running" && peek.Status != "stopping") {
		t.Fatalf("parked worker's detached job = (%#v, %v), want it still running", peek, ok)
	}

	// The main agent retains full access to what a parked worker started.
	if !tools.StopJobByUser(jobID, "test cleanup") {
		t.Fatal("StopJobByUser = false, want the main agent able to stop the orphaned job")
	}
}
