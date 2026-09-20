//go:build unix

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The job owns the command's output pipe, so a descendant that inherits the
// command's process group keeps feeding the job after the direct command is
// reaped. Before the job owned the pipe, cmd.WaitDelay cut the copy off three
// seconds after the direct process exited and the late output was lost.
func TestJobKeepsDescendantOutputPastTheDirectCommandExit(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	restoreWait := jobOutputWaitMs
	jobOutputWaitMs = 30_000
	t.Cleanup(func() { jobOutputWaitMs = restoreWait })

	id, err := ExecuteJobForTest(jobTestCtx(), "(sleep 4; echo DESCENDANT_LATE) & exit 0", "descendant output", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}

	start := time.Now()
	out := runJobOutput(t, map[string]any{"job_id": id, "wait": "exit"})
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Fatalf("wait:exit returned after %s, want it to wait for the descendant past the old 3s cut", elapsed)
	}
	for _, want := range []string{"DESCENDANT_LATE", "completed (exit code 0; the process group drained)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read = %q, want %q", out, want)
		}
	}
}

// A descendant that ignores SIGTERM still has to die: the stop escalates to
// SIGKILL once the grace period passes without the group draining.
func TestStopEscalatesToSIGKILLAfterTheDirectCommandExited(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	shortenJobGroupTimings(t)

	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(jobTestCtx(), sender)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	// exec keeps the ignored disposition across the image change, so the only
	// process left in the group really does ignore SIGTERM.
	command := "(trap '' TERM; exec sleep 60) & echo $! > " + strconv.Quote(pidFile) + "; exit 0"
	id, err := ExecuteJobForTest(ctx, command, "stubborn descendant", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	descendantPID := readJobPidFile(t, pidFile)
	j := waitForJobGroupPending(t, id)

	if !StopJobByUser(id, "stopped by user") {
		t.Fatal("stopping a job whose group is still pending must report true")
	}
	payload := waitForJobFinishedPayload(t, sender)
	if !payload.UserStopped {
		t.Fatalf("payload = %+v, want the user-stop marker", payload)
	}
	if !strings.HasPrefix(payload.Status, "killed (stopped by user") {
		t.Fatalf("status = %q, want the user-stop kill", payload.Status)
	}
	if !strings.Contains(payload.Message, "the command had already exited with exit code 0") {
		t.Fatalf("message = %q, want the already-exited fact for a stop that only had descendants", payload.Message)
	}
	waitForPidToDisappear(t, descendantPID, 5*time.Second)

	// The group is drained, so the job is not left reporting a live group.
	if alive, known := processGroupAlive(j.commandProcessGroupID()); alive || !known {
		t.Fatalf("processGroupAlive after the stop = (%v, %v), want a definitively empty group", alive, known)
	}
}

// The same escalation applies while the direct command still waits on the
// descendant: the SIGTERM reaps the leader, the ignored descendant survives it.
func TestStopEscalatesToSIGKILLWhileTheDirectCommandStillWaits(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	shortenJobGroupTimings(t)

	sender := &recordingEventSender{ch: make(chan any, 1)}
	ctx := WithEventSender(jobTestCtx(), sender)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	command := "(trap '' TERM; exec sleep 60) & echo $! > " + strconv.Quote(pidFile) + "; wait"
	id, err := ExecuteJobForTest(ctx, command, "stubborn descendant", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	descendantPID := readJobPidFile(t, pidFile)

	if !StopJobByUser(id, "stopped by user") {
		t.Fatal("stopping a running job must report true")
	}
	payload := waitForJobFinishedPayload(t, sender)
	if !strings.HasPrefix(payload.Status, "killed (stopped by user") {
		t.Fatalf("status = %q, want the user-stop kill", payload.Status)
	}
	// The leader was still running when the stop landed, so the terminal state
	// must not claim it had exited on its own.
	if strings.Contains(payload.Message, "the command had already exited") {
		t.Fatalf("message = %q, want no already-exited fact for a leader that was still running", payload.Message)
	}
	waitForPidToDisappear(t, descendantPID, 5*time.Second)
}

// A foreground command that exits while its process group still holds
// descendants is promoted instead of blocking the turn until they exit.
func TestForegroundCommandIsPromotedWhenItsGroupOutlivesIt(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	out, err := ShellTool{}.Execute(jobTestCtx(), mustMarshal(t, map[string]any{
		"command":       "(sleep 2) & echo started",
		"yield_time_ms": 5000,
	}))
	if err != nil {
		t.Fatalf("ShellTool.Execute: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, groupPendingReason) {
		t.Fatalf("out = %q, want the group-pending promotion reason", out)
	}
	id := parseBackgroundJobID(t, out)
	j, ok := globalJobRegistry.get(id)
	if !ok {
		t.Fatalf("promoted job %s is not tracked", id)
	}
	if j.isFinished() {
		t.Fatalf("promoted job %s must still be running while its group holds a descendant", id)
	}
}

// The same promotion applies when the caller asked for no yield budget: a
// command that exits while its group still holds descendants must not block the
// turn until the group drains or the deadline kills it.
func TestForegroundCommandIsPromotedWhenItsGroupOutlivesItWithoutYieldBudget(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })

	out, err := ShellTool{}.Execute(jobTestCtx(), mustMarshal(t, map[string]any{
		"command":       "(sleep 2) & echo started",
		"yield_time_ms": 0,
	}))
	if err != nil {
		t.Fatalf("ShellTool.Execute: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, groupPendingReason) {
		t.Fatalf("out = %q, want the group-pending promotion reason", out)
	}
	id := parseBackgroundJobID(t, out)
	j, ok := globalJobRegistry.get(id)
	if !ok {
		t.Fatalf("promoted job %s is not tracked", id)
	}
	if j.isFinished() {
		t.Fatalf("promoted job %s must still be running while its group holds a descendant", id)
	}
}

// A stop that cannot attribute the group to this job must not signal it: once
// the direct command is reaped, a live group number may have been recycled, so
// signalling risks hitting an unrelated group. A witness whose members have all
// exited is no better evidence than no witness at all, and neither is a recorded
// pid that is still alive but outside the group, which is what a member that
// detaches itself (setsid) leaves behind. All three cases report the teardown as
// unconfirmed and leave the descendant alone.
func TestStopWithoutALiveWitnessDoesNotSignalTheGroup(t *testing.T) {
	for _, tt := range []struct {
		name    string
		witness []int
	}{
		{name: "never witnessed"},
		{name: "witness already exited", witness: []int{reapedProcessID(t)}},
		{name: "witness alive outside the group", witness: []int{os.Getpid()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "descendant.pid")
			cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > "+strconv.Quote(pidFile)+"; exit 0")
			configureCommandProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start command: %v", err)
			}
			pgid := cmd.Process.Pid
			descendantPID := readJobPidFile(t, pidFile)
			t.Cleanup(func() {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
				_, _ = cmd.Process.Wait()
			})

			waitCh := waitForCommand(cmd)
			if !waitForCommandExit(waitCh, 5*time.Second) {
				t.Fatal("the direct command did not exit")
			}
			// Only the descendant keeps the group alive.
			if alive, known := processGroupAlive(pgid); !alive || !known {
				t.Fatalf("processGroupAlive(pgid) = (%v, %v), want the descendant to hold the group open", alive, known)
			}
			// The member is visible to a process-table sample, which is exactly
			// what a stop must no longer treat as this job's identity proof.
			if members := processGroupMembers(pgid); len(members) == 0 {
				t.Fatal("expected the descendant to be visible to a process-table sample")
			}

			j := &job{ID: "no-live-witness", cmd: cmd}
			if tt.witness != nil {
				j.setGroupWitness(tt.witness)
			}
			stop := stopJobProcessGroup(j, waitCh)
			if stop.outcome != jobGroupStopUnconfirmed {
				t.Fatalf("stop outcome = %v, want jobGroupStopUnconfirmed", stop.outcome)
			}
			if !stop.leaderReapedBefore {
				t.Fatal("the stop arrived after the direct command was reaped")
			}
			if !processAliveForTest(descendantPID) {
				t.Fatalf("descendant pid %d was signalled despite the missing witness", descendantPID)
			}
		})
	}
}

