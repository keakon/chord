package agent

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func (a *MainAgent) handleDeliveryOperation(op *deliveryOperation) {
	if op.submit != nil {
		a.markRawUserMessageDispatched(op.acceptedOrder)
	}
	id, target := op.identity()
	if id == "" && (op.submit == nil || op.submit.RequestID == "") {
		a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("operation_id is required"))
		return
	}
	if target != a.InputDeliveryTarget() {
		a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("input target changed; refresh the input queue"))
		return
	}
	if a.sessionExecutionPaused() && a.pause.restored && op.submit != nil && (op.submit.DeliveryPolicy == "" || op.submit.DeliveryPolicy == recovery.InputDeliveryNextRequest) {
		reservedBytes := deliveryIntentTextBytes(op)
		duplicate := false
		for _, pending := range a.delivery.resumeOperations {
			reservedBytes += deliveryIntentTextBytes(pending)
			otherID, _ := pending.identity()
			if id != "" && otherID == id {
				duplicate = true
			}
		}
		if duplicate {
			a.rejectDeliveryOperation(op, InputQueueConflict, fmt.Errorf("operation_id is already pending"))
			return
		}
		if reservedBytes > recovery.InputDeliveryMaxTextBytes || len(a.delivery.resumeOperations) >= recovery.InputDeliveryMaxInputs {
			a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("too many inputs waiting for session resume"))
			return
		}
		a.delivery.resumeOperations = append(a.delivery.resumeOperations, op)
		a.resumePausedSessionForUser()
		return
	}
	if a.sessionExecutionPaused() && !a.pause.restored && !op.admittedBeforePause || a.shuttingDown.Load() || a.delivery.navigation {
		a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("session is paused or shutting down"))
		return
	}
	matches := func(other *deliveryOperation) bool { otherID, _ := other.identity(); return id != "" && id == otherID }
	if a.delivery.inflight != nil && matches(a.delivery.inflight) {
		a.rejectDeliveryOperation(op, InputQueueConflict, fmt.Errorf("operation_id is already in flight"))
		return
	}
	if slices.ContainsFunc(a.delivery.pending, matches) {
		a.rejectDeliveryOperation(op, InputQueueConflict, fmt.Errorf("operation_id is already pending"))
		return
	}
	if len(a.delivery.pending) >= 256 {
		a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("input queue is busy; refresh and try again"))
		return
	}
	if op.submit != nil {
		input := op.submit
		if input.Content == "" {
			for _, part := range input.Parts {
				if !part.IsBinary() {
					input.Content += part.Text
				}
			}
		}
		if input.DeliveryPolicy == "" {
			input.DeliveryPolicy = recovery.InputDeliveryNextRequest
		}
		if input.DeliveryPolicy != recovery.InputDeliveryNextRequest && input.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
			a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("invalid delivery policy %q", input.DeliveryPolicy))
			return
		}
		if strings.TrimSpace(input.Content) == "" && len(input.Parts) == 0 {
			a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("input is empty"))
			return
		}
		content := strings.TrimSpace(input.Content)
		if input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion && IsUnsupportedAfterCompletionInput(content) {
			a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("this command cannot run after completion; the draft was preserved"))
			return
		}
		if input.DeliveryPolicy == recovery.InputDeliveryNextRequest && !a.sessionExecutionPaused() && a.turn == nil && !a.mcpTransitionActive.Load() && !a.pendingUserDrainSuspended && a.delivery.inflight == nil && len(a.delivery.pending) == 0 && a.delivery.persistenceError == nil && !a.deliveryHasRunnableQueue() {
			if op.inputID == "" {
				op.inputID = rand.Text()
			}
			a.handleUserMessage(Event{Type: EventUserMessage, Payload: acceptedUserMessage{InputID: op.inputID, RequestID: input.RequestID, AcceptedOrder: op.acceptedOrder, Content: input.Content, Parts: input.Parts}})
			for _, msg := range a.ctxMgr.Snapshot() {
				if msg.Role == message.RoleUser && msg.InputID == op.inputID {
					a.emitPendingDraftConsumed(input.DraftID, msg)
					break
				}
			}
			if input.OperationID != "" {
				a.emitToTUI(InputQueueOperationResultEvent{Target: input.Target, OperationID: input.OperationID, InputID: op.inputID, Result: InputQueueCommitted})
			}
			return
		}
		if input.DeliveryPolicy == recovery.InputDeliveryNextRequest && !op.userIntentApplied && !a.sessionExecutionPaused() {
			if a.questions.paused {
				a.handleQuestionCommand(&questionCommand{operation: QuestionOperation{Operation: questionOpResume, OperationID: makeRequestID()}, ctx: a.parentCtx})
			}
			a.interruptQuestionWaits()
			a.explicitUserTurnCount.Add(1)
			a.sweepSubAgentLifecycle()
			a.settleStalledTurn("user message")
			op.userIntentApplied = true
		}
		input.Content, input.Parts = a.expandSlashCommandForModel(input.Content, input.Parts)
		if op.inputID == "" {
			op.inputID = rand.Text()
		}
		if a.delivery.acceptedOrders == nil {
			a.delivery.acceptedOrders = make(map[string]int64)
		}
		a.delivery.acceptedOrders[op.inputID] = op.acceptedOrder
	}
	if op.update != nil {
		op.inputID = op.update.InputID
	}
	if op.delete != nil {
		op.inputID = op.delete.InputID
	}
	if op.control != nil {
		op.inputID = op.control.InputID
	}
	// Reserve input/text capacity for accepted intents as well as committed data.
	count, textBytes, pendingBytes := 0, 0, 0
	add := func(content string, bytes int) { count++; textBytes += len(content) + bytes }
	if state := a.delivery.committed; state != nil {
		for _, input := range state.Inputs {
			n := 0
			for _, part := range input.Parts {
				if !part.IsBinary() {
					n += len(part.Text)
				}
			}
			add(input.Content, n)
		}
	}
	intents := append(append([]*deliveryOperation(nil), a.delivery.pending...), op)
	if a.delivery.inflight != nil && !slices.Contains(intents, a.delivery.inflight) {
		intents = append(intents, a.delivery.inflight)
	}
	for _, pending := range intents {
		if pending.submit != nil {
			n := 0
			for _, part := range pending.submit.Parts {
				if !part.IsBinary() {
					n += len(part.Text)
				}
			}
			add(pending.submit.Content, n)
			pendingBytes += len(pending.submit.Content) + n
		}
		if pending.update != nil && pending.update.Content != nil {
			pendingBytes += len(*pending.update.Content)
			for _, part := range pending.update.Parts {
				if !part.IsBinary() {
					pendingBytes += len(part.Text)
				}
			}
		}
	}
	if (op.submit != nil || op.update != nil) && (pendingBytes > recovery.InputDeliveryMaxTextBytes || op.submit != nil && (count > recovery.InputDeliveryMaxInputs || textBytes > recovery.InputDeliveryMaxTextBytes)) {
		a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("input queue capacity exceeded"))
		return
	}
	if op.control != nil && a.delivery.persistenceError != nil && a.delivery.inflight == nil {
		a.delivery.owner = nil
	}
	if op.control != nil && op.control.Action == InputQueueResume {
		generation := a.delivery.cancelGeneration.Load()
		op.resumeAllowed = func() bool {
			return !a.sessionExecutionPaused() && a.delivery.cancelGeneration.Load() == generation
		}
	}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}

