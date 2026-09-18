package llm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// TestChunkTimeoutReaderTimerResetsOnEachDataRead verifies that the per-chunk timer
// resets on every Read that returns data, ensuring the timeout measures
// interval-between-events rather than total stream time.
func TestChunkTimeoutReaderTimerResetsOnEachDataRead(t *testing.T) {
	chunks := []struct {
		data []byte
		err  error
	}{
		{data: []byte("data: first\n\n"), err: nil},
		{data: []byte("data: second\n\n"), err: nil},
		{data: []byte("data: third\n\n"), err: io.EOF},
	}
	chIdx := 0
	reader := &slowChunkReader{chunks: chunks, next: &chIdx}
	cancelled := false
	cancel := func() { cancelled = true }

	// Use 200ms per-chunk timeout: if the timer were NOT resetting between
	// reads, the total would exceed 200ms and trigger the timeout.
	cr := NewChunkTimeoutReader(reader, 200*time.Millisecond, cancel)
	defer cr.Stop()

	var readData []string
	buf := make([]byte, 4096)
	for {
		n, err := cr.Read(buf)
		if n > 0 {
			readData = append(readData, string(buf[:n]))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("unexpected error: %v (cancelled=%v)", err, cancelled)
		}
	}

	if cancelled {
		t.Fatal("cancel was called; timer should have been reset on each data read")
	}
	if len(readData) == 0 {
		t.Fatal("no data read")
	}
}

// TestProviderChunkTimeoutReaderUsesFixedProviderIdleTimeout pins that the
// provider idle override still wins over the parser's own phase timeouts.
func TestProviderChunkTimeoutReaderUsesFixedProviderIdleTimeout(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:              config.ProviderTypeResponses,
		StreamIdleTimeout: 3,
	}, nil)
	cancel := func() {}
	cr := NewProviderChunkTimeoutReader(strings.NewReader("data"), p, DefaultChunkTimeout, cancel)
	defer cr.Stop()

	if cr.timeout != 3*time.Second {
		t.Fatalf("initial timeout = %v, want 3s", cr.timeout)
	}
	cr.SetChunkTimeout(SlowPhaseChunkTimeout)
	if cr.timeout != 3*time.Second {
		t.Fatalf("slow phase timeout = %v, want fixed provider timeout 3s", cr.timeout)
	}
}

// TestProviderChunkTimeoutReaderLeavesHealthyStreamUncappedByDefault pins the
// documented default: a stream that keeps producing data is never cut off by
// wall-clock time alone. Only stream_idle_timeout bounds it, and every arrival
// resets that timer.
func TestProviderChunkTimeoutReaderLeavesHealthyStreamUncappedByDefault(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
	}, nil)
	cancel := func() {}
	cr := NewProviderChunkTimeoutReader(strings.NewReader("data"), p, DefaultChunkTimeout, cancel)
	defer cr.Stop()

	if cr.totalBudget != 0 {
		t.Fatalf("total budget = %v, want 0 (uncapped) when stream_total_timeout is unset", cr.totalBudget)
	}
	if cr.totalTimer != nil {
		t.Fatal("total timer armed for an uncapped stream")
	}
}

// TestStreamTotalTimeoutBoundsSlowDrip pins the gap the idle timeout cannot
// close: a stream that drips one byte just before each idle deadline resets
// the idle timer every time, so without a wall-clock cap it never ends.
// A steady drip must still be cut off once the budget elapses.
func TestStreamTotalTimeoutBoundsSlowDrip(t *testing.T) {
	p := NewProviderConfig("test", config.ProviderConfig{
		Type:               config.ProviderTypeResponses,
		StreamIdleTimeout:  1,
		StreamTotalTimeout: 1,
	}, nil)
	// The timer goroutine fires the total budget, so the flag must be atomic.
	var cancelled atomic.Bool
	cancel := func() { cancelled.Store(true) }
	// Drip faster than the idle timeout so only the total budget can fire.
	cr := NewProviderChunkTimeoutReader(&dripReader{interval: 100 * time.Millisecond}, p, DefaultChunkTimeout, cancel)
	defer cr.Stop()

	buf := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if _, err = cr.Read(buf); err != nil {
			break
		}
	}
	total, ok := errors.AsType[*StreamTotalTimeoutError](err)
	if !ok {
		t.Fatalf("read error = %v, want *StreamTotalTimeoutError", err)
	}
	if !cancelled.Load() {
		t.Fatal("cancel was not called when the total budget fired")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !total.Timeout() {
		t.Fatal("StreamTotalTimeoutError must be a net.Error timeout so retry classification rotates keys")
	}
}