// A drain that cannot list the group's members still tracks the group itself.
// The group's membership is the liveness signal and the listing is only the
// witness a stop needs, so losing it must not end the job while a descendant
// still runs.
func TestGroupDrainWaitsForAGroupItCannotWitness(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	shortenJobGroupTimings(t)

	restoreMembers := jobGroupMembers
	jobGroupMembers = func(int) []int { return nil }
	t.Cleanup(func() { jobGroupMembers = restoreMembers })

	id, err := ExecuteJobForTest(jobTestCtx(), "sleep 60 & exit 0", "unwitnessed descendant", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	j := waitForJobGroupPending(t, id)
	pgid := commandProcessGroupIDForTest(t, j)
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	// The descendant holds the group open, so a job that ended with its command
	// would already be finished here.
	time.Sleep(10 * jobGroupPollInterval)
	if j.isFinished() {
		t.Fatalf("job %s ended while its unwitnessed group still held a descendant", id)
	}

	// The group itself is still the boundary: once it empties, the job ends and
	// says the group drained.
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill process group: %v", err)
	}
	waitForJobToFinish(t, j)
	if text := j.statusText(); !strings.Contains(text, "the process group drained") {
		t.Fatalf("status = %q, want the drained claim once the group is empty", text)
	}
}

// A descendant forked after the witness was recorded must keep the job active.
// The witness is a sample taken when the direct command exited; a later fork
// inherits the group without appearing in it, so its members exiting cannot be
// read as the group number having been recycled while the group still answers.
func TestGroupDrainKeepsRunningForDescendantsForkedAfterTheWitness(t *testing.T) {
	resetJobRegistryOnlyForTest(t)
	t.Cleanup(func() { StopAllJobsForShutdown() })
	shortenJobGroupTimings(t)
	// The terminal state is published only after the output copy is released, so
	// the backstop must not outlast the window this test asserts on: an early
	// drain would otherwise still look like a running job here.
	restoreGrace := jobOutputDrainGrace
	jobOutputDrainGrace = 50 * time.Millisecond
	t.Cleanup(func() { jobOutputDrainGrace = restoreGrace })
	// The recorded witness is a short-lived subshell that forks a long-running
	// descendant through another shell and exits, leaving the group held by a
	// member the witness never recorded.
	id, err := ExecuteJobForTest(jobTestCtx(), "(sleep 1; sh -c 'sleep 60 &') & exit 0", "late descendant", nil)
	if err != nil {
		t.Fatalf("ExecuteJobForTest: %v", err)
	}
	j := waitForJobGroupPending(t, id)
	pgid := commandProcessGroupIDForTest(t, j)
	witness := waitForJobWitness(t, j, pgid)
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	// Wait for the state that used to be mistaken for a recycled group number:
	// every recorded member is gone and the group holds one that was never
	// recorded.
	deadline := time.Now().Add(10 * time.Second)
	for !lateMemberHoldsGroup(pgid, witness) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the late descendant to take over the group")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("witness=%v members=%v", witness, processGroupMembers(pgid))

	// The job must stay active while that descendant holds the group, which the
	// group itself proves even though the witness no longer can.
	time.Sleep(10 * jobGroupPollInterval)
	if j.isFinished() {
		t.Fatalf("job %s ended while a descendant forked after the witness still ran", id)
	}

	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill process group: %v", err)
	}
	waitForJobToFinish(t, j)
	if text := j.statusText(); !strings.Contains(text, "the process group drained") {
		t.Fatalf("status = %q, want the drained claim once the group is empty", text)
	}
}

