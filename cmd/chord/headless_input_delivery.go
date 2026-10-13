package main

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
)

type headlessInputQueueBackend interface {
	InputDeliveryTarget() agent.InputTarget
	InputQueueSnapshot() *agent.InputQueueSnapshot
	SubmitUserInput(agent.SubmitInput) bool
	UpdateQueuedInput(agent.UpdateInput) bool
	DeleteQueuedInput(agent.DeleteInput) bool
	ControlInputQueue(agent.QueueControl) bool
}

type headlessInputQueueItem struct {
	InputID     string                       `json:"input_id"`
	Revision    uint64                       `json:"revision"`
	Sequence    int64                        `json:"sequence"`
	Policy      recovery.InputDeliveryPolicy `json:"delivery_policy"`
	Stage       string                       `json:"stage"`
	WorkID      string                       `json:"work_id,omitempty"`
	WorkDir     string                       `json:"work_dir"`
	WorktreeID  string                       `json:"worktree_id,omitempty"`
	Preview     string                       `json:"preview"`
	Attachments int                          `json:"attachments"`
	EditHeld    bool                         `json:"edit_held"`
}

type headlessInputQueue struct {
	RestoredNextInputsBlocked bool `json:"restored_next_inputs_blocked,omitempty"`
	agent.InputTarget
	ViewVersion   uint64                   `json:"view_version"`
	QueueRevision uint64                   `json:"queue_revision"`
	Gate          recovery.FollowupGate    `json:"gate"`
	Works         []recovery.DeliveryWork  `json:"works"`
	Inputs        []headlessInputQueueItem `json:"inputs"`
	Blocked       string                   `json:"blocked,omitempty"`
}

func inputQueueProjection(snapshot *agent.InputQueueSnapshot) *headlessInputQueue {
	if snapshot == nil {
		return nil
	}
	out := &headlessInputQueue{RestoredNextInputsBlocked: snapshot.RestoredNextInputsBlocked, InputTarget: snapshot.Target, ViewVersion: snapshot.ViewVersion, Blocked: snapshot.Blocked, Inputs: []headlessInputQueueItem{}}
	if snapshot.State == nil {
		out.Gate.Mode = recovery.FollowupGateRunning
		return out
	}
	state := snapshot.State
	out.QueueRevision, out.Gate = state.Revision, state.Gate
	out.Works = append([]recovery.DeliveryWork(nil), state.Works...)
	holds := make(map[string]bool)
	for _, id := range state.EditHolds {
		holds[id] = true
	}
	for _, input := range state.Inputs {
		preview := []rune(input.Content)
		if len(preview) > 160 {
			preview = preview[:160]
		}
		attachments := 0
		for _, part := range input.Parts {
			if part.IsBinary() {
				attachments++
			}
		}
		out.Inputs = append(out.Inputs, headlessInputQueueItem{InputID: input.InputID, Revision: input.Revision, Sequence: input.Sequence, Policy: input.DeliveryPolicy, Stage: input.Stage, WorkID: input.WorkID, WorkDir: input.WorkDir, WorktreeID: input.WorktreeID, Preview: string(preview), Attachments: attachments, EditHeld: holds[input.InputID]})
	}
	return out
}
func headlessInputQueueSnapshot(backend headlessBackend) *headlessInputQueue {
	if queue, ok := backend.(headlessInputQueueBackend); ok {
		return inputQueueProjection(queue.InputQueueSnapshot())
	}
	return nil
}

// Keep ordinary send content a string while distinguishing an omitted update
// body from an explicitly empty replacement.
func (cmd *headlessCommand) UnmarshalJSON(data []byte) error {
	type command headlessCommand
	var decoded command
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, decoded.ContentPresent = fields["content"]
	*cmd = headlessCommand(decoded)
	return nil
}