// TestStreamTotalTimeoutSurvivesCancelMasking pins that a total-budget fire is
// reported as a timeout even when the cancelled underlying read surfaces
// context.Canceled: classification looks at the error to decide whether to
// rotate keys, and an opaque cancellation would be treated as a user abort.
func TestStreamTotalTimeoutSurvivesCancelMasking(t *testing.T) {
	// The timer goroutine fires the total budget, so the flag must be atomic.
	var cancelled atomic.Bool
	cancel := func() { cancelled.Store(true) }
	cr := NewChunkTimeoutReader(&blockingReader{cancel: cancel}, time.Hour, cancel)
	defer cr.Stop()
	cr.SetTotalTimeout(50 * time.Millisecond)

	_, err := cr.Read(make([]byte, 8))
	if _, ok := errors.AsType[*StreamTotalTimeoutError](err); !ok {
		t.Fatalf("read error = %v, want *StreamTotalTimeoutError (underlying read returns context.Canceled)", err)
	}
	if !cancelled.Load() {
		t.Fatal("cancel was not called when the total budget fired")
	}
}

// dripReader returns one byte per interval, forever — the slow-drip shape that
// keeps an idle timer alive indefinitely.
type dripReader struct {
	interval time.Duration
}

func (r *dripReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	time.Sleep(r.interval)
	p[0] = 'x'
	return 1, nil
}

// blockingReader blocks until the total-budget timer cancels it, then reports
// the cancellation, which is the masking case classification must survive.
type blockingReader struct {
	cancel func()
}

func (r *blockingReader) Read(_ []byte) (int, error) {
	<-time.After(2 * time.Second)
	return 0, context.Canceled
}

// slowChunkReader delivers chunks with simulated inter-chunk delays.
type slowChunkReader struct {
	chunks []struct {
		data []byte
		err  error
	}
	next *int
}

func (r *slowChunkReader) Read(p []byte) (int, error) {
	idx := *r.next
	if idx >= len(r.chunks) {
		return 0, io.EOF
	}
	if idx > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	chunk := r.chunks[idx]
	*r.next++
	n := copy(p, chunk.data)
	return n, chunk.err
}

// TestChunkTimeoutReaderInterEventNotTotalTime verifies the critical invariant:
// the per-chunk timeout measures the interval between events, not total stream
// time. A stream that continuously delivers data (with each inter-event gap
// well under the timeout) should NOT be killed even if the total stream time
// far exceeds the timeout. This test catches the class of bugs where the timer
// is not properly reset after each data read.
func TestChunkTimeoutReaderInterEventNotTotalTime(t *testing.T) {
	// 10 chunks, each with 50ms delay before it. Total time ~500ms.
	// Per-chunk timeout is 200ms (well above the 50ms inter-chunk gap).
	// If the timer incorrectly measured total time instead of inter-event,
	// it would fire after 200ms (during chunk ~4).
	const numChunks = 10
	chunks := make([]struct {
		data []byte
		err  error
	}, numChunks)
	for i := range chunks {
		chunks[i].data = fmt.Appendf(nil, "data: chunk%d\n\n", i)
	}
	chunks[numChunks-1].err = io.EOF

	chIdx := 0
	reader := &slowChunkReader{chunks: chunks, next: &chIdx}
	cancelled := false
	cancel := func() { cancelled = true }

	cr := NewChunkTimeoutReader(reader, 200*time.Millisecond, cancel)
	defer cr.Stop()

	buf := make([]byte, 4096)
	var totalReads int
	for {
		n, err := cr.Read(buf)
		if n > 0 {
			totalReads++
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("unexpected error (cancelled=%v): %v; timer should measure inter-event gap, not total time", cancelled, err)
		}
	}

	if cancelled {
		t.Fatal("cancel was called; per-chunk timeout should measure inter-event gap, not total stream time")
	}
	if totalReads != numChunks {
		t.Fatalf("expected %d reads, got %d; stream was prematurely terminated", numChunks, totalReads)
	}
}

