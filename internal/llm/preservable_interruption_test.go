package llm

import (
	"errors"
	"io"
	"testing"
	"time"
)

// TestIsPreservableStreamInterruption covers the exported classification the
// agent uses to decide whether a failed request may carry already-streamed
// assistant text that should be saved and resumed instead of discarded.
func TestIsPreservableStreamInterruption(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"interrupted response", &InterruptedResponseError{StopReason: "interrupted"}, true},
		{"chunk read timeout", &ChunkTimeoutError{d: 90 * time.Second}, true},
		{"truncated transport", io.ErrUnexpectedEOF, true},
		{"plain error", errors.New("boom"), false},
		{"wrapped plain error", errors.New("reading SSE stream: boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPreservableStreamInterruption(tt.err); got != tt.want {
				t.Fatalf("IsPreservableStreamInterruption(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
