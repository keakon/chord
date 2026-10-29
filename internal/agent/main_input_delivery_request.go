package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type deliveryRequest struct {
	turnID, turnEpoch uint64
	source            string
	fallback          *llmFallbackBoundaryPayload
	retry             *llmRetryBoundaryPayload
}

// boundaryInputMessages exposes the request-boundary payload that owns the
// transcript rows appended while the queued input was claimed.
func (r *deliveryRequest) boundaryInputMessages() *[]message.Message {
	if r == nil {
		return nil
	}
	if r.fallback != nil {
		return &r.fallback.inputMessages
	}
	if r.retry != nil {
		return &r.retry.inputMessages
	}
	return nil
}

func (a *MainAgent) prepareDeliveryRequest(_ context.Context, turnID uint64, source string) bool {
	pending := deliveryRequest{turnID: turnID, source: source}
	if a.turn != nil {
		pending.turnEpoch = a.turn.Epoch
	}
	return a.prepareInputDeliveryRequest(pending)
}

func (a *MainAgent) prepareInputDeliveryRequest(pending deliveryRequest) bool {
	if a.delivery.owner == nil && len(a.delivery.pending) == 0 && a.delivery.inflight == nil {
		return false
	}
	if a.delivery.persistenceError != nil {
		a.delivery.request = &pending
		return true
	}
	if a.delivery.inflight != nil || len(a.delivery.pending) > 0 {
		if a.turn != nil {
			a.delivery.request = &pending
		}
		return true
	}
	if a.retryDeliveryPermit() {
		return true
	}
	if a.turn != nil && a.delivery.committed != nil {
		for _, input := range a.delivery.committed.Inputs {
			if input.WorkID == a.turn.DeliveryWorkID && input.Stage == recovery.PendingInputStageClaimed {
				a.delivery.persistenceError = fmt.Errorf("input execution permit is missing; stop and explicitly continue the current work")
				a.publishInputDelivery()
				return true
			}
		}
	}
	ids := a.runnableDeliveryInputs(false)
	if len(ids) == 0 {
		return false
	}
	if a.turn == nil || a.turn.ID != pending.turnID {
		return true
	}
	a.delivery.request = &pending
	a.claimDeliveryInputs(ids, false)
	return true
}

func (a *MainAgent) resumeDeliveryRequest() {
	if a.delivery.inflight != nil || len(a.delivery.pending) > 0 || a.delivery.persistenceError != nil || a.delivery.environmentError != "" {
		return
	}
	if a.retryDeliveryPermit() {
		return
	}
	pending := a.delivery.request
	if pending == nil {
		a.queueLoopEvent(Event{Type: eventInputDeliveryAdvance})
		return
	}
	a.delivery.request = nil
	if a.turn == nil || a.turn.ID != pending.turnID || a.turn.Epoch != pending.turnEpoch || a.turn.Ctx.Err() != nil {
		return
	}
	if pending.fallback != nil {
		a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: pending.turnID, Payload: pending.fallback})
		return
	}
	if pending.retry != nil {
		a.handleLLMRetryBoundary(Event{Type: EventLLMRetryBoundary, TurnID: pending.turnID, Payload: pending.retry})
		return
	}
	a.beginMainLLMAfterPreparation(a.turn.Ctx, pending.turnID, pending.source)
}

func (a *MainAgent) advanceInputDelivery() {
	if a.turn != nil || a.pendingUserDrainSuspended || a.pendingHandoff != nil || a.delivery.navigation || a.sessionExecutionPaused() || a.shuttingDown.Load() || a.mcpTransitionActive.Load() || a.questionWaiting.Load() || a.IsCompactionRunning() || a.delivery.persistenceError != nil || a.delivery.inflight != nil || len(a.delivery.pending) > 0 {
		return
	}
	ids := a.runnableDeliveryInputs(true)
	if len(ids) == 0 {
		return
	}
	a.claimDeliveryInputs(ids, true)
}

