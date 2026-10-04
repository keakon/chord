package main

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func (m *mockBackend) SendUserMessageWithReceipt(content, _ string) bool {
	m.SendUserMessage(content)
	return true
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
			state := &headlessState{pendingQuestion: &headlessQuestionPayload{RequestID: "question-1"}}
			out := newTestOut()
			handleHeadlessCommand(headlessCommand{Type: "send", Content: content, RequestID: "input-1"}, backend, state, out.writer())
			env := findHeadlessEnvelopeValue(out.drain(), "input_result")
			if env == nil || env.Seq == 0 || env.Payload.(map[string]any)["status"] != agent.InputRejected {
				t.Fatalf("missing rejection: %#v", env)
			}
			if len(backend.supersededQuestions) != 0 || state.pendingQuestion == nil {
				t.Fatal("rejected input superseded a pending question")
			}
		})
	}
}
