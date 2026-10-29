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
	if expectedView := deliveryOperationExpectedView(op); expectedView != 0 && expectedView != a.delivery.viewVersion {
		a.rejectDeliveryOperation(op, InputQueueConflict, fmt.Errorf("input queue view changed: %w", recovery.ErrInputDeliveryConflict))
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
		// A cancel-covered message must never start work directly: it rides the
		// durable queue so the covered-cancel retirement keeps its receipt and
		// transcript presentation.
		if a.immediateDispatchEligible(op, input) {
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
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}

// immediateDispatchEligible reports whether an ordinary submit can run right
// now as a direct user message instead of a durable queue row. Both the
// submit path and the absent-queue re-dispatch must agree on this: a submit
// that is not eligible has to stay durable, or the absent-queue re-dispatch
// would loop forever between the queue and the composer.
func (a *MainAgent) immediateDispatchEligible(op *deliveryOperation, input *SubmitInput) bool {
	return input.DeliveryPolicy == recovery.InputDeliveryNextRequest &&
		!a.cancelCoversAcceptedOrder(op.acceptedOrder) &&
		!a.sessionExecutionPaused() && a.turn == nil && !a.mcpTransitionActive.Load() &&
		!a.pendingUserDrainSuspended && a.delivery.inflight == nil &&
		len(a.delivery.pending) == 0 && a.delivery.persistenceError == nil &&
		!a.deliveryHasRunnableQueue()
}

func (a *MainAgent) applyDeliveryOperation(op *deliveryOperation, state *recovery.InputDeliveryState) error {
	_, target := op.identity()
	if target != a.InputDeliveryTarget() {
		return fmt.Errorf("input target changed; refresh the input queue")
	}
	expectedView := deliveryOperationExpectedView(op)
	if expectedView != 0 && expectedView != a.delivery.viewVersion {
		return fmt.Errorf("input queue view changed: %w", recovery.ErrInputDeliveryConflict)
	}
	switch {
	case op.submit != nil:
		input := op.submit
		dir := a.WorkDirSnapshot()
		if dir.Generation != target.WorkDirGeneration {
			return fmt.Errorf("working directory changed; refresh the input target")
		}
		state.NextSequence++
		next := recovery.PendingInput{InputID: op.inputID, OwnerTaskID: identity.MainAgentID, Sequence: state.NextSequence, Revision: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: input.DeliveryPolicy, WorkDir: dir.Path, WorktreeID: dir.WorktreeID, DraftID: input.DraftID, RequestID: input.RequestID, Content: input.Content, Parts: cloneContentParts(input.Parts), FromUser: true}
		// An after-completion input queued while work is running waits for that
		// work to settle successfully; one queued while idle starts at the next
		// idle. newTurn re-points stale waits, so the wait never gets stuck.
		// The running work is recorded for every mid-turn submit so its settle
		// outcome stays readable no matter which policy the input carries.
		if a.delivery.currentWorkID != "" {
			if input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion {
				next.WaitWorkID = a.delivery.currentWorkID
			}
			ensureDeliveryWork(state, a.delivery.currentWorkID, a.delivery.currentOrigin)
		}
		state.Inputs = append(state.Inputs, next)
	case op.update != nil:
		update := op.update
		input, err := deliveryInputForEdit(state, update.InputID, update.ExpectedRevision)
		if err != nil {
			return err
		}
		policyChanged := false
		if update.DeliveryPolicy != "" {
			if update.DeliveryPolicy != recovery.InputDeliveryNextRequest && update.DeliveryPolicy != recovery.InputDeliveryAfterCompletion {
				return fmt.Errorf("invalid delivery policy %q", update.DeliveryPolicy)
			}
			policyChanged = input.DeliveryPolicy != update.DeliveryPolicy
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
		// Editing the content keeps the wait, so an edit never starts an input
		// that was held behind a predecessor. Switching the policy re-sets it:
		// a next-request input never waits, and a newly after-completion input
		// waits on whatever is running now.
		if policyChanged {
			input.WaitWorkID = ""
			if input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion && a.delivery.currentWorkID != "" {
				input.WaitWorkID = a.delivery.currentWorkID
				ensureDeliveryWork(state, a.delivery.currentWorkID, a.delivery.currentOrigin)
			}
		}
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

func deliveryOperationExpectedView(op *deliveryOperation) uint64 {
	switch {
	case op.submit != nil:
		return op.submit.ExpectedViewVersion
	case op.update != nil:
		return op.update.ExpectedViewVersion
	case op.delete != nil:
		return op.delete.ExpectedViewVersion
	case op.control != nil:
		return op.control.ExpectedViewVersion
	default:
		return 0
	}
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
		} else {
			if !held {
				return recovery.ErrInputDeliveryConflict
			}
			removeDeliveryHold(state, input.InputID)
		}
		input.Revision++
	case InputQueueClear:
		if control.ExpectedQueueRevision != state.Revision {
			return recovery.ErrInputDeliveryConflict
		}
		state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool {
			if input.Stage == recovery.PendingInputStageQueued && input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion {
				removeDeliveryHold(state, input.InputID)
				return true
			}
			return false
		})
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
	return len(a.runnableDeliveryInputs(true)) > 0
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
