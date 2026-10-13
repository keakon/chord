package agent

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
)

type deliveryOperation struct {
	admittedBeforePause bool
	userIntentApplied   bool
	failed              func(error)
	loadOnly            bool
	acceptedOrder       int64
	submit              *SubmitInput
	update              *UpdateInput
	delete              *DeleteInput
	control             *QueueControl
	inputID             string
	mutate              func(*recovery.InputDeliveryState) error
	finish              func()
	resumeAllowed       func() bool
	syncTranscript      bool
}

func (op *deliveryOperation) identity() (string, InputTarget) {
	switch {
	case op.submit != nil:
		return op.submit.OperationID, op.submit.Target
	case op.update != nil:
		return op.update.OperationID, op.update.Target
	case op.delete != nil:
		return op.delete.OperationID, op.delete.Target
	case op.control != nil:
		return op.control.OperationID, op.control.Target
	default:
		return "", InputTarget{}
	}
}

type deliveryCommit struct {
	owner  *recoveryOwnership
	op     *deliveryOperation
	state  *recovery.InputDeliveryState
	err    error
	loaded bool
}

func (a *MainAgent) publishInputDelivery() {
	live := make(map[string]bool)
	if state := a.delivery.committed; state != nil {
		for _, input := range state.Inputs {
			live[input.InputID] = true
		}
	}
	for _, op := range a.delivery.pending {
		live[op.inputID] = true
	}
	if op := a.delivery.inflight; op != nil {
		live[op.inputID] = true
	}
	for id := range a.delivery.acceptedOrders {
		if !live[id] {
			delete(a.delivery.acceptedOrders, id)
		}
	}
	a.delivery.viewVersion++
	snapshot := &InputQueueSnapshot{Target: a.InputDeliveryTarget(), ViewVersion: a.delivery.viewVersion, State: a.delivery.committed.Clone(), RestoredNextInputsBlocked: a.delivery.restoredNextInputsBlocked}
	if a.delivery.environmentError != "" {
		snapshot.Blocked = a.delivery.environmentError
	}
	if a.delivery.loading {
		snapshot.Blocked = "Restored input queue is being reconciled"
	}
	if a.delivery.persistenceError != nil {
		snapshot.Blocked = a.delivery.persistenceError.Error()
	}
	a.delivery.snapshot.Store(snapshot)
	a.emitToTUI(InputQueueChangedEvent{Snapshot: snapshot.Clone()})
}

func (a *MainAgent) emitDeliveryResult(op *deliveryOperation, result string, err error) {
	id, target := op.identity()
	event := InputQueueOperationResultEvent{OperationID: id, Target: target, InputID: op.inputID, Result: result}
	if state := a.delivery.committed; state != nil {
		event.QueueRevision = state.Revision
		for _, input := range state.Inputs {
			if input.InputID == op.inputID {
				event.ItemRevision = input.Revision
			}
		}
	}
	if err != nil {
		event.Reason = err.Error()
	}
	if id != "" {
		a.emitToTUI(event)
	}
	if op.submit != nil && op.submit.RequestID != "" {
		status := InputQueued
		if result == InputQueueUnknown {
			status = InputUnknown
		} else if result != InputQueueCommitted {
			status = InputRejected
		}
		a.emitToTUI(InputResultEvent{Target: &target, RequestID: op.submit.RequestID, Status: status, Message: event.Reason, InputID: event.InputID, Revision: event.ItemRevision, QueueRevision: event.QueueRevision})
	}
}