// lateMemberHoldsGroup reports whether the group holds a member that is not in
// the witness while every recorded member has exited.
func lateMemberHoldsGroup(pgid int, witness []int) bool {
	members := processGroupMembers(pgid)
	if len(members) == 0 {
		return false
	}
	unrecorded := false
	for _, pid := range members {
		if !slices.Contains(witness, pid) {
			unrecorded = true
			break
		}
	}
	if !unrecorded {
		return false
	}
	return !slices.ContainsFunc(witness, processAliveForTest)
}

// waitForJobWitness waits until the drain has recorded the descendant witness.
// The witness is sampled just after the phase boundary is published, so a
// reader that only waits for the boundary can still observe it empty.
func waitForJobWitness(t *testing.T, j *job, pgid int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if witness := j.recordedWitness(); len(witness) > 0 {
			return witness
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s recorded no descendant witness; members = %v", j.ID, processGroupMembers(pgid))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// commandProcessGroupIDForTest returns the group the job's command started. It
// waits for the drain-phase boundary first: that boundary is published after
// the command was reaped, which orders this read after os/exec filled the
// process in. Capturing the value while the command still starts would both
// race with os/exec and risk a zero group id, which signals the caller's own
// group.
func commandProcessGroupIDForTest(t *testing.T, j *job) int {
	t.Helper()
	waitForJobGroupPending(t, j.ID)
	if pgid := j.commandProcessGroupID(); pgid > 0 {
		return pgid
	}
	t.Fatalf("job %s has no process group after its command was reaped", j.ID)
	return 0
}

// waitForJobToFinish waits until the job records its terminal state.
func waitForJobToFinish(t *testing.T, j *job) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !j.isFinished() {
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish", j.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func shortenJobGroupTimings(t *testing.T) {
	t.Helper()
	restoreGrace := killGracePeriod
	killGracePeriod = 100 * time.Millisecond
	t.Cleanup(func() { killGracePeriod = restoreGrace })
	restorePoll := jobGroupPollInterval
	jobGroupPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { jobGroupPollInterval = restorePoll })
}

func readJobPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out reading descendant pid from %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForJobGroupPending waits until the job has published its phase boundary,
// i.e. the direct command is reaped and only descendants keep the group alive.
func waitForJobGroupPending(t *testing.T, id string) *job {
	t.Helper()
	j, ok := globalJobRegistry.get(id)
	if !ok {
		t.Fatalf("job %s is not tracked", id)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-j.groupPendingSignal():
			return j
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job %s to enter its process-group drain phase", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForJobFinishedPayload(t *testing.T, sender *recordingEventSender) *JobFinishedPayload {
	t.Helper()
	select {
	case raw := <-sender.ch:
		payload, ok := raw.(*JobFinishedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *JobFinishedPayload", raw)
		}
		return payload
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the stopped job's completion event")
		return nil
	}
}

// processAliveForTest reports whether pid still names a live process.
func processAliveForTest(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func waitForPidToDisappear(t *testing.T, pid int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if !processAliveForTest(pid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d is still alive after %s", pid, budget)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
