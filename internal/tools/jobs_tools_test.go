package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

const jobTestOwner = "job-test-owner"

func jobTestCtx() context.Context { return WithAgentID(context.Background(), jobTestOwner) }

func runJobOutput(t *testing.T, args map[string]any) string {
	t.Helper()
	out, err := (JobOutputTool{}).Execute(jobTestCtx(), mustMarshal(t, args))
	if err != nil {
		t.Fatalf("JobOutputTool.Execute(%v): %v", args, err)
	}
	return out
}

func TestJobOutputReadsIncrementallyAndWaitTimeoutKeepsJobRunning(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	id, err := ExecuteJobForTest(jobTestCtx(), "printf 'first'; sleep 1; printf 'second'", "incremental output", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	restoreWait := jobOutputWaitMs
	t.Cleanup(func() { jobOutputWaitMs = restoreWait })

	// wait:output blocks for the first chunk instead of busy-polling, and the
	// runtime owns the budget, so the test shrinks it.
	jobOutputWaitMs = 5000
	out := runJobOutput(t, map[string]any{"job_id": id, "wait": "output"})
	if !strings.Contains(out, "first") {
		t.Fatalf("wait:output read = %q, want the early output", out)
	}

	// A wait shorter than the remaining work returns running instead of an
	// error, and the job keeps its process.
	jobOutputWaitMs = 50
	out = runJobOutput(t, map[string]any{"job_id": id, "wait": "exit"})
	if !strings.Contains(out, "[status: running]") {
		t.Fatalf("wait that expired = %q, want running status", out)
	}
	if j, ok := globalJobRegistry.get(id); !ok || j.isFinished() {
		t.Fatalf("job %s must survive a wait timeout", id)
	}

	// The final wait collects the rest and reports the terminal status.
	jobOutputWaitMs = 5000
	out = runJobOutput(t, map[string]any{"job_id": id, "wait": "exit"})
	if !strings.Contains(out, "second") {
		t.Fatalf("final read = %q, want remaining output", out)
	}
	if !strings.Contains(out, "[status: completed (exit code 0)]") {
		t.Fatalf("final read = %q, want completed status", out)
	}
}

func TestJobKillSuppressesCompletionNotification(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(jobTestCtx(), sender)
	id, err := ExecuteJobForTest(ctx, "sleep 5", "kill me", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	out, err := (JobKillTool{}).Execute(jobTestCtx(), mustMarshal(t, map[string]any{"job_id": id, "reason": "no longer needed"}))
	if err != nil {
		t.Fatalf("JobKillTool.Execute: %v", err)
	}
	if !strings.Contains(out, "stopping") {
		t.Fatalf("kill output = %q, want stopping status", out)
	}
	select {
	case payload := <-sender.ch:
		t.Fatalf("job_kill must not emit a completion notification, got %T", payload)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestClaimJobReportedDedupsButNeverDropsUnknown(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	if !ClaimJobReported("job-does-not-exist") {
		t.Fatal("an unknown job id must still be delivered")
	}
	id, err := ExecuteJobForTest(jobTestCtx(), "printf done", "report once", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	if !ClaimJobReported(id) {
		t.Fatal("the first claim of a completion must deliver")
	}
	if ClaimJobReported(id) {
		t.Fatal("an already-reported completion must not deliver twice")
	}
}

func TestJobOutputTerminalReadMarksReported(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	id, err := ExecuteJobForTest(jobTestCtx(), "printf done", "terminal read", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	out := runJobOutput(t, map[string]any{"job_id": id, "wait": "exit"})
	if !strings.Contains(out, "[status: completed (exit code 0)]") {
		t.Fatalf("terminal read = %q, want completed status", out)
	}
	if ClaimJobReported(id) {
		t.Fatal("a terminal job_output read must suppress the duplicate completion notification")
	}
}

func TestJobOutputAndKillEnforceOwnership(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	id, err := ExecuteJobForTest(jobTestCtx(), "sleep 5", "owned job", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	// A caller that presents no agent identity cannot reach any job.
	if _, err := (JobOutputTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"job_id": id, "wait": "none"})); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("anonymous job_output err = %v, want ownership rejection", err)
	}

	// A sibling agent that knows the id cannot read or stop another agent's job.
	sibling := WithAgentID(context.Background(), "sibling-agent")
	if _, err := (JobOutputTool{}).Execute(sibling, mustMarshal(t, map[string]any{"job_id": id, "wait": "none"})); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("sibling job_output err = %v, want ownership rejection", err)
	}
	if _, err := (JobKillTool{}).Execute(sibling, mustMarshal(t, map[string]any{"job_id": id})); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("sibling job_kill err = %v, want ownership rejection", err)
	}
	if j, ok := globalJobRegistry.get(id); !ok || j.isFinished() {
		t.Fatal("a rejected job_kill must not stop the job")
	}

	// The main agent may operate on any job.
	mainCtx := WithJobAccess(WithAgentID(context.Background(), "main-9"), JobAccess{MainAgentID: "main-9"})
	if _, err := (JobOutputTool{}).Execute(mainCtx, mustMarshal(t, map[string]any{"job_id": id, "wait": "none"})); err != nil {
		t.Fatalf("main job_output err = %v, want access", err)
	}

	// A worker may read a job its direct owner started, the documented
	// worker-reads-owner's-job case.
	workerCtx := WithJobAccess(WithAgentID(context.Background(), "worker-1"), JobAccess{OwnerAgentID: jobTestOwner})
	if _, err := (JobOutputTool{}).Execute(workerCtx, mustMarshal(t, map[string]any{"job_id": id, "wait": "none"})); err != nil {
		t.Fatalf("owner's worker job_output err = %v, want access", err)
	}
}

func TestJobWithNoRecordedOwnerFailsClosed(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	id, err := ExecuteJobForTest(context.Background(), "sleep 5", "unowned job", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	if _, err := (JobOutputTool{}).Execute(jobTestCtx(), mustMarshal(t, map[string]any{"job_id": id, "wait": "none"})); err == nil {
		t.Fatal("a job with no recorded owner must not be readable")
	}
}

func TestJobListFiltersByOwner(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	ownerOne := WithAgentID(WithEventSender(context.Background(), &recordingEventSender{ch: make(chan any, 1)}), "owner-1")
	id1, err := ExecuteJobForTest(ownerOne, "sleep 5", "owner one job", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest(owner-1): %v", err)
	}
	if _, err := ExecuteJobForTest(WithAgentID(context.Background(), "owner-2"), "sleep 5", "owner two job", nil); err != nil {
		t.Fatalf("ExecuteJobForTest(owner-2): %v", err)
	}
	out, err := (JobListTool{}).Execute(WithAgentID(context.Background(), "owner-1"), nil)
	if err != nil {
		t.Fatalf("JobListTool.Execute: %v", err)
	}
	if !strings.Contains(out, id1) || !strings.Contains(out, "owner one job") {
		t.Fatalf("job_list = %q, want owner-1's job", out)
	}
	if strings.Contains(out, "owner two job") {
		t.Fatalf("job_list leaked another owner's job: %q", out)
	}
}

func TestJobOutputWaitOutputReturnsOnNextWrite(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	id, err := ExecuteJobForTest(jobTestCtx(), "sleep 0.2; printf 'later'", "wait for output", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	out := runJobOutput(t, map[string]any{"job_id": id, "wait": "output"})
	if !strings.Contains(out, "later") {
		t.Fatalf("wait:output = %q, want the output that arrived during the wait", out)
	}
}

func TestJobOutputWaitOutputIsBoundedOnAQuietJob(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	restoreWait := jobOutputWaitMs
	t.Cleanup(func() { jobOutputWaitMs = restoreWait })
	jobOutputWaitMs = 50
	id, err := ExecuteJobForTest(jobTestCtx(), "sleep 5", "quiet job", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	start := time.Now()
	out := runJobOutput(t, map[string]any{"job_id": id, "wait": "output"})
	if !strings.Contains(out, "[status: running]") {
		t.Fatalf("quiet wait:output = %q, want the job still running", out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("quiet wait:output blocked for %s, want it bounded by the runtime budget", elapsed)
	}
}

func TestJobOutputRejectsUnknownWait(t *testing.T) {
	if _, err := (JobOutputTool{}).Execute(context.Background(), mustMarshal(t, map[string]any{"job_id": "job-1", "wait": "forever"})); err == nil {
		t.Fatal("an unknown wait value must be rejected")
	}
}

func TestJobOutputEscalatesRepeatedNonBlockingEmptyReads(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	restoreWait := jobOutputWaitMs
	t.Cleanup(func() { jobOutputWaitMs = restoreWait })
	jobOutputWaitMs = 50

	id, err := ExecuteJobForTest(jobTestCtx(), "sleep 5", "quiet job", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	first := runJobOutput(t, map[string]any{"job_id": id, "wait": "none"})
	if strings.Contains(first, "[notice]") || strings.Contains(first, "rejected automatically") {
		t.Fatalf("first empty read = %q, want a plain status", first)
	}
	second := runJobOutput(t, map[string]any{"job_id": id, "wait": "none"})
	if !strings.Contains(second, "[notice]") {
		t.Fatalf("second empty read = %q, want the polling notice", second)
	}
	third, err := (JobOutputTool{}).Execute(jobTestCtx(), mustMarshal(t, map[string]any{"job_id": id, "wait": "none"}))
	if err == nil {
		t.Fatalf("third empty read = %q, want a rejection error", third)
	}
	if !strings.Contains(err.Error(), "rejected automatically") {
		t.Fatalf("third empty read error = %q, want a refusal", err)
	}

	// A blocking wait is a renewal, not polling: it clears the streak.
	after := runJobOutput(t, map[string]any{"job_id": id, "wait": "exit"})
	if strings.Contains(after, "rejected automatically") {
		t.Fatalf("wait:exit after refusals = %q, want a normal read", after)
	}
}

func TestJobOutputPollStreakResetsAfterNewOutput(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	id, err := ExecuteJobForTest(jobTestCtx(), "sleep 0.3; printf 'late'; sleep 5", "late output", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	_ = runJobOutput(t, map[string]any{"job_id": id, "wait": "none"})
	_ = runJobOutput(t, map[string]any{"job_id": id, "wait": "none"})

	out := runJobOutput(t, map[string]any{"job_id": id, "wait": "output"})
	if !strings.Contains(out, "late") {
		t.Fatalf("wait:output = %q, want the late output", out)
	}
	after := runJobOutput(t, map[string]any{"job_id": id, "wait": "none"})
	if strings.Contains(after, "[notice]") || strings.Contains(after, "rejected automatically") {
		t.Fatalf("read after fresh output = %q, want the streak reset", after)
	}
}