func (a *MainAgent) applyDeliveryOperation(op *deliveryOperation, state *recovery.InputDeliveryState) error {
	_, target := op.identity()
	if target != a.InputDeliveryTarget() {
		return fmt.Errorf("input target changed; refresh the input queue")
	}
	switch {
	case op.submit != nil:
		input := op.submit
		dir := a.WorkDirSnapshot()
		if dir.Generation != target.WorkDirGeneration {
			return fmt.Errorf("working directory changed; refresh the input target")
		}
		state.NextSequence++
		state.Inputs = append(state.Inputs, recovery.PendingInput{InputID: op.inputID, OwnerTaskID: identity.MainAgentID, Sequence: state.NextSequence, Revision: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: input.DeliveryPolicy, WorkDir: dir.Path, WorktreeID: dir.WorktreeID, DraftID: input.DraftID, RequestID: input.RequestID, Content: input.Content, Parts: cloneContentParts(input.Parts), FromUser: true})
		if state.Gate.Mode == recovery.FollowupGateRunning && a.delivery.currentWorkID != "" {
			state.Gate.WaitingWorkID = a.delivery.currentWorkID
			ensureDeliveryWork(state, a.delivery.currentWorkID, a.delivery.currentOrigin)
		}
		if a.cancelCoversAcceptedOrder(op.acceptedOrder) && state.Gate.Mode == recovery.FollowupGateRunning {
			state.Gate.Mode, state.Gate.Reason = recovery.FollowupGatePaused, recovery.FollowupPredecessorCancelled
		}
	case op.update != nil:
		update := op.update
		input, err := deliveryInputForEdit(state, update.InputID, update.ExpectedRevision)
		if err != nil {
			return err
		}
		if update.DeliveryPolicy != "" {
			if update.DeliveryPolicy != recovery.InputDeliveryNextRequest && update.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
				return fmt.Errorf("invalid delivery policy %q", update.DeliveryPolicy)
			}
			input.DeliveryPolicy = update.DeliveryPolicy
		}
		if update.Content != nil {
			input.Content, input.Parts = a.expandSlashCommandForModel(*update.Content, cloneContentParts(update.Parts))
			if strings.TrimSpace(input.Content) == "" && len(input.Parts) == 0 {
				return fmt.Errorf("input is empty")
			}
		}
		content := strings.TrimSpace(input.Content)
		if input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion && IsUnsupportedAfterCompletionInput(content) {
			return fmt.Errorf("this command cannot run after completion")
		}
		input.Revision++
		removeDeliveryHold(state, input.InputID)
	case op.delete != nil:
		input, err := deliveryInputForEdit(state, op.delete.InputID, op.delete.ExpectedRevision)
		if err != nil {
			return err
		}
		id := input.InputID
		state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool { return input.InputID == id })
		removeDeliveryHold(state, id)
	case op.control != nil:
		return a.applyDeliveryControl(op.control, state)
	}
	return nil
}

