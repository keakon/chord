package llm

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
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

// TestVisibleStreamTrackerOnlyTextIsResumable guards the split between "the
// attempt produced visible output" (which decides whether the output stays on
// screen during a retry) and "the attempt produced body text" (which decides
// whether the callers can resume it). Thinking deltas are visible but never
// reach the caller's partial-text accumulator, so an interruption after
// reasoning alone must not escalate: escalating would end the turn instead of
// retrying, which is the opposite of what the preservation is for.
func TestVisibleStreamTrackerOnlyTextIsResumable(t *testing.T) {
	t.Run("thinking only is not resumable", func(t *testing.T) {
		tracker := &visibleStreamTracker{}
		tracker.Callback(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "let me work through this"})
		if !tracker.visible {
			t.Fatal("thinking deltas must still count as visible output")
		}
		if !tracker.MarkInterruptedVisibleOutput() {
			t.Fatal("MarkInterruptedVisibleOutput() = false, want true (nothing to roll back)")
		}
		if tracker.HadTextDelta() {
			t.Fatal("HadTextDelta() = true after thinking-only stream, want false")
		}
	})

	t.Run("body text is resumable", func(t *testing.T) {
		tracker := &visibleStreamTracker{}
		tracker.Callback(message.StreamDelta{Type: message.StreamDeltaThinking, Text: "done thinking"})
		tracker.Callback(message.StreamDelta{Type: message.StreamDeltaText, Text: "the second conflict"})
		if !tracker.MarkInterruptedVisibleOutput() {
			t.Fatal("MarkInterruptedVisibleOutput() = false, want true")
		}
		if !tracker.HadTextDelta() {
			t.Fatal("HadTextDelta() = false after a text delta, want true")
		}
	})

	t.Run("blank text is not resumable", func(t *testing.T) {
		tracker := &visibleStreamTracker{}
		tracker.Callback(message.StreamDelta{Type: message.StreamDeltaText, Text: "   \n\t"})
		tracker.MarkInterruptedVisibleOutput()
		if tracker.HadTextDelta() {
			t.Fatal("HadTextDelta() = true for whitespace-only text, want false")
		}
	})

	t.Run("roll back clears the text record", func(t *testing.T) {
		tracker := &visibleStreamTracker{}
		tracker.Callback(message.StreamDelta{Type: message.StreamDeltaText, Text: "partial"})
		tracker.Callback(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Reason: "retry"}})
		if tracker.HadTextDelta() {
			t.Fatal("HadTextDelta() = true after rollback, want false")
		}
	})
}
