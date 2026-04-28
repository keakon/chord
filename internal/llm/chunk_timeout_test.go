package llm

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

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
	var timeoutErr *ChunkTimeoutError
	if !errors.As(err, &timeoutErr) {
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
