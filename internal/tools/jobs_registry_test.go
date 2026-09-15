package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMaybePruneJobLogsRateLimitsSweep(t *testing.T) {
	r := &JobRegistry{jobs: make(map[string]*job)}
	seedStale := func(dir, name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
		old := time.Now().Add(-2 * jobLogRetention)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("age %s: %v", path, err)
		}
		return path
	}

	dir := t.TempDir()
	first := seedStale(dir, "job-first.log")
	r.maybePruneJobLogs(dir)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("first sweep left %s (err=%v)", first, err)
	}

	// A second sweep inside the interval must not rescan, so a stale file that
	// appears afterwards survives until the interval elapses.
	second := seedStale(dir, "job-second.log")
	r.maybePruneJobLogs(dir)
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("rate-limited sweep removed %s: %v", second, err)
	}

	// A different directory prunes immediately instead of waiting out the
	// interval that belongs to another session's log dir.
	otherDir := t.TempDir()
	other := seedStale(otherDir, "job-other.log")
	r.maybePruneJobLogs(otherDir)
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("sweep for a new dir left %s (err=%v)", other, err)
	}
}

// A failed disk write must not cost the model its output: io.MultiWriter stops
// at the first error, so an error from the log sink would otherwise drop the
// bytes from the in-memory window and kill the child process mid-run.
func TestJobLogWriteFailureKeepsTeedMemoryCopy(t *testing.T) {
	sessionDir := t.TempDir()
	writer, err := openRotatingJobLog(sessionDir, filepath.Join(sessionDir, "job-1.log"))
	if err != nil {
		t.Fatalf("openRotatingJobLog: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	mem := newTailWriter(maxOutputBytes)
	tee := io.MultiWriter(writer, mem)
	if _, err := tee.Write([]byte("kept")); err != nil {
		t.Fatalf("tee write = %v, want the log failure swallowed", err)
	}
	if got := mem.String(); got != "kept" {
		t.Fatalf("memory copy = %q, want %q", got, "kept")
	}
}

// The dedup answer has to outlive the job entry: eviction reclaims the output
// buffer, and an evicted-but-already-surfaced job must not be delivered again
// when its notification is replayed.
func TestClaimReportedSurvivesEviction(t *testing.T) {
	r := &JobRegistry{jobs: make(map[string]*job)}
	r.jobs["job-1"] = &job{ID: "job-1", finished: true}
	if !r.claimReported("job-1") {
		t.Fatal("first claim = false, want true")
	}
	// Push the retained count past the cap so the real eviction path runs;
	// job-1 has the oldest finish time, so it is the one dropped.
	for i := range maxRetainedFinishedJobs + 1 {
		id := fmt.Sprintf("old-%d", i)
		r.jobs[id] = &job{ID: id, finished: true, finishedAt: time.Unix(int64(i), 0)}
	}
	r.mu.Lock()
	r.evictFinishedLocked()
	r.mu.Unlock()
	if _, ok := r.jobs["job-1"]; ok {
		t.Fatal("job-1 survived eviction; the test did not exercise the path")
	}
	if r.claimReported("job-1") {
		t.Fatal("claim after eviction = true, want false: the result was already surfaced")
	}
	// An id the registry never owned is still delivered: it cannot know the
	// result reached the model and must not silently drop a replay.
	if !r.claimReported("job-unknown") {
		t.Fatal("unknown id claim = false, want true")
	}
}

func TestCompletionMessageOmitsFollowUpForSuccessfulJob(t *testing.T) {
	j := &job{
		ID:          "job-7",
		Description: "Run the full test suite",
		Command:     "go test ./...",
		output:      newTailWriter(maxOutputBytes),
	}
	if _, err := j.output.Write([]byte("ok\n")); err != nil {
		t.Fatalf("write output: %v", err)
	}

	msg := j.completionMessage(jobStatusCompleted, "completed (exit code 0)")
	for _, want := range []string{
		"[Background job job-7 finished]",
		"Status: completed (exit code 0)",
		"Purpose: Run the full test suite",
		"Command: go test ./...",
		"Relevant output:\nok",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("completion message missing %q:\n%s", want, msg)
		}
	}
	// A successful completion is informational: pointing the model at
	// job_output only invites a redundant read.
	if strings.Contains(msg, "job_output(") {
		t.Fatalf("successful completion must not ask for another read:\n%s", msg)
	}
}

func TestCompletionMessagePointsAtOutputAfterFailure(t *testing.T) {
	j := &job{
		ID:      "job-8",
		Command: "echo hi",
		output:  newTailWriter(maxOutputBytes),
	}
	msg := j.completionMessage(jobStatusFailed, "exit code 1")
	if strings.Contains(msg, "Purpose:") {
		t.Fatalf("completion message should omit purpose when description is empty:\n%s", msg)
	}
	if !strings.Contains(msg, "Command: echo hi") {
		t.Fatalf("completion message missing command:\n%s", msg)
	}
	if !strings.Contains(msg, "job_output(job-8)") {
		t.Fatalf("failed completion must point at job_output:\n%s", msg)
	}
}

func TestRelevantOutputForCompletionKeepsTheTail(t *testing.T) {
	j := &job{ID: "job-9", output: newTailWriter(maxOutputBytes)}
	if _, err := j.output.Write([]byte(strings.Repeat("x", 900) + "FATAL: boom")); err != nil {
		t.Fatalf("write output: %v", err)
	}
	snippet := j.relevantOutputForCompletion()
	if !strings.Contains(snippet, "FATAL: boom") {
		t.Fatalf("snippet must keep the tail, got %q", snippet)
	}
	if !strings.HasPrefix(snippet, "...(showing the last") {
		t.Fatalf("oversized snippet must be marked as a tail excerpt, got %q", snippet)
	}
}

// Two batched reads by the same agent must split the window between them, not
// replay or drop it.
func TestReadIncrementalConcurrentClaimsDeliverEachByteOnce(t *testing.T) {
	j := &job{ID: "job-concurrent", output: newTailWriter(1 << 16)}
	const payload = "0123456789"
	if _, err := j.output.Write([]byte(payload)); err != nil {
		t.Fatalf("write output: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var got strings.Builder
	for range 2 {
		wg.Go(func() {
			chunk, _ := j.readIncremental("agent-1")
			mu.Lock()
			got.WriteString(chunk)
			mu.Unlock()
		})
	}
	wg.Wait()
	if got.String() != payload {
		t.Fatalf("concurrent claims = %q, want each byte exactly once", got.String())
	}
}

func TestSummarizeJobCommandKeepsFirstLineAndTruncates(t *testing.T) {
	if got := summarizeJobCommand("echo first\necho second"); got != "echo first" {
		t.Fatalf("summarizeJobCommand = %q, want only the first line", got)
	}
	got := summarizeJobCommand(strings.Repeat("x", 300))
	if len(got) <= 200 || !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("summarizeJobCommand(long) = %q, want a truncated first line", got)
	}
}

// A cancel that suppresses the notification also clears an earlier promotion's
// detached flag: the terminal state is delivered as a foreground result or not
// at all, never twice, and never into a session that no longer owns the job.
// This pins the last-writer-wins contract between detach and requestCancel that
// a session switch, an agent stop, and job_kill all rely on.
func TestRequestCancelClearsPromotedDetach(t *testing.T) {
	j := &job{status: jobStatusRunning, cancelCh: make(chan string, 1)}
	if !j.detach() {
		t.Fatal("detach on a running job must report true")
	}
	if !j.detached {
		t.Fatal("detach must mark the job background-owned")
	}
	if !j.requestCancel("terminated on session switch", false) {
		t.Fatal("requestCancel on a running job must report true")
	}
	if j.detached {
		t.Fatal("a notification-suppressing cancel must clear the promoted detached flag")
	}
	if j.status != jobStatusStopping {
		t.Fatalf("status = %q, want %q", j.status, jobStatusStopping)
	}
	if j.isKilled() {
		// isKilled reports the terminal status, which a pending cancel has not
		// set: the job is still stopping, not killed.
		t.Fatal("a pending cancel must not report a terminal kill")
	}
}

// A job is readable by more than its owner, so one reader must not consume
// another's output or advance another's anti-polling streak. Before readers had
// their own cursors, a single main-agent read left the owner with an empty
// window and a streak that refused its next calls outright.
func TestReadersDoNotConsumeEachOthersOutputOrStreak(t *testing.T) {
	j := &job{ID: "job-two-readers", output: newTailWriter(1 << 16)}
	if _, err := j.output.Write([]byte("first batch\n")); err != nil {
		t.Fatalf("write output: %v", err)
	}

	if chunk, _ := j.readIncremental("main"); chunk != "first batch\n" {
		t.Fatalf("main read %q, want the full output", chunk)
	}
	if chunk, _ := j.readIncremental("owner"); chunk != "first batch\n" {
		t.Fatalf("owner read %q, want the same output main already read", chunk)
	}

	// Each reader advances independently from here.
	if _, err := j.output.Write([]byte("second batch\n")); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if chunk, _ := j.readIncremental("owner"); chunk != "second batch\n" {
		t.Fatalf("owner read %q, want only the new output", chunk)
	}
	if chunk, _ := j.readIncremental("owner"); chunk != "" {
		t.Fatalf("owner re-read %q, want nothing left", chunk)
	}
	if chunk, _ := j.readIncremental("main"); chunk != "second batch\n" {
		t.Fatalf("main read %q, want the new output the owner already consumed", chunk)
	}

	// Streaks are per reader: main polling itself into a refusal leaves the
	// owner's first read at streak 1.
	for i := 1; i <= jobOutputPollRefuseStreak; i++ {
		if got := j.noteReadOutcome("main", true); got != i {
			t.Fatalf("main streak = %d, want %d", got, i)
		}
	}
	if got := j.noteReadOutcome("owner", true); got != 1 {
		t.Fatalf("owner streak = %d, want 1 — main's polling must not refuse the owner", got)
	}
	if got := j.noteReadOutcome("main", false); got != 0 {
		t.Fatalf("main streak after new bytes = %d, want 0", got)
	}
}

// outputWaitState answers per reader, so a wait:output call does not return
// immediately just because some other agent has not caught up.
func TestHasUnreadOutputIsPerReader(t *testing.T) {
	j := &job{ID: "job-unread", output: newTailWriter(1 << 16)}
	if _, err := j.output.Write([]byte("data")); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if _, unread := j.outputWaitState("owner"); !unread {
		t.Fatal("owner has unread output, want true")
	}
	if _, _ = j.readIncremental("owner"); func() bool {
		_, unread := j.outputWaitState("owner")
		return unread
	}() {
		t.Fatal("owner consumed its window, want false")
	}
	if _, unread := j.outputWaitState("main"); !unread {
		t.Fatal("main has not read yet, want true")
	}
}

// Every reader waiting on the same output generation must wake from one write.
// The channel is closed, rather than receiving one shared token, so a main
// agent and its owner cannot leave one another parked until the wait timeout.
func TestOutputWaitStateBroadcastsToAllReaders(t *testing.T) {
	j := &job{ID: "job-broadcast", output: newTailWriter(1 << 16)}
	ownerSignal, ownerUnread := j.outputWaitState("owner")
	mainSignal, mainUnread := j.outputWaitState("main")
	if ownerUnread || mainUnread {
		t.Fatalf("empty output reported unread: owner=%v main=%v", ownerUnread, mainUnread)
	}
	if ownerSignal != mainSignal {
		t.Fatal("readers waiting on one output generation must share its signal")
	}

	if _, err := j.output.Write([]byte("new output")); err != nil {
		t.Fatalf("write output: %v", err)
	}
	for name, signal := range map[string]<-chan struct{}{
		"owner": ownerSignal,
		"main":  mainSignal,
	} {
		select {
		case <-signal:
		default:
			t.Fatalf("%s reader was not woken by the output write", name)
		}
	}
}