func handleHeadlessInputQueue(cmd headlessCommand, backend headlessBackend, out *stdoutWriter) {
	queue, ok := backend.(headlessInputQueueBackend)
	reject := func(reason string) {
		result := agent.InputQueueOperationResultEvent{OperationID: cmd.OperationID, InputID: cmd.InputID, Result: agent.InputQueueRejected, Reason: reason}
		if ok {
			result.Target = queue.InputDeliveryTarget()
			if snapshot := queue.InputQueueSnapshot(); snapshot != nil && snapshot.State != nil {
				result.QueueRevision = snapshot.State.Revision
				for _, input := range snapshot.State.Inputs {
					if input.InputID == cmd.InputID {
						result.ItemRevision = input.Revision
					}
				}
			}
		}
		out.emit(headlessEnvelope{Type: "input_queue_result", Payload: result})
	}
	if !ok {
		reject("Input queue is unavailable")
		return
	}
	if cmd.OperationID == "" || cmd.InputTarget == nil {
		reject("operation_id and target are required")
		return
	}
	target := queue.InputDeliveryTarget()
	if *cmd.InputTarget != target || flagGatewayInstance != "" && cmd.BindingID != flagGatewayInstance {
		reject("Input queue binding changed; refresh its state")
		return
	}
	snapshot := queue.InputQueueSnapshot()
	var input *recovery.PendingInput
	if snapshot != nil && snapshot.State != nil {
		for i := range snapshot.State.Inputs {
			if snapshot.State.Inputs[i].InputID == cmd.InputID {
				input = &snapshot.State.Inputs[i]
				break
			}
		}
	}
	admitted := false
	switch cmd.Action {
	case "get":
		if input == nil {
			reject("Input no longer exists; refresh the queue")
			return
		}
		attachments := 0
		for _, part := range input.Parts {
			if part.IsBinary() {
				attachments++
			}
		}
		out.emit(headlessEnvelope{Type: "input_queue_result", Payload: map[string]any{"operation_id": cmd.OperationID, "target": target, "result": agent.InputQueueCommitted, "input_id": input.InputID, "item_revision": input.Revision, "queue_revision": snapshot.State.Revision, "content": input.Content, "attachments": attachments}})
		return
	case "update":
		if cmd.ExpectedRevision == nil {
			reject("expected_revision is required")
			return
		}
		var content *string
		if cmd.ContentPresent {
			if input == nil {
				reject("Input no longer exists; refresh the queue")
				return
			}
			for _, part := range input.Parts {
				if part.IsBinary() {
					reject("Use the local TUI to edit input with attachments")
					return
				}
			}
			content = new(cmd.Content)
		}
		admitted = queue.UpdateQueuedInput(agent.UpdateInput{OperationID: cmd.OperationID, Target: target, InputID: cmd.InputID, ExpectedRevision: *cmd.ExpectedRevision, DeliveryPolicy: cmd.DeliveryPolicy, Content: content})
	case "delete":
		if cmd.ExpectedRevision == nil {
			reject("expected_revision is required")
			return
		}
		admitted = queue.DeleteQueuedInput(agent.DeleteInput{OperationID: cmd.OperationID, Target: target, InputID: cmd.InputID, ExpectedRevision: *cmd.ExpectedRevision})
	case agent.InputQueuePause, agent.InputQueueResume, agent.InputQueueClear:
		if cmd.ExpectedQueueRevision == nil {
			reject("expected_queue_revision is required")
			return
		}
		admitted = queue.ControlInputQueue(agent.QueueControl{OperationID: cmd.OperationID, Target: target, Action: cmd.Action, ExpectedQueueRevision: *cmd.ExpectedQueueRevision})
	case agent.InputQueueHoldEdit, agent.InputQueueReleaseEdit:
		if cmd.ExpectedRevision == nil {
			reject("expected_revision is required")
			return
		}
		admitted = queue.ControlInputQueue(agent.QueueControl{OperationID: cmd.OperationID, Target: target, Action: cmd.Action, InputID: cmd.InputID, ExpectedRevision: *cmd.ExpectedRevision})
	default:
		reject("Unknown input_queue action")
		return
	}
	if !admitted {
		reject("Agent is shutting down")
	}
}

func headlessSendWithPolicy(cmd headlessCommand, backend headlessBackend, state *headlessState, out *stdoutWriter) bool {
	if cmd.DeliveryPolicy == "" || cmd.DeliveryPolicy == recovery.InputDeliveryNextRequest {
		return false
	}
	if cmd.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
		emitHeadlessInputRejected(cmd.RequestID, "Invalid delivery_policy", state, out)
		return true
	}
	if cmd.Agent != "" && cmd.Agent != "main" {
		emitHeadlessInputRejected(cmd.RequestID, "after_completion is only supported by MainAgent", state, out)
		return true
	}
	queue, ok := backend.(headlessInputQueueBackend)
	if !ok {
		emitHeadlessInputRejected(cmd.RequestID, "After-completion delivery is unavailable", state, out)
		return true
	}
	if strings.TrimSpace(cmd.Content) == "" {
		emitHeadlessInputRejected(cmd.RequestID, "Empty input", state, out)
		return true
	}
	if cmd.RequestID == "" && cmd.OperationID == "" {
		emitHeadlessInputRejected(cmd.RequestID, "request_id is required for after_completion", state, out)
		return true
	}
	if !queue.SubmitUserInput(agent.SubmitInput{OperationID: cmd.OperationID, RequestID: cmd.RequestID, Target: queue.InputDeliveryTarget(), DeliveryPolicy: cmd.DeliveryPolicy, Content: cmd.Content}) {
		emitHeadlessInputRejected(cmd.RequestID, "Agent is shutting down", state, out)
	}
	return true
}
