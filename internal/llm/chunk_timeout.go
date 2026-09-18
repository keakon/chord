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
	// TerminalDrainChunkTimeout is used after the provider has already reported
	// terminal or recoverable content state and the parser is only waiting for
	// protocol trailers such as usage, [DONE], or response.completed.
	TerminalDrainChunkTimeout = 1 * time.Second
)

// ChunkTimeoutError is a net.Error-compatible error returned when no SSE chunk
// arrives within the configured timeout. Before any visible output it is treated
// like a provider-skip timeout (advance to another provider/model); after visible
// output the caller may retry on the same key.
type ChunkTimeoutError struct{ d time.Duration }

func (e *ChunkTimeoutError) Error() string {
	return "chunk read timeout: no data from model for " + e.d.String()
}
func (e *ChunkTimeoutError) Timeout() bool   { return true }
func (e *ChunkTimeoutError) Temporary() bool { return true }

// StreamTotalTimeoutError is the net.Error-compatible error returned when a
// stream kept producing data but never finished inside its wall-clock budget.
// It is the one shape the idle timeout cannot catch: every arrival resets the
// idle timer, so a stream that drips a byte just before each deadline stays
// alive forever. Classification treats it exactly like ChunkTimeoutError.
type StreamTotalTimeoutError struct{ d time.Duration }

func (e *StreamTotalTimeoutError) Error() string {
	return "stream total timeout: still producing data after " + e.d.String()
}
func (e *StreamTotalTimeoutError) Timeout() bool   { return true }
func (e *StreamTotalTimeoutError) Temporary() bool { return true }

// chunkPhaser is the optional interface that SSE parsers use to adjust the
// per-chunk timeout when entering or leaving a slow phase (thinking / tool_use).
type chunkPhaser interface {
	SetChunkTimeout(d time.Duration)
	SetTerminalDrainTimeout(d time.Duration)
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
// (implements net.Error) instead of context.Canceled, so higher-level retry
// classification can distinguish timeout handling from ordinary cancellation.
type ChunkTimeoutReader struct {
	r        io.Reader
	cancel   func()
	mu       sync.Mutex
	timer    *time.Timer
	timeout  time.Duration
	fixed    time.Duration
	timedOut atomic.Bool

	lastReadBytes       int
	lastReadErr         string
	totalBytes          int64
	lastByteAt          time.Time
	timeoutFiredAt      time.Time
	timeoutReadReturned bool
	timeoutReadBytes    int

	// totalBudget is an optional wall-clock cap for the whole stream. Unlike
	// the idle timer above it is never reset by incoming data, so it bounds the
	// one shape the idle timeout cannot: a stream that drips data often enough
	// to keep the idle timer alive but never finishes. Zero disables it — a
	// long healthy stream is not a fault, so the default keeps no cap.
	totalBudget   time.Duration
	totalTimer    *time.Timer
	totalTimedOut atomic.Bool
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

// NewProviderChunkTimeoutReader wraps r with the provider-level stream idle
// timeout when configured; otherwise it uses initialTimeout and allows parsers
// to switch to their built-in slow-phase timeout. Terminal drain may still
// shorten a provider-level timeout so completed content is not held hostage by
// optional protocol trailers.
func NewProviderChunkTimeoutReader(r io.Reader, provider *ProviderConfig, initialTimeout time.Duration, cancel func()) *ChunkTimeoutReader {
	var cr *ChunkTimeoutReader
	if provider != nil {
		if d := provider.StreamIdleTimeout(); d > 0 {
			cr = NewChunkTimeoutReader(r, d, cancel)
			cr.fixed = d
		}
	}
	if cr == nil {
		cr = NewChunkTimeoutReader(r, initialTimeout, cancel)
	}
	if provider != nil {
		cr.SetTotalTimeout(provider.StreamTotalTimeout())
	}
	return cr
}

// SetTotalTimeout arms an optional wall-clock cap for the whole stream,
// measured from the moment the reader is wrapped. It is deliberately not reset
// by reads. A non-positive duration leaves the stream uncapped, which is the
// default: a stream that keeps producing data is slow, not broken, and only an
// operator who wants to bound that case opts in.
func (cr *ChunkTimeoutReader) SetTotalTimeout(d time.Duration) {
	cr.mu.Lock()
	cr.totalBudget = d
	if cr.totalTimer != nil {
		cr.totalTimer.Stop()
		cr.totalTimer = nil
	}
	if d > 0 {
		cr.totalTimer = time.AfterFunc(d, cr.fireTotalTimeout)
	}
	cr.mu.Unlock()
}

func (cr *ChunkTimeoutReader) fireTimeout() {
	cr.mu.Lock()
	cr.timeoutFiredAt = time.Now()
	cr.mu.Unlock()
	cr.timedOut.Store(true)
	cr.cancel()
}

func (cr *ChunkTimeoutReader) fireTotalTimeout() {
	cr.totalTimedOut.Store(true)
	cr.cancel()
}

// streamError converts a timer-fired read into the error classification the
// retry layer understands. Both timers cancel the request context, so the
// underlying read surfaces context.Canceled unless this reports the real cause.
func (cr *ChunkTimeoutReader) streamError() error {
	if cr.totalTimedOut.Load() {
		cr.mu.Lock()
		d := cr.totalBudget
		cr.mu.Unlock()
		return &StreamTotalTimeoutError{d}
	}
	if cr.timedOut.Load() {
		cr.mu.Lock()
		d := cr.timeout
		cr.mu.Unlock()
		return &ChunkTimeoutError{d}
	}
	return nil
}

func (cr *ChunkTimeoutReader) Read(p []byte) (int, error) {
	if err := cr.streamError(); err != nil {
		if _, total := err.(*StreamTotalTimeoutError); !total {
			cr.mu.Lock()
			cr.timeoutReadReturned = true
			cr.timeoutReadBytes = 0
			cr.mu.Unlock()
		}
		return 0, err
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
	cr.mu.Unlock()
	if err != nil {
		// A timer fired concurrently; surface the timeout instead of
		// context.Canceled so error classification can rotate keys.
		if timeout := cr.streamError(); timeout != nil {
			return n, timeout
		}
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
	if cr.fixed > 0 {
		d = cr.fixed
	}
	cr.timeout = d
	cr.timer.Reset(d)
	cr.mu.Unlock()
}

// SetTerminalDrainTimeout updates the current timeout for terminal drain.
// Provider-level stream_idle_timeout can still shorten the drain, but must not
// force terminal trailers to wait longer than the parser's terminal budget.
func (cr *ChunkTimeoutReader) SetTerminalDrainTimeout(d time.Duration) {
	cr.mu.Lock()
	if cr.fixed > 0 && cr.fixed < d {
		d = cr.fixed
	}
	cr.timeout = d
	cr.timer.Reset(d)
	cr.mu.Unlock()
}

// Stop cancels both timers. Call on stream completion to avoid leaks.
func (cr *ChunkTimeoutReader) Stop() {
	cr.mu.Lock()
	cr.timer.Stop()
	if cr.totalTimer != nil {
		cr.totalTimer.Stop()
		cr.totalTimer = nil
	}
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