func (a *MainAgent) startNextDeliveryOperation() {
	if a.delivery.inflight != nil || len(a.delivery.pending) == 0 {
		return
	}
	op := a.delivery.pending[0]
	owner := a.currentRecoveryOwnership()
	if owner == nil || owner.manager == nil {
		a.delivery.pending = a.delivery.pending[1:]
		a.rejectDeliveryOperation(op, InputQueueRejected, fmt.Errorf("durable input queue requires session storage"))
		a.startNextDeliveryOperation()
		return
	}
	if previous := a.delivery.owner; previous != nil && previous.epoch == owner.epoch && previous.dir == owner.dir {
		a.delivery.owner = owner
	}
	if a.delivery.owner != owner {
		a.delivery.inflight = op
		transcript := a.ctxMgr.Snapshot()
		a.outputWg.Go(func() {
			state, err := owner.manager.LoadInputDelivery()
			if err == nil && state != nil {
				restored := recovery.ReconcileInputDelivery(state, transcript)
				state, err = owner.manager.SaveInputDelivery(state.Revision, restored)
			}
			a.sendEvent(Event{Type: eventInputDeliveryCommitted, Payload: deliveryCommit{owner: owner, op: op, state: state, err: err, loaded: true}})
		})
		return
	}
	a.delivery.pending = a.delivery.pending[1:]
	if a.delivery.persistenceError != nil {
		a.rejectDeliveryOperation(op, InputQueueUnknown, a.delivery.persistenceError)
		a.startNextDeliveryOperation()
		return
	}
	if a.delivery.committed == nil && op.submit != nil && op.submit.DeliveryPolicy == recovery.InputDeliveryNextRequest && a.turn == nil && !a.sessionExecutionPaused() && !a.mcpTransitionActive.Load() && !a.pendingUserDrainSuspended && len(a.delivery.pending) == 0 {
		// Loading an absent queue at startup must not turn an ordinary idle
		// send into the first durable queue transaction.
		a.handleDeliveryOperation(op)
		return
	}
	state := a.delivery.committed.Clone()
	if state == nil {
		state = &recovery.InputDeliveryState{SessionID: filepath.Base(owner.dir), Gate: recovery.FollowupGate{Mode: recovery.FollowupGateRunning}}
	}
	base := state.Revision
	var err error
	if op.mutate != nil {
		err = op.mutate(state)
	} else {
		err = a.applyDeliveryOperation(op, state)
	}
	if err != nil {
		result := InputQueueRejected
		if errors.Is(err, recovery.ErrInputDeliveryConflict) {
			result = InputQueueConflict
		}
		a.rejectDeliveryOperation(op, result, err)
		a.startNextDeliveryOperation()
		return
	}
	pruneDeliveryWorks(state, a.delivery.currentWorkID)
	state.Revision++
	if err := recovery.ValidateInputDelivery(state, state.SessionID); err != nil {
		a.rejectDeliveryOperation(op, InputQueueRejected, err)
		a.startNextDeliveryOperation()
		return
	}
	a.delivery.inflight = op
	pump := a.persist
	stop := a.stoppingCh
	a.outputWg.Go(func() {
		var err error
		if op.syncTranscript {
			barrier := make(chan struct{})
			if pump == nil || !pump.enqueue(persistEntry{barrier: barrier}, stop) {
				err = errPersistenceQueueUnavailable
			} else {
				select {
				case <-barrier:
					err = pump.strictError(owner.manager)
				case <-stop:
					err = errPersistenceStopping
				}
			}
			if err == nil {
				err = owner.manager.SyncMessages(identity.MainAgentID)
			}
		}
		var committed *recovery.InputDeliveryState
		if err == nil {
			committed, err = owner.manager.SaveInputDelivery(base, state)
		}
		a.sendEvent(Event{Type: eventInputDeliveryCommitted, Payload: deliveryCommit{owner: owner, op: op, state: committed, err: err}})
	})
}

func (a *MainAgent) handleDeliveryCommitted(commit deliveryCommit) {
	if a.delivery.inflight != commit.op {
		return
	}
	a.delivery.inflight = nil
	if a.currentRecoveryOwnership() != commit.owner {
		a.emitDeliveryResult(commit.op, InputQueueUnknown, fmt.Errorf("input queue session changed; refresh its state"))
		a.delivery.owner = nil
		a.startNextDeliveryOperation()
		return
	}
	a.delivery.owner = commit.owner
	if commit.loaded {
		a.delivery.loading = false
	}
	if commit.err != nil {
		a.delivery.persistenceError = commit.err
		a.notePersistenceFailure(commit.err)
		if commit.loaded && len(a.delivery.pending) > 0 {
			a.delivery.pending = a.delivery.pending[1:]
		}
		result := InputQueueRejected
		if _, ok := errors.AsType[*recovery.InputDeliveryCommitUnknownError](commit.err); ok {
			result = InputQueueUnknown
		}
		a.emitDeliveryResult(commit.op, result, commit.err)
		if commit.op.failed != nil {
			commit.op.failed(commit.err)
		}
	} else {
		a.delivery.committed = commit.state
		if commit.loaded {
			a.delivery.persistenceError = nil
		}
		if commit.loaded && commit.op.loadOnly && len(a.delivery.pending) > 0 {
			a.delivery.pending = a.delivery.pending[1:]
		}
		if commit.loaded && commit.state != nil {
			for _, input := range commit.state.Inputs {
				if input.Stage == recovery.PendingInputStageQueued && input.DeliveryPolicy == recovery.InputDeliveryNextRequest {
					a.delivery.restoredNextInputsBlocked = true
				}
			}
		}
		if !commit.loaded {
			if commit.op.mutate == nil {
				a.delivery.environmentError = ""
			}
			if commit.op.submit != nil && commit.op.submit.DeliveryPolicy == recovery.InputDeliveryNextRequest {
				a.delivery.restoredNextInputsBlocked = false
				if !a.sessionExecutionPaused() {
					a.resumePendingUserDrain()
				}
				if !a.cancelCoversAcceptedOrder(commit.op.acceptedOrder) {
					a.mailboxDeliveryPaused.Store(false)
				}
			}
			if commit.op.control != nil && commit.op.control.Action == InputQueueResume && commit.op.resumeAllowed != nil && commit.op.resumeAllowed() {
				a.resumePendingUserDrain()
			}
			a.emitDeliveryResult(commit.op, InputQueueCommitted, nil)
			if commit.op.finish != nil {
				commit.op.finish()
			}
		}
	}
	a.publishInputDelivery()
	a.startNextDeliveryOperation()
	if a.delivery.inflight == nil && len(a.delivery.pending) == 0 && a.delivery.persistenceError == nil && a.turn == nil && a.compactionState.readyDraft != nil {
		a.applyReadyDraft()
	}
	a.resumeDeliveryRequest()
}

func (a *MainAgent) rejectDeliveryOperation(op *deliveryOperation, result string, err error) {
	a.emitDeliveryResult(op, result, err)
	if op.mutate != nil && op.failed == nil {
		a.delivery.persistenceError = err
		a.publishInputDelivery()
	}
	if op.failed != nil {
		op.failed(err)
	}
}
