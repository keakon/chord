package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/llm"
)

// The client escalates preservable stream interruptions instead of retrying
// them internally, which moved recovery into the caller. A SubAgent only
// recovers — and only then drains and saves the text it already streamed — when
// isTransientSubAgentTransportError accepts the error. These are exactly the
// escalated shapes; before the client started escalating them the SubAgent
// never saw them, so an unclassified shape now fails the turn outright and
// drops the partial reply.
func TestIsTransientSubAgentTransportErrorCoversEscalatedInterruptions(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"interrupted response", &llm.InterruptedResponseError{StopReason: "interrupted"}, true},
		{"chunk read timeout", &llm.ChunkTimeoutError{}, true},
		{"truncated transport", io.ErrUnexpectedEOF, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"in-band sse error event", &llm.APIError{Origin: llm.APIErrorOriginSSEEvent, Message: "response.failed: upstream closed"}, true},
		{"in-band websocket error event", &llm.APIError{Origin: llm.APIErrorOriginWebSocketEvent, Message: "stream error"}, true},
		{"server error status", &llm.APIError{Origin: llm.APIErrorOriginHTTPResponse, StatusCode: 503}, true},
		{"rate limit status", &llm.APIError{Origin: llm.APIErrorOriginHTTPResponse, StatusCode: 429}, true},
		{"connection reset", &net.OpError{Op: "read", Err: errStreamConnReset{}}, true},
		{"cancelled", context.Canceled, false},
		{"plain error", errors.New("boom"), false},
		{"client error status", &llm.APIError{Origin: llm.APIErrorOriginHTTPResponse, StatusCode: 400}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientSubAgentTransportError(tt.err); got != tt.want {
				t.Fatalf("isTransientSubAgentTransportError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestInterruptedRequestRecoveryInstructionKeepsPartialWork guards the split:
// when the interrupted request had already streamed body text, the sub-agent
// must be told to pick that reply back up. Sending it the bounded wrap-up
// instruction there would make it abandon a reply that was just preserved.
func TestInterruptedRequestRecoveryInstructionKeepsPartialWork(t *testing.T) {
	empty := &SubAgent{turn: &Turn{}}
	wrapUp := empty.interruptedRequestRecoveryInstruction()
	if !strings.Contains(wrapUp, "finish coordination now") {
		t.Fatalf("no-partial instruction = %q, want the bounded wrap-up wording", wrapUp)
	}

	sub := &SubAgent{turn: &Turn{}}
	sub.turn.appendPartialText("the failing assertion is in the second fixture")
	resume := sub.interruptedRequestRecoveryInstruction()
	if !strings.Contains(resume, "Continue that reply") || !strings.Contains(resume, "do not repeat text") {
		t.Fatalf("partial instruction = %q, want the resume-from-break wording", resume)
	}
	if sub.turn.peekPartialText() == "" {
		t.Fatal("choosing the instruction must not drain the partial text")
	}
	if resume == wrapUp {
		t.Fatal("partial and no-partial instructions must differ")
	}
}

type errStreamConnReset struct{}

func (errStreamConnReset) Error() string { return "connection reset by peer" }
