package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type queueTestBackend struct {
	mockBackend
	snapshot  *agent.InputQueueSnapshot
	updates   []agent.UpdateInput
	submitted []agent.SubmitInput
}

func (b *queueTestBackend) InputDeliveryTarget() agent.InputTarget        { return b.snapshot.Target }
func (b *queueTestBackend) InputQueueSnapshot() *agent.InputQueueSnapshot { return b.snapshot.Clone() }
func (b *queueTestBackend) SubmitUserInput(input agent.SubmitInput) bool {
	b.submitted = append(b.submitted, input)
	return true
}
func (b *queueTestBackend) UpdateQueuedInput(input agent.UpdateInput) bool {
	b.updates = append(b.updates, input)
	return true
}
func (*queueTestBackend) DeleteQueuedInput(agent.DeleteInput) bool  { return true }
func (*queueTestBackend) ControlInputQueue(agent.QueueControl) bool { return true }
func TestHeadlessInputDeliveryProjectionRedactsAttachments(t *testing.T) {
	snapshot := &agent.InputQueueSnapshot{State: &recovery.InputDeliveryState{Inputs: []recovery.PendingInput{{InputID: "input", Content: strings.Repeat("界", 200), Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: []byte("private binary"), ImagePath: "/private/attachment.png"}}}}}}
	projection := inputQueueProjection(snapshot)
	data, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private") || len([]rune(projection.Inputs[0].Preview)) != 160 || projection.Inputs[0].Attachments != 1 {
		t.Fatalf("unsafe projection: %s", data)
	}
}
func TestHeadlessInputDeliveryUpdateOmittedBodyPreservesAttachment(t *testing.T) {
	target := agent.InputTarget{RuntimeID: "runtime", SessionID: "session", SessionEpoch: 1, OwnerTaskID: "main"}
	backend := &queueTestBackend{snapshot: &agent.InputQueueSnapshot{Target: target, State: &recovery.InputDeliveryState{Revision: 1, Inputs: []recovery.PendingInput{{InputID: "input", Revision: 1, Content: "sample", Parts: []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "/session/attachment.png"}}}}}}}
	out := newTestOut(t)
	var cmd headlessCommand
	if err := json.Unmarshal([]byte(`{"type":"input_queue","action":"update","operation_id":"op","input_id":"input","expected_revision":1,"delivery_policy":"after_completion"}`), &cmd); err != nil {
		t.Fatal(err)
	}
	cmd.InputTarget = &target
	handleHeadlessInputQueue(cmd, backend, out.writer())
	if len(backend.updates) != 1 || backend.updates[0].Content != nil {
		t.Fatal("omitted content replaced attachments")
	}
	cmd.ContentPresent = true
	cmd.Content = "replacement"
	handleHeadlessInputQueue(cmd, backend, out.writer())
	if len(backend.updates) != 1 {
		t.Fatal("remote attachment body edit was admitted")
	}
	replies := out.drain()
	if len(replies) != 1 || replies[0].Type != "input_queue_result" {
		t.Fatalf("missing rejection: %+v", replies)
	}
}
func TestHeadlessInputDeliveryDoesNotDismissCurrentInteraction(t *testing.T) {
	backend := &queueTestBackend{snapshot: &agent.InputQueueSnapshot{Target: agent.InputTarget{RuntimeID: "runtime", SessionID: "session", OwnerTaskID: "main"}}}
	state := &headlessState{pendingConfirm: &headlessConfirmPayload{RequestID: "confirm", ToolName: "sample"}}
	out := newTestOut(t)
	handleHeadlessCommand(headlessCommand{Type: "send", RequestID: "request", Content: "sample", DeliveryPolicy: recovery.InputDeliveryAfterCompletion}, backend, state, out.writer())
	if len(backend.submitted) != 1 || state.pendingConfirm == nil || len(backend.confirmCalls) != 0 {
		t.Fatal("after-completion input disturbed current confirmation")
	}
}
