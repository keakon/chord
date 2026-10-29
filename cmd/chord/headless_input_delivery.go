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
	WaitWorkID  string                       `json:"wait_work_id,omitempty"`
	WorkDir     string                       `json:"work_dir"`
	WorktreeID  string                       `json:"worktree_id,omitempty"`
	Preview     string                       `json:"preview"`
	Attachments int                          `json:"attachments"`
	EditHeld    bool                         `json:"edit_held"`
}

type headlessInputQueue struct {
	agent.InputTarget
	ViewVersion    uint64                   `json:"view_version"`
	ViewVersionCAS bool                     `json:"view_version_cas"`
	QueueRevision  uint64                   `json:"queue_revision"`
	Works          []recovery.DeliveryWork  `json:"works"`
	Inputs         []headlessInputQueueItem `json:"inputs"`
	Blocked        string                   `json:"blocked,omitempty"`
	RestoredHeld   bool                     `json:"restored_held,omitempty"`
}

func inputQueueProjection(snapshot *agent.InputQueueSnapshot) *headlessInputQueue {
	if snapshot == nil {
		return nil
	}
	out := &headlessInputQueue{InputTarget: snapshot.Target, ViewVersion: snapshot.ViewVersion, ViewVersionCAS: true, Blocked: snapshot.Blocked, RestoredHeld: snapshot.RestoredHeld, Inputs: []headlessInputQueueItem{}}
	if snapshot.State == nil {
		return out
	}
	state := snapshot.State
	out.QueueRevision = state.Revision
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
		out.Inputs = append(out.Inputs, headlessInputQueueItem{InputID: input.InputID, Revision: input.Revision, Sequence: input.Sequence, Policy: input.DeliveryPolicy, Stage: input.Stage, WorkID: input.WorkID, WorkDir: input.WorkDir, WorktreeID: input.WorktreeID, Preview: string(preview), Attachments: attachments, WaitWorkID: input.WaitWorkID, EditHeld: holds[input.InputID]})
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
	var expectedView uint64
	if cmd.ExpectedViewVersion != nil {
		expectedView = *cmd.ExpectedViewVersion
		if snapshot == nil || expectedView == 0 || expectedView != snapshot.ViewVersion {
			out.emit(headlessEnvelope{Type: "input_queue_result", Payload: agent.InputQueueOperationResultEvent{OperationID: cmd.OperationID, Target: target, Result: agent.InputQueueConflict, Reason: "Input queue view changed; refresh its state"}})
			return
		}
	}
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
		admitted = queue.UpdateQueuedInput(agent.UpdateInput{ExpectedViewVersion: expectedView, OperationID: cmd.OperationID, Target: target, InputID: cmd.InputID, ExpectedRevision: *cmd.ExpectedRevision, DeliveryPolicy: cmd.DeliveryPolicy, Content: content})
	case "delete":
		if cmd.ExpectedRevision == nil {
			reject("expected_revision is required")
			return
		}
		admitted = queue.DeleteQueuedInput(agent.DeleteInput{ExpectedViewVersion: expectedView, OperationID: cmd.OperationID, Target: target, InputID: cmd.InputID, ExpectedRevision: *cmd.ExpectedRevision})
	case agent.InputQueueClear:
		if cmd.ExpectedQueueRevision == nil {
			reject("expected_queue_revision is required")
			return
		}
		admitted = queue.ControlInputQueue(agent.QueueControl{ExpectedViewVersion: expectedView, OperationID: cmd.OperationID, Target: target, Action: cmd.Action, ExpectedQueueRevision: *cmd.ExpectedQueueRevision})
	case agent.InputQueueHoldEdit, agent.InputQueueReleaseEdit:
		if cmd.ExpectedRevision == nil {
			reject("expected_revision is required")
			return
		}
		admitted = queue.ControlInputQueue(agent.QueueControl{ExpectedViewVersion: expectedView, OperationID: cmd.OperationID, Target: target, Action: cmd.Action, InputID: cmd.InputID, ExpectedRevision: *cmd.ExpectedRevision})
	default:
		reject("Unknown input_queue action")
		return
	}
	if !admitted {
		reject("Agent is shutting down")
	}
}

func headlessSendWithPolicy(cmd headlessCommand, backend headlessBackend, out *stdoutWriter) bool {
	if cmd.DeliveryPolicy == "" || cmd.DeliveryPolicy == recovery.InputDeliveryNextRequest {
		return false
	}
	queue, ok := backend.(headlessInputQueueBackend)
	reject := func(reason string) {
		result := agent.InputQueueOperationResultEvent{OperationID: cmd.OperationID, Result: agent.InputQueueRejected, Reason: reason}
		if ok {
			result.Target = queue.InputDeliveryTarget()
		}
		out.emit(headlessEnvelope{Type: "input_queue_result", Payload: result})
	}
	if cmd.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
		reject("Invalid delivery_policy")
		return true
	}
	if cmd.Agent != "" && cmd.Agent != "main" {
		reject("after_completion is only supported by MainAgent")
		return true
	}
	if !ok {
		reject("After-completion delivery is unavailable")
		return true
	}
	if cmd.OperationID == "" || cmd.InputTarget == nil {
		reject("operation_id and target are required for after_completion")
		return true
	}
	target := queue.InputDeliveryTarget()
	if *cmd.InputTarget != target || flagGatewayInstance != "" && cmd.BindingID != flagGatewayInstance {
		reject("Input queue binding changed; refresh its state")
		return true
	}
	snapshot := queue.InputQueueSnapshot()
	if strings.TrimSpace(cmd.Content) == "" {
		reject("Empty input")
		return true
	}
	var expectedView uint64
	if cmd.ExpectedViewVersion != nil {
		expectedView = *cmd.ExpectedViewVersion
		if snapshot == nil || expectedView == 0 || expectedView != snapshot.ViewVersion {
			out.emit(headlessEnvelope{Type: "input_queue_result", Payload: agent.InputQueueOperationResultEvent{OperationID: cmd.OperationID, Target: target, Result: agent.InputQueueConflict, Reason: "Input queue view changed; refresh its state"}})
			return true
		}
	}
	if !queue.SubmitUserInput(agent.SubmitInput{ExpectedViewVersion: expectedView, OperationID: cmd.OperationID, RequestID: cmd.RequestID, Target: target, DeliveryPolicy: cmd.DeliveryPolicy, Content: cmd.Content}) {
		reject("Agent is shutting down")
	}
	return true
}
