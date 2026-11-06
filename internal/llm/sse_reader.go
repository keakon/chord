package llm

import (
	"io"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/sse"
)

func newLLMSSEReader(reader io.Reader, cb StreamCallback, limit int, terminal func(sse.Event) bool) *sse.Reader {
	if cb != nil {
		reader = &sseStatusReader{Reader: reader, callback: cb}
	}
	return sse.NewReader(reader, limit, terminal)
}

type sseStatusReader struct {
	io.Reader
	callback StreamCallback
}

func (r *sseStatusReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && r.callback != nil {
		// Heartbeats and partial frames already prove that response bytes
		// arrived; waiting for a complete event would leave the UI at headers.
		r.callback(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaWaitingToken}})
		r.callback = nil
	}
	return n, err
}
