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
	handleHeadlessCommand(headlessCommand{Type: "send", OperationID: "operation", InputTarget: new(backend.snapshot.Target), RequestID: "request", Content: "sample", DeliveryPolicy: recovery.InputDeliveryAfterCompletion}, backend, state, out.writer())
	if len(backend.submitted) != 1 || state.pendingConfirm == nil || len(backend.confirmCalls) != 0 {
		t.Fatal("after-completion input disturbed current confirmation")
	}
}

func TestHeadlessAfterCompletionRejectsChangedTarget(t *testing.T) {
	target := agent.InputTarget{RuntimeID: "runtime", SessionID: "session", SessionEpoch: 1, OwnerTaskID: "main", WorkDirGeneration: 1}
	for _, changed := range []*agent.InputTarget{
		nil,
		new(agent.InputTarget{RuntimeID: "other", SessionID: "session", SessionEpoch: 1, OwnerTaskID: "main", WorkDirGeneration: 1}),
		new(agent.InputTarget{RuntimeID: "runtime", SessionID: "other", SessionEpoch: 1, OwnerTaskID: "main", WorkDirGeneration: 1}),
		new(agent.InputTarget{RuntimeID: "runtime", SessionID: "session", SessionEpoch: 2, OwnerTaskID: "main", WorkDirGeneration: 1}),
		new(agent.InputTarget{RuntimeID: "runtime", SessionID: "session", SessionEpoch: 1, OwnerTaskID: "main", WorkDirGeneration: 2}),
	} {
		backend := &queueTestBackend{snapshot: &agent.InputQueueSnapshot{Target: target}}
		out := newTestOut(t)
		handleHeadlessCommand(headlessCommand{Type: "send", OperationID: "operation", RequestID: "request", InputTarget: changed, Content: "sample", DeliveryPolicy: recovery.InputDeliveryAfterCompletion}, backend, &headlessState{}, out.writer())
		if len(backend.submitted) != 0 {
			t.Fatalf("changed target admitted: %+v", changed)
		}
		replies := out.drain()
		if len(replies) != 1 || replies[0].Type != "input_queue_result" {
			t.Fatalf("missing correlated rejection: %+v", replies)
		}
		payload, err := json.Marshal(replies[0].Payload)
		if err != nil {
			t.Fatal(err)
		}
		var result agent.InputQueueOperationResultEvent
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		if result.OperationID != "operation" || result.Result != agent.InputQueueRejected {
			t.Fatalf("rejection=%+v", result)
		}
	}
}

func TestHeadlessInputQueueRejectsStaleViewBeforeAdmission(t *testing.T) {
	target := agent.InputTarget{RuntimeID: "runtime", SessionID: "session", SessionEpoch: 1, OwnerTaskID: "main"}
	snapshot := &agent.InputQueueSnapshot{Target: target, ViewVersion: 7, State: &recovery.InputDeliveryState{
		Revision: 3,
		Inputs:   []recovery.PendingInput{{InputID: "input", Revision: 2, Sequence: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryAfterCompletion}},
	}}
	backend := &queueTestBackend{snapshot: snapshot}
	out := newTestOut(t)
	expected := uint64(6)
	revision := uint64(2)
	handleHeadlessInputQueue(headlessCommand{
		OperationID:         "stale",
		InputTarget:         &target,
		Action:              "delete",
		InputID:             "input",
		ExpectedRevision:    &revision,
		ExpectedViewVersion: &expected,
	}, backend, out.writer())
	if len(backend.updates) != 0 || len(backend.submitted) != 0 {
		t.Fatal("stale queue view was admitted")
	}
	replies := out.drain()
	if len(replies) != 1 || replies[0].Type != "input_queue_result" {
		t.Fatalf("replies = %+v", replies)
	}
	payload, err := json.Marshal(replies[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var result agent.InputQueueOperationResultEvent
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "stale" || result.Result != agent.InputQueueConflict {
		t.Fatalf("stale reply = %+v", result)
	}
}

func TestHeadlessAfterCompletionRejectsStaleView(t *testing.T) {
	target := agent.InputTarget{RuntimeID: "runtime", SessionID: "session", SessionEpoch: 1, OwnerTaskID: "main"}
	backend := &queueTestBackend{snapshot: &agent.InputQueueSnapshot{Target: target, ViewVersion: 7}}
	out := newTestOut(t)
	expected := uint64(6)
	handleHeadlessCommand(headlessCommand{
		Type: "send", OperationID: "stale-after", RequestID: "request",
		InputTarget: &target, Content: "sample", DeliveryPolicy: recovery.InputDeliveryAfterCompletion,
		ExpectedViewVersion: &expected,
	}, backend, &headlessState{}, out.writer())
	if len(backend.submitted) != 0 {
		t.Fatal("stale after-completion submit was admitted")
	}
	replies := out.drain()
	if len(replies) != 1 || replies[0].Type != "input_queue_result" {
		t.Fatalf("replies = %+v", replies)
	}
	payload, err := json.Marshal(replies[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var result agent.InputQueueOperationResultEvent
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "stale-after" || result.Result != agent.InputQueueConflict {
		t.Fatalf("stale reply = %+v", result)
	}
}
