package llm

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultChunkTimeout is the normal per-chunk idle timeout for visible text
	// streaming phases. It should be long enough to tolerate slow upstream
	// generation without making genuinely dead streams feel hung forever.
	DefaultChunkTimeout = 60 * time.Second
	// SlowPhaseChunkTimeout is used for phases known to stall longer between
	// chunks, such as thinking/reasoning or large tool argument generation.
	SlowPhaseChunkTimeout = 90 * time.Second
)

// ChunkTimeoutError is a net.Error-compatible error returned when no SSE chunk
// arrives within the configured timeout. It satisfies isPerKeyTimeoutRetry so
// the retry logic rotates API keys before falling back to another model.
type ChunkTimeoutError struct{ d time.Duration }

func (e *ChunkTimeoutError) Error() string {
	return "chunk read timeout: no data from model for " + e.d.String()
}
func (e *ChunkTimeoutError) Timeout() bool   { return true }
func (e *ChunkTimeoutError) Temporary() bool { return true }

// chunkPhaser is the optional interface that SSE parsers use to adjust the
// per-chunk timeout when entering or leaving a slow phase (thinking / tool_use).
type chunkPhaser interface {
	SetChunkTimeout(d time.Duration)
}

type chunkTimeoutSnapshot struct {
	Timeout             time.Duration
	TimedOut            bool
	LastReadBytes       int
	LastReadErr         string
	TotalBytes          int64
	LastByteAt          time.Time
	TimeoutFiredAt      time.Time
	TimeoutReadReturned bool
	TimeoutReadBytes    int
}

type chunkTimeoutDiagnostics interface {
	chunkTimeoutSnapshot() chunkTimeoutSnapshot
}

// ChunkTimeoutReader wraps an io.Reader and fires cancel() if no data arrives
// within the current timeout. After each successful Read, the timer is reset.
// Call SetChunkTimeout to change the timeout (also resets the timer immediately).
//
// When the timer fires it sets timedOut and calls cancel so the underlying
// HTTP body read is interrupted. Read() then returns a *ChunkTimeoutError
// (implements net.Error) instead of context.Canceled, so error classification
// treats it as a per-key retriable read-phase timeout.
type ChunkTimeoutReader struct {
	r        io.Reader
	cancel   func()
	mu       sync.Mutex
	timer    *time.Timer
	timeout  time.Duration
	timedOut atomic.Bool

	lastReadBytes       int
	lastReadErr         string
	totalBytes          int64
	lastByteAt          time.Time
	timeoutFiredAt      time.Time
	timeoutReadReturned bool
	timeoutReadBytes    int
}

// NewChunkTimeoutReader wraps r with per-chunk deadline enforcement.
// initialTimeout is the timeout for the first chunk.
// cancel must cancel the context used for the HTTP request (so the read unblocks).
func NewChunkTimeoutReader(r io.Reader, initialTimeout time.Duration, cancel func()) *ChunkTimeoutReader {
	cr := &ChunkTimeoutReader{
		r:       r,
		cancel:  cancel,
		timeout: initialTimeout,
	}
	cr.timer = time.AfterFunc(initialTimeout, cr.fireTimeout)
	return cr
}

func (cr *ChunkTimeoutReader) fireTimeout() {
	cr.mu.Lock()
	cr.timeoutFiredAt = time.Now()
	cr.mu.Unlock()
	cr.timedOut.Store(true)
	cr.cancel()
}

func (cr *ChunkTimeoutReader) Read(p []byte) (int, error) {
	if cr.timedOut.Load() {
		cr.mu.Lock()
		d := cr.timeout
		cr.timeoutReadReturned = true
		cr.timeoutReadBytes = 0
		cr.mu.Unlock()
		return 0, &ChunkTimeoutError{d}
	}
	n, err := cr.r.Read(p)
	now := time.Now()
	cr.mu.Lock()
	cr.lastReadBytes = n
	if err != nil {
		cr.lastReadErr = err.Error()
	} else {
		cr.lastReadErr = ""
	}
	if n > 0 {
		cr.totalBytes += int64(n)
		cr.lastByteAt = now
	}
	timedOut := cr.timedOut.Load()
	if err != nil && timedOut {
		cr.timeoutReadReturned = true
		cr.timeoutReadBytes = n
	}
	d := cr.timeout
	cr.mu.Unlock()
	if err != nil && cr.timedOut.Load() {
		// Timer fired concurrently; surface ChunkTimeoutError instead of
		// context.Canceled so error classification can rotate keys.
		return n, &ChunkTimeoutError{d}
	}
	if n > 0 {
		cr.mu.Lock()
		cr.timer.Reset(cr.timeout)
		cr.mu.Unlock()
	}
	return n, err
}

// SetChunkTimeout updates the current timeout and immediately resets the timer.
func (cr *ChunkTimeoutReader) SetChunkTimeout(d time.Duration) {
	cr.mu.Lock()
	cr.timeout = d
	cr.timer.Reset(d)
	cr.mu.Unlock()
}

// Stop cancels the internal timer. Call on stream completion to avoid leaks.
func (cr *ChunkTimeoutReader) Stop() {
	cr.mu.Lock()
	cr.timer.Stop()
	cr.mu.Unlock()
}

func (cr *ChunkTimeoutReader) chunkTimeoutSnapshot() chunkTimeoutSnapshot {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return chunkTimeoutSnapshot{
		Timeout:             cr.timeout,
		TimedOut:            cr.timedOut.Load(),
		LastReadBytes:       cr.lastReadBytes,
		LastReadErr:         cr.lastReadErr,
		TotalBytes:          cr.totalBytes,
		LastByteAt:          cr.lastByteAt,
		TimeoutFiredAt:      cr.timeoutFiredAt,
		TimeoutReadReturned: cr.timeoutReadReturned,
		TimeoutReadBytes:    cr.timeoutReadBytes,
	}
}
