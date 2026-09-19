package agent

import (
	"testing"
	"time"
)

// A panic that aborts a drain pass must not silently lose the in-flight job:
// the recovery requeues it with a charged attempt and a backoff so the worker
// retries the same session instead of only waking for later jobs.
func TestRecoverMemoryDrainPanicRequeuesInflightJob(t *testing.T) {
	a := &MainAgent{}
	job := memoryJob{sessionDir: "/sessions/sample-session", attempts: 1}
	other := memoryJob{sessionDir: "/sessions/other-session"}
	a.memoryPending = []memoryJob{other}
	cancelled := make(chan struct{})
	a.memoryInflight = &memoryInflight{
		sessionDir: job.sessionDir,
		cancel:     func() { close(cancelled) },
		job:        job,
	}

	func() {
		defer a.recoverMemoryDrainPanic()
		panic("extraction exploded")
	}()

	select {
	case <-cancelled:
	default:
		t.Fatal("panic recovery did not cancel the interrupted extraction")
	}
	if a.memoryInflight != nil {
		t.Fatal("inflight guard not released after panic")
	}
	if len(a.memoryPending) != 2 {
		t.Fatalf("pending len = %d, want the interrupted job requeued ahead of the rest", len(a.memoryPending))
	}
	head := a.memoryPending[0]
	if head.sessionDir != job.sessionDir {
		t.Fatalf("requeued head = %q, want %q", head.sessionDir, job.sessionDir)
	}
	if head.attempts != job.attempts+1 {
		t.Fatalf("requeued attempts = %d, want %d", head.attempts, job.attempts+1)
	}
	if !head.retryAt.After(time.Now()) {
		t.Fatalf("requeued retryAt = %v, want a future backoff", head.retryAt)
	}
	if a.memoryPending[1].sessionDir != other.sessionDir {
		t.Fatalf("later job = %q, want it preserved behind the retry", a.memoryPending[1].sessionDir)
	}
}

// A job that keeps panicking past the attempt cap is dropped rather than
// retried forever.
func TestRecoverMemoryDrainPanicDropsJobPastAttemptCap(t *testing.T) {
	a := &MainAgent{}
	job := memoryJob{sessionDir: "/sessions/sample-session", attempts: memoryMaxExtractionAttempts}
	a.memoryInflight = &memoryInflight{sessionDir: job.sessionDir, cancel: func() {}, job: job}

	func() {
		defer a.recoverMemoryDrainPanic()
		panic("extraction exploded")
	}()

	if a.memoryInflight != nil {
		t.Fatal("inflight guard not released after panic")
	}
	if len(a.memoryPending) != 0 {
		t.Fatalf("pending len = %d, want the job dropped past the attempt cap", len(a.memoryPending))
	}
}