// TestChunkTimeoutReaderTimerResetViaBufioScanner verifies that the timer resets
// correctly when reads go through bufio.Scanner (the actual SSE parsing path).
// This catches bugs where bufio's internal buffering might cause the timer to
// not be reset because a single Read returns partial line data.
func TestChunkTimeoutReaderTimerResetViaBufioScanner(t *testing.T) {
	// Deliver an SSE-like stream with small chunks separated by delays.
	// Each chunk is smaller than the bufio buffer, so bufio may coalesce reads.
	chunks := []struct {
		data []byte
		err  error
	}{
		{data: []byte("data: event1\n\ndata: event2\n\n"), err: nil},
		// 100ms gap between reads — well within 500ms chunk timeout.
		{data: []byte("data: event3\n\n"), err: nil},
		{data: []byte("data: [DONE]\n\n"), err: io.EOF},
	}
	chIdx := 0
	reader := &slowChunkReader{chunks: chunks, next: &chIdx}
	cancelled := false
	cancel := func() { cancelled = true }

	cr := NewChunkTimeoutReader(reader, 500*time.Millisecond, cancel)
	defer cr.Stop()

	scanner := bufio.NewScanner(cr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		if _, ok := errors.AsType[*ChunkTimeoutError](err); ok {
			t.Fatalf("chunk timeout fired unexpectedly (cancelled=%v); timer should reset on each data read; got %v", cancelled, err)
		}
		t.Fatalf("scanner error: %v", err)
	}
	if cancelled {
		t.Fatal("cancel was called; timer should have been reset on each data read")
	}
	if len(lines) == 0 {
		t.Fatal("no lines read")
	}
}

type timeoutPartialReader struct {
	cancelled <-chan struct{}
	payload   []byte
	done      bool
}

func (r *timeoutPartialReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	<-r.cancelled
	r.done = true
	n := copy(p, r.payload)
	return n, context.Canceled
}

func TestChunkTimeoutReaderSnapshotCapturesPartialTimeoutRead(t *testing.T) {
	cancelled := make(chan struct{})
	reader := &timeoutPartialReader{
		cancelled: cancelled,
		payload:   []byte(`{"type":"response.output_text.delta","delta":"par`),
	}
	cancel := func() {
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}

	cr := NewChunkTimeoutReader(reader, 10*time.Millisecond, cancel)
	defer cr.Stop()

	buf := make([]byte, 256)
	n, err := cr.Read(buf)
	if n == 0 {
		t.Fatal("expected partial bytes to be returned before timeout surfaced")
	}
	if _, ok := errors.AsType[*ChunkTimeoutError](err); !ok {
		t.Fatalf("err = %v, want ChunkTimeoutError", err)
	}

	snap := cr.chunkTimeoutSnapshot()
	if !snap.TimedOut {
		t.Fatal("snapshot.TimedOut = false, want true")
	}
	if !snap.TimeoutReadReturned {
		t.Fatal("snapshot.TimeoutReadReturned = false, want true")
	}
	if snap.TimeoutReadBytes != n {
		t.Fatalf("snapshot.TimeoutReadBytes = %d, want %d", snap.TimeoutReadBytes, n)
	}
	if snap.LastReadBytes != n {
		t.Fatalf("snapshot.LastReadBytes = %d, want %d", snap.LastReadBytes, n)
	}
	if snap.TotalBytes != int64(n) {
		t.Fatalf("snapshot.TotalBytes = %d, want %d", snap.TotalBytes, n)
	}
	if snap.LastByteAt.IsZero() {
		t.Fatal("snapshot.LastByteAt is zero, want recorded timestamp")
	}
	if snap.TimeoutFiredAt.IsZero() {
		t.Fatal("snapshot.TimeoutFiredAt is zero, want recorded timestamp")
	}
}