func (a *MainAgent) applyDeliveryControl(control *QueueControl, state *recovery.InputDeliveryState) error {
	switch control.Action {
	case InputQueueHoldEdit, InputQueueReleaseEdit:
		input, err := deliveryInputForEdit(state, control.InputID, control.ExpectedRevision)
		if err != nil {
			return err
		}
		held := slices.Contains(state.EditHolds, input.InputID)
		if control.Action == InputQueueHoldEdit {
			if held {
				return recovery.ErrInputDeliveryConflict
			}
			state.EditHolds = append(state.EditHolds, input.InputID)
			if state.Gate.Mode == recovery.FollowupGateRunning {
				state.Gate.Mode, state.Gate.Reason = recovery.FollowupGatePaused, recovery.FollowupEditHold
			}
		} else {
			if !held {
				return recovery.ErrInputDeliveryConflict
			}
			removeDeliveryHold(state, input.InputID)
		}
		input.Revision++
	case InputQueuePause, InputQueueResume, InputQueueClear:
		if control.ExpectedQueueRevision != state.Revision {
			return recovery.ErrInputDeliveryConflict
		}
		switch control.Action {
		case InputQueuePause:
			state.Gate.Mode, state.Gate.Reason = recovery.FollowupGatePaused, recovery.FollowupUserPaused
		case InputQueueClear:
			state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool {
				if input.Stage == recovery.PendingInputStageQueued && input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion {
					removeDeliveryHold(state, input.InputID)
					return true
				}
				return false
			})
		case InputQueueResume:
			if a.turn != nil || a.mcpTransitionActive.Load() || a.questionWaiting.Load() || len(state.EditHolds) > 0 {
				return fmt.Errorf("wait for current work and finish editing before resuming the queue")
			}
			dir := a.WorkDirSnapshot()
			for _, input := range state.Inputs {
				if input.Stage != recovery.PendingInputStageQueued {
					continue
				}
				if input.DeliveryPolicy == recovery.InputDeliveryNextRequest {
					return fmt.Errorf("ordinary inputs must be processed first")
				}
				if input.WorkDir != dir.Path || input.WorktreeID != dir.WorktreeID {
					return fmt.Errorf("return to the queued input's working directory before resuming")
				}
			}
			// Explicit resume skips already-consumed work; its transcript and
			// unknown outcome are not rewritten as successful completion.
			state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool {
				return input.Stage != recovery.PendingInputStageQueued
			})
			state.Gate = recovery.FollowupGate{Mode: recovery.FollowupGateRunning}
		}
	default:
		return fmt.Errorf("invalid input queue action %q", control.Action)
	}
	return nil
}

func removeDeliveryHold(state *recovery.InputDeliveryState, id string) {
	state.EditHolds = slices.DeleteFunc(state.EditHolds, func(held string) bool { return held == id })
}
func ensureDeliveryWork(state *recovery.InputDeliveryState, id, origin string) {
	for _, work := range state.Works {
		if work.WorkID == id {
			return
		}
	}
	state.Works = append(state.Works, recovery.DeliveryWork{WorkID: id, Origin: origin, Outcome: recovery.DeliveryWorkActive})
}

func (a *MainAgent) deliveryHasRunnableQueue() bool {
	state := a.delivery.committed
	if state == nil {
		return false
	}
	for _, input := range state.Inputs {
		if input.Stage == recovery.PendingInputStageQueued && (input.DeliveryPolicy == recovery.InputDeliveryNextRequest || state.Gate.Mode == recovery.FollowupGateRunning) {
			return true
		}
	}
	return false
}

func deliveryIntentTextBytes(op *deliveryOperation) int {
	if op.submit == nil {
		return 0
	}
	size := len(op.submit.Content)
	for _, part := range op.submit.Parts {
		if !part.IsBinary() {
			size += len(part.Text)
		}
	}
	return size
}
