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
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk, _ := j.readIncremental()
			mu.Lock()
			got.WriteString(chunk)
			mu.Unlock()
		}()
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
