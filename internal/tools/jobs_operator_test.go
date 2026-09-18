package tools

import (
	"strings"
	"testing"
	"time"
)

func TestTailWriterLastOutputTimeTracksWrites(t *testing.T) {
	w := newTailWriter(1 << 16)
	if got := w.lastOutputTime(); !got.IsZero() {
		t.Fatalf("lastOutputTime before any write = %v, want the zero value", got)
	}
	if _, err := w.Write(nil); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if got := w.lastOutputTime(); !got.IsZero() {
		t.Fatalf("an empty write advanced lastOutputTime to %v, want the zero value", got)
	}

	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	first := w.lastOutputTime()
	if first.IsZero() {
		t.Fatal("a non-empty write must record lastOutputTime")
	}

	time.Sleep(time.Millisecond)
	if _, err := w.Write([]byte("second")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	second := w.lastOutputTime()
	if !second.After(first) {
		t.Fatalf("lastOutputTime = %v, want it to advance past the previous %v", second, first)
	}
}

func TestStopJobByUserUnknownAndFinishedReturnFalse(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	if StopJobByUser("job-unknown", "stopped by user") {
		t.Fatal("stopping an unknown job id must report false")
	}

	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(jobTestCtx(), sender)
	id, err := ExecuteJobForTest(ctx, "printf done", "quick job", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	select {
	case <-sender.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the quick job to finish")
	}

	if StopJobByUser(id, "stopped by user") {
		t.Fatal("stopping an already-finished job must report false")
	}
}

func TestStopJobByUserNotifiesDetachedJob(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(jobTestCtx(), sender)
	id, err := ExecuteJobForTest(ctx, "sleep 5", "stop me", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	if !StopJobByUser(id, "stopped by user") {
		t.Fatal("stopping a running detached job must report true")
	}

	select {
	case raw := <-sender.ch:
		payload, ok := raw.(*JobFinishedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *JobFinishedPayload", raw)
		}
		if payload.BackgroundID != id {
			t.Fatalf("payload background id = %q, want %q", payload.BackgroundID, id)
		}
		if !payload.UserStopped {
			t.Fatal("a user-stopped detached job must mark the completion payload")
		}
		if payload.Status != "killed (stopped by user)" {
			t.Fatalf("payload status = %q, want %q", payload.Status, "killed (stopped by user)")
		}
		if !strings.Contains(payload.Message, "Status: killed (stopped by user)") {
			t.Fatalf("payload message = %q, want it to carry the user-stop status", payload.Message)
		}
		if sender.eventType != EventBackgroundObjectFinished {
			t.Fatalf("event type = %q, want %q", sender.eventType, EventBackgroundObjectFinished)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the stopped job's completion event")
	}
}

func TestStopJobByUserForegroundJobStaysSilent(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(jobTestCtx(), sender)
	j, err := globalJobRegistry.start(ctx, jobStartRequest{Command: "sleep 5", Description: "foreground stop"})
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	if !StopJobByUser(j.ID, "stopped by user") {
		t.Fatal("stopping a running foreground job must report true")
	}
	select {
	case <-j.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the stopped foreground job to finish")
	}

	// A foreground job is reported through its waiting shell result, so the
	// stop must not also deliver an asynchronous completion notification.
	select {
	case raw := <-sender.ch:
		t.Fatalf("a foreground stop emitted a completion event %T", raw)
	case <-time.After(200 * time.Millisecond):
	}

	j.mu.Lock()
	detached := j.detached
	origin := j.stopOrigin
	j.mu.Unlock()
	if detached {
		t.Fatal("a user stop must not promote a foreground job to the background")
	}
	if origin != jobStopOriginUser {
		t.Fatalf("stop origin = %q, want %q", origin, jobStopOriginUser)
	}
}

// detachedJobForFinishTest builds a detached job whose only purpose is to
// drive finish() directly, isolating the payload branch from process teardown.
func detachedJobForFinishTest(sender EventSender, origin jobStopOrigin) *job {
	return &job{
		ID:          "job-1",
		AgentID:     "owner-1",
		Command:     "printf sample",
		detached:    true,
		eventSender: sender,
		status:      jobStatusStopping,
		stopOrigin:  origin,
		done:        make(chan struct{}),
		cancelCh:    make(chan string, 1),
	}
}

// A stop request that raced a self-exit must not be reported as a user stop:
// the process completed on its own, so the terminal state is a normal
// completion and its toast must survive.
func TestFinishDoesNotMarkUserStoppedWhenProcessCompleted(t *testing.T) {
	sender := &recordingEventSender{ch: make(chan any, 1)}
	j := detachedJobForFinishTest(sender, jobStopOriginUser)
	globalJobRegistry.finish(j, jobStatusCompleted, "exit code 0", nil)

	select {
	case raw := <-sender.ch:
		payload, ok := raw.(*JobFinishedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *JobFinishedPayload", raw)
		}
		if payload.UserStopped {
			t.Fatalf("payload marked a user stop for a job that completed on its own: %+v", payload)
		}
		if payload.Status != "completed (exit code 0)" {
			t.Fatalf("payload status = %q, want the natural completion status", payload.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("a detached job finishing on its own must still send its completion event")
	}
}

func TestFinishMarksUserStoppedWhenTheKillTookEffect(t *testing.T) {
	sender := &recordingEventSender{ch: make(chan any, 1)}
	j := detachedJobForFinishTest(sender, jobStopOriginUser)
	globalJobRegistry.finish(j, jobStatusKilled, "stopped by user", nil)

	select {
	case raw := <-sender.ch:
		payload, ok := raw.(*JobFinishedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *JobFinishedPayload", raw)
		}
		if !payload.UserStopped {
			t.Fatalf("payload = %+v, want a user-stop marker once the kill took effect", payload)
		}
		if payload.Status != "killed (stopped by user)" {
			t.Fatalf("payload status = %q, want %q", payload.Status, "killed (stopped by user)")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the stopped job's completion event")
	}
}

func TestPeekJobForDisplayReturnsFieldsAndCleanedTailWithoutConsuming(t *testing.T) {
	resetJobRegistryOnlyForTest(t)

	w := newTailWriter(1 << 16)
	rawOutput := "line one\x1b[31mred\x1b[0m\n"
	if _, err := w.Write([]byte(rawOutput)); err != nil {
		t.Fatalf("write output: %v", err)
	}
	lastOutputAt := w.lastOutputTime()
	j := &job{
		ID:          "job-1",
		AgentID:     "owner-1",
		Command:     "printf sample",
		Description: "sample description",
		StartedAt:   time.Unix(100, 0),
		LogFile:     "/tmp/job-1.log",
		status:      jobStatusRunning,
		output:      w,
		cancelCh:    make(chan string, 1),
		done:        make(chan struct{}),
	}
	globalJobRegistry.mu.Lock()
	globalJobRegistry.jobs[j.ID] = j
	globalJobRegistry.mu.Unlock()

	peek, ok := PeekJobForDisplay("job-1", 1024)
	if !ok {
		t.Fatal("PeekJobForDisplay(job-1) = false, want true")
	}
	if peek.ID != "job-1" {
		t.Errorf("id = %q, want job-1", peek.ID)
	}
	if peek.Label != "sample description" {
		t.Errorf("label = %q, want the description", peek.Label)
	}
	if peek.Command != "printf sample" {
		t.Errorf("command = %q, want the command", peek.Command)
	}
	if peek.Owner != "owner-1" {
		t.Errorf("owner = %q, want the agent id", peek.Owner)
	}
	if peek.Status != string(jobStatusRunning) {
		t.Errorf("status = %q, want %q", peek.Status, jobStatusRunning)
	}
	if !peek.StartedAt.Equal(time.Unix(100, 0)) {
		t.Errorf("started at = %v, want the recorded start time", peek.StartedAt)
	}
	if !peek.LastOutputAt.Equal(lastOutputAt) {
		t.Errorf("last output at = %v, want %v", peek.LastOutputAt, lastOutputAt)
	}
	if peek.LogFile != "/tmp/job-1.log" {
		t.Errorf("log file = %q, want the recorded path", peek.LogFile)
	}
	if peek.Tail != "line onered\n" {
		t.Errorf("tail = %q, want the escape-stripped output", peek.Tail)
	}
	if peek.DroppedBytes != 0 || peek.Truncated {
		t.Errorf("dropped=%d truncated=%v, want 0/false", peek.DroppedBytes, peek.Truncated)
	}

	// Peeking is non-consuming: the raw window is still available to the
	// incremental reader from its original cursor.
	chunk, dropped := j.readIncremental("main")
	if chunk != rawOutput {
		t.Errorf("readIncremental = %q, want the full raw output %q", chunk, rawOutput)
	}
	if dropped != 0 {
		t.Errorf("readIncremental dropped = %d, want 0", dropped)
	}
	// And it does not claim the completion notification.
	if !ClaimJobReported("job-1") {
		t.Error("ClaimJobReported after a peek = false, want the notification to stay deliverable")
	}

	if _, ok := PeekJobForDisplay("job-unknown", 1024); ok {
		t.Error("PeekJobForDisplay(unknown) = true, want false")
	}
}

func TestPeekJobForDisplayReportsDroppedAndTruncated(t *testing.T) {
	resetJobRegistryOnlyForTest(t)

	// A 4-byte window retains only the last 4 bytes, so the dropped counter is
	// non-zero while the excerpt itself is not truncated.
	w := newTailWriter(4)
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatalf("write output: %v", err)
	}
	j := &job{
		ID:       "job-2",
		Command:  "printf sample",
		status:   jobStatusRunning,
		output:   w,
		cancelCh: make(chan string, 1),
		done:     make(chan struct{}),
	}
	globalJobRegistry.mu.Lock()
	globalJobRegistry.jobs[j.ID] = j
	globalJobRegistry.mu.Unlock()

	peek, ok := PeekJobForDisplay("job-2", 4)
	if !ok {
		t.Fatal("PeekJobForDisplay(job-2) = false, want true")
	}
	if peek.Label != "printf sample" {
		t.Errorf("label = %q, want the command when the description is empty", peek.Label)
	}
	if peek.Tail != "6789" {
		t.Errorf("tail = %q, want the last retained bytes", peek.Tail)
	}
	if peek.DroppedBytes != 6 {
		t.Errorf("dropped = %d, want 6 earlier bytes dropped from the window", peek.DroppedBytes)
	}
	if peek.Truncated {
		t.Errorf("truncated = true, want false when the whole window fits")
	}

	peek, ok = PeekJobForDisplay("job-2", 2)
	if !ok {
		t.Fatal("PeekJobForDisplay(job-2, 2) = false, want true")
	}
	if !peek.Truncated {
		t.Error("truncated = false, want true when the excerpt is cut")
	}
	if peek.Tail != "89" {
		t.Errorf("tail = %q, want the requested tail excerpt", peek.Tail)
	}
	if peek.DroppedBytes != 6 {
		t.Errorf("dropped = %d, want 6", peek.DroppedBytes)
	}
}
