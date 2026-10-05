package main

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestHeadlessInputReplyCancellationDuringBackpressure(t *testing.T) {
	out := newStdoutWriter(t.Context(), io.Discard)
	commandCtx, cancelCommand := context.WithCancel(t.Context())
	defer cancelCommand()
	out.commandCtx = commandCtx
	replyCtx, cancelReply := out.inputReplyContext()
	defer cancelReply()
	if _, bounded := replyCtx.Deadline(); bounded {
		t.Fatal("active reply must preserve ordinary output backpressure")
	}
	deadline := time.Now().Add(25 * time.Millisecond)
	out.shutdownOnce.Do(func() { out.shutdownDeadline = deadline })
	for range cap(out.ch) {
		out.ch <- headlessEnvelope{Type: "status"}
	}
	result := make(chan bool, 1)
	go func() { result <- out.emitWithContext(replyCtx, headlessEnvelope{Type: "input_result"}) }()
	cancelCommand()
	select {
	case accepted := <-result:
		if accepted || time.Now().Before(deadline) {
			t.Fatal("reply did not wait until the shared shutdown deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not bound an in-flight reply")
	}
	go out.run()
	if !out.closeUntil(time.After(time.Second)) {
		t.Fatal("accepted output did not drain")
	}
}
