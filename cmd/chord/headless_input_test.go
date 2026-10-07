package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func (m *mockBackend) SendUserMessageWithReceipt(content, _ string) bool {
	m.SendUserMessage(content)
	return true
}

func TestHeadlessInputRejectedDrainsAfterApplicationCancellation(t *testing.T) {
	for _, content := range []string{" ", "/export", "new request"} {
		t.Run(content, func(t *testing.T) {
			var buf bytes.Buffer
			out := newStdoutWriter(t.Context(), &buf)
			ctx, cancel := context.WithCancel(t.Context())
			out.commandCtx = ctx
			cancel()
			backend := &rejectingInputBackend{}
			state := &headlessState{subscriptions: map[string]bool{}, pendingConfirm: &headlessConfirmPayload{RequestID: "confirm-1", ToolName: "sample_tool"}}
			handleHeadlessCommand(headlessCommand{Type: "send", Content: content, RequestID: "input-1"}, backend, state, out)
			go out.run()
			if !out.closeUntil(time.After(time.Second)) {
				t.Fatal("rejection receipt did not drain")
			}
			envs := decodeHeadlessJSONLines(t, buf.Bytes())
			var replies int
			for _, env := range envs {
				if env.Type == "input_result" {
					replies++
					payload := env.Payload.(map[string]any)
					if env.Seq == 0 || payload["request_id"] != "input-1" || payload["status"] != agent.InputRejected {
						t.Fatalf("unexpected rejection receipt: %#v", env)
					}
				}
			}
			if replies != 1 {
				t.Fatalf("rejection receipts = %d, want 1", replies)
			}
			if state.pendingConfirm == nil || len(backend.confirmCalls) != 0 || len(backend.sentMessages) != 0 {
				t.Fatal("rejected input changed agent work or pending confirmation")
			}
		})
	}
}

func TestHeadlessInputRejectedSharesShutdownDeadline(t *testing.T) {
	for _, blocked := range []string{"ordering", "queue"} {
		t.Run(blocked, func(t *testing.T) {
			out := newStdoutWriter(t.Context(), &bytes.Buffer{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out.commandCtx = ctx
			cancel()
			deadline := time.Now().Add(25 * time.Millisecond)
			out.shutdownOnce.Do(func() { out.shutdownDeadline = deadline })
			if blocked == "ordering" {
				out.ordering <- struct{}{}
			} else {
				for range cap(out.ch) {
					out.ch <- headlessEnvelope{Type: "status"}
				}
			}
			done := make(chan struct{})
			go func() {
				handleHeadlessCommand(headlessCommand{Type: "send", Content: "sample", RequestID: "input-1"}, &rejectingInputBackend{}, &headlessState{}, out)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("rejection reply ignored the shared shutdown deadline")
			}
			if !out.shutdownOutputDeadline().Equal(deadline) {
				t.Fatal("reply restarted the shutdown budget")
			}
			if blocked == "ordering" {
				<-out.ordering
				if len(out.ch) != 0 {
					t.Fatal("timed-out reply entered the output queue")
				}
			} else if len(out.ch) != cap(out.ch) {
				t.Fatal("timed-out reply changed a saturated queue")
			}
			go out.run()
			if !out.closeUntil(time.After(time.Second)) {
				t.Fatal("output did not close after releasing backpressure")
			}
		})
	}
}

func (headlessSendOnlyBackend) SendUserMessageWithReceipt(string, string) bool { return true }

type rejectingInputBackend struct{ mockBackend }

func (*rejectingInputBackend) SendUserMessageWithReceipt(string, string) bool { return false }

func TestHeadlessInputReplyIgnoresSubscription(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{}}
	for _, status := range []string{agent.InputHandled, agent.InputQueued, agent.InputStarted, agent.InputRejected} {
		envs := filterHeadlessEvent(agent.InputResultEvent{RequestID: "input-1", Status: status}, state)
		if len(envs) != 1 || envs[0].Type != "input_result" || envs[0].Seq == 0 {
			t.Fatalf("reply for %s: %#v", status, envs)
		}
		if status == agent.InputStarted && !state.busy {
			t.Fatal("started receipt did not update status cache")
		}
	}
}

func TestHeadlessInputRejectedBeforeInteractions(t *testing.T) {
	for _, content := range []string{" ", "/export", "new request"} {
		t.Run(content, func(t *testing.T) {
			backend := &rejectingInputBackend{}
			state := &headlessState{questions: map[string]*headlessQuestionPayload{"question-1": {RequestID: "question-1"}}}
			out := newTestOut()
			handleHeadlessCommand(headlessCommand{Type: "send", Content: content, RequestID: "input-1"}, backend, state, out.writer())
			env := findHeadlessEnvelopeValue(out.drain(), "input_result")
			if env == nil || env.Seq == 0 || env.Payload.(map[string]any)["status"] != agent.InputRejected {
				t.Fatalf("missing rejection: %#v", env)
			}
			if testPendingQuestion(state) == nil {
				t.Fatal("rejected input superseded a pending question")
			}
		})
	}
}