func (a *MainAgent) claimDeliveryInputs(ids []string, newTurn bool) {
	if a.retireCoveredDeliveryInputs(ids) {
		return
	}
	dir := a.WorkDirSnapshot()
	for _, input := range a.delivery.committed.Inputs {
		if slices.Contains(ids, input.InputID) && (input.WorkDir != dir.Path || input.WorktreeID != dir.WorktreeID) {
			if a.delivery.environmentError == "" {
				a.delivery.environmentError = "Queued input belongs to another working directory: " + input.WorkDir
				a.publishInputDelivery()
				a.emitToTUI(ToastEvent{Message: a.delivery.environmentError, Level: "warn"})
			}
			return
		}
	}
	workID := a.delivery.currentWorkID
	origin := a.delivery.currentOrigin
	if newTurn || workID == "" {
		origin = recovery.DeliveryWorkUser
		for _, input := range a.delivery.committed.Inputs {
			if slices.Contains(ids, input.InputID) && input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion {
				origin = recovery.DeliveryWorkFollowup
			}
		}
		workID = a.newDeliveryWork(origin)
	}
	cancelGeneration := a.delivery.cancelGeneration.Load()
	op := &deliveryOperation{}
	op.mutate = func(state *recovery.InputDeliveryState) error {
		for i := range state.Inputs {
			input := &state.Inputs[i]
			if !slices.Contains(ids, input.InputID) {
				continue
			}
			if input.Stage != recovery.PendingInputStageQueued || slices.Contains(state.EditHolds, input.InputID) {
				return recovery.ErrInputDeliveryConflict
			}
			if input.WorkDir != dir.Path || input.WorktreeID != dir.WorktreeID {
				return fmt.Errorf("queued input belongs to another working directory")
			}
			input.Stage, input.WorkID = recovery.PendingInputStageClaimed, workID
			input.WaitWorkID = ""
			input.Revision++
		}
		ensureDeliveryWork(state, workID, origin)
		return nil
	}
	op.finish = func() {
		if a.sessionExecutionPaused() || a.delivery.navigation {
			return
		}
		if a.delivery.cancelGeneration.Load() != cancelGeneration {
			a.settleDeliveryWork(workID, recovery.DeliveryWorkCancelled)
			return
		}
		if newTurn {
			a.mailboxDeliveryPaused.Store(false)
			a.tryRecoverPersistenceBeforeTurn()
			a.stageNextSubAgentMailboxBatch()
			a.newTurn(workID)
		} else if a.turn != nil {
			a.turn.DeliveryWorkID = workID
		}
		if a.turn == nil || a.turn.Ctx.Err() != nil {
			a.settleDeliveryWork(workID, recovery.DeliveryWorkCancelled)
			return
		}
		turnID, turnEpoch := a.turn.ID, a.turn.Epoch
		existing := make(map[string]bool)
		for _, msg := range a.ctxMgr.Snapshot() {
			if msg.Role == message.RoleUser && msg.InputID != "" {
				existing[msg.InputID] = true
			}
		}
		// Capture only the new durable input and any skill records it adds. A
		// boundary owner already holds the prepared history and tail overlays.
		var before int
		var boundaryInputs *[]message.Message
		if pending := a.delivery.request; pending != nil {
			boundaryInputs = pending.boundaryInputMessages()
			if boundaryInputs != nil {
				before = a.ctxMgr.MessageCount()
			}
		}
		for _, input := range a.delivery.committed.Inputs {
			if !slices.Contains(ids, input.InputID) || existing[input.InputID] {
				continue
			}
			a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, InputID: input.InputID, InputRequestID: input.RequestID, Content: input.Content, Parts: cloneContentParts(input.Parts)})
		}
		if boundaryInputs != nil {
			*boundaryInputs = append(*boundaryInputs, a.ctxMgr.Snapshot()[before:]...)
		}
		permit := a.newDeliveryPermit(ids, workID, dir, turnID, turnEpoch, cancelGeneration)
		a.delivery.permit = permit
		a.delivery.pending = append(a.delivery.pending, permit)
	}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}

// Covered ordinary inputs retain their cancelled transcript presentation, but
// cannot cancel a newer work or carry a newer input into their cancellation.
func (a *MainAgent) retireCoveredDeliveryInputs(ids []string) bool {
	var covered []string
	for _, input := range a.delivery.committed.Inputs {
		if input.DeliveryPolicy == recovery.InputDeliveryNextRequest && slices.Contains(ids, input.InputID) && a.cancelCoversAcceptedOrder(a.delivery.acceptedOrders[input.InputID]) {
			covered = append(covered, input.InputID)
		}
	}
	if len(covered) == 0 {
		return false
	}
	workID := rand.Text()
	op := &deliveryOperation{mutate: func(state *recovery.InputDeliveryState) error {
		ensureDeliveryWork(state, workID, recovery.DeliveryWorkUser)
		for i := range state.Inputs {
			input := &state.Inputs[i]
			if slices.Contains(covered, input.InputID) {
				input.Stage, input.WorkID = recovery.PendingInputStageClaimed, workID
				input.Revision++
			}
		}
		return nil
	}}
	op.finish = func() {
		if a.sessionExecutionPaused() || a.delivery.navigation {
			return
		}
		present := make(map[string]bool)
		for _, msg := range a.ctxMgr.Snapshot() {
			present[msg.InputID] = true
		}
		var consumed []recovery.PendingInput
		for _, input := range a.delivery.committed.Inputs {
			if slices.Contains(covered, input.InputID) {
				if !present[input.InputID] {
					a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, InputID: input.InputID, InputRequestID: input.RequestID, Content: input.Content, Parts: cloneContentParts(input.Parts)})
				}
				consumed = append(consumed, input)
			}
		}
		retire := &deliveryOperation{syncTranscript: true, mutate: func(state *recovery.InputDeliveryState) error {
			state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool { return slices.Contains(covered, input.InputID) })
			for i := range state.Works {
				if state.Works[i].WorkID == workID {
					state.Works[i].Outcome = recovery.DeliveryWorkCancelled
				}
			}
			return nil
		}, finish: func() {
			for _, input := range consumed {
				a.emitToTUI(PendingDraftConsumedEvent{Target: a.InputDeliveryTarget(), InputID: input.InputID, Revision: input.Revision, DraftID: input.DraftID, WorkID: workID, Parts: messagePartsForTUI(message.Message{Content: input.Content, Parts: input.Parts})})
			}
		}}
		a.delivery.pending = append(a.delivery.pending, retire)
	}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
	return true
}
