package agent

import (
	"crypto/rand"
	"fmt"
	"sort"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type stagedSessionInput struct {
	input           pendingUserMessage
	startedBaseline uint64
}

type sessionOwnerInputsDispatched struct {
	owner    string
	inputIDs []string
}

func (a *MainAgent) NoteSessionInputsDispatched(ownerTaskID string, inputIDs []string) {
	a.recoverySnapshotMu.Lock()
	needed := a.pause.checkpoint != nil
	a.recoverySnapshotMu.Unlock()
	if !needed {
		return
	}
	a.sendEvent(Event{Type: eventSessionOwnerInputsDispatched, Payload: sessionOwnerInputsDispatched{owner: ownerTaskID, inputIDs: append([]string(nil), inputIDs...)}})
}

func (a *MainAgent) sendSessionInput(content string, parts []message.ContentPart, requestID string) bool {
	gate := a.sessionExecutionGate()
	parts = cloneContentParts(parts)
	for {
		select {
		case <-a.stoppingCh:
			return false
		default:
		}
		// Admission and enqueue are atomic with respect to the transfer cutoff.
		// Never retain inputMu while waiting for capacity: session installation
		// runs on the only loop that can drain the event queue.
		a.pause.inputMu.Lock()
		if a.pause.inputClosed || gate != a.sessionExecutionGate() {
			a.pause.inputMu.Unlock()
			a.emitInputResult(requestID, InputRejected, "Session changed or is transferring; input was not accepted", 0)
			return false
		}
		a.eventMu.Lock()
		channelAvailable := len(a.deferredEvents) == 0 && len(a.loopEvents) == 0 && len(a.eventCh) < cap(a.eventCh)
		if channelAvailable || len(a.deferredEvents) < a.eventOverflowLimit {
			if a.pause.inputPrefix == "" {
				a.pause.inputPrefix = rand.Text()
			}
			p := a.acceptRawUserMessage(content, parts)
			p.RequestID = requestID
			a.trySendEventLocked(Event{Type: EventUserMessage, Payload: p})
			a.eventMu.Unlock()
			a.pause.inputMu.Unlock()
			return true
		}
		a.eventBackpressure.Add(1)
		a.eventMu.Unlock()
		a.pause.inputMu.Unlock()
		select {
		case <-a.eventSpaceCh:
		case <-a.stoppingCh:
			return false
		}
	}
}

func (a *MainAgent) stageSessionInput(p pendingUserMessage) {
	if p.InputID == "" {
		return
	}
	for _, existing := range a.stagedMainInputs {
		if existing.input.InputID == p.InputID {
			return
		}
	}
	p.Stage = recovery.PendingInputStageTranscriptStaged
	a.stagedMainInputs = append(a.stagedMainInputs, stagedSessionInput{input: p, startedBaseline: a.requestStarts.Load()})
}

func (a *MainAgent) retireDispatchedSessionInputs(starts uint64) {
	consumed := make(map[string]bool)
	remaining := a.stagedMainInputs[:0]
	for _, entry := range a.stagedMainInputs {
		if entry.startedBaseline < starts {
			consumed[entry.input.InputID] = true
		} else {
			remaining = append(remaining, entry)
		}
	}
	a.stagedMainInputs = remaining
	ids := make([]string, 0, len(consumed))
	for id := range consumed {
		ids = append(ids, id)
	}
	a.retireOwnerSessionInputs(sessionOwnerInputsDispatched{owner: identity.MainAgentID, inputIDs: ids})
}

func (a *MainAgent) retireOwnerSessionInputs(dispatched sessionOwnerInputsDispatched) {
	consumed := make(map[string]bool, len(dispatched.inputIDs))
	for _, id := range dispatched.inputIDs {
		consumed[id] = true
	}
	if len(consumed) == 0 {
		return
	}
	if a.pause.parkedInputs == nil {
		a.pause.parkedInputs = make(map[string][]pendingUserMessage)
	}
	parked := a.pause.parkedInputs[dispatched.owner]
	remaining := parked[:0]
	for _, input := range parked {
		if !consumed[input.InputID] {
			remaining = append(remaining, input)
		}
	}
	if len(remaining) == 0 {
		delete(a.pause.parkedInputs, dispatched.owner)
	} else {
		a.pause.parkedInputs[dispatched.owner] = remaining
	}
	a.recoverySnapshotMu.Lock()
	hasCheckpoint := a.pause.checkpoint != nil
	a.recoverySnapshotMu.Unlock()
	if !hasCheckpoint {
		return
	}
	base := a.buildRecoverySnapshot()
	a.outputWg.Go(func() {
		// Persist transcript first. A crash before the checkpoint update then
		// reconciles its InputID without appending the user's text again.
		if !a.persist.flush() {
			a.notePersistenceFailure(fmt.Errorf("input retirement persistence barrier unavailable"))
			return
		}
		manager := a.recoveryManager()
		if manager == nil {
			a.notePersistenceFailure(fmt.Errorf("input retirement recovery writer unavailable"))
			return
		}
		if err := a.persist.strictError(manager); err != nil {
			a.notePersistenceFailure(err)
			return
		}
		a.recoverySnapshotMu.Lock()
		defer a.recoverySnapshotMu.Unlock()
		checkpoint := a.pause.checkpoint.Clone()
		if checkpoint == nil {
			return
		}
		inputs := checkpoint.PendingInputs[:0]
		for _, input := range checkpoint.PendingInputs {
			if input.OwnerTaskID != dispatched.owner || !consumed[input.InputID] {
				inputs = append(inputs, input)
			}
		}
		checkpoint.PendingInputs = inputs
		snapshot, err := manager.Recover()
		if err != nil {
			snapshot = base
		}
		snapshot.Pause = checkpoint
		if err := manager.SaveSnapshot(snapshot); err != nil {
			a.notePersistenceFailure(err)
			return
		}
		a.pause.checkpoint = checkpoint
		a.emitToTUI(SessionPendingInputsUpdatedEvent{InputIDs: append([]string(nil), dispatched.inputIDs...)})
	})
}

func pendingPauseInput(p pendingUserMessage, owner string) recovery.PendingInput {
	stage := p.Stage
	if stage == "" {
		stage = recovery.PendingInputStageQueued
	}
	return recovery.PendingInput{InputID: p.InputID, OwnerTaskID: owner, Sequence: p.AcceptedOrder, Revision: p.Revision, DraftID: p.DraftID, RequestID: p.RequestID, Content: p.Content, Parts: cloneContentParts(p.Parts), Kind: p.Kind, FromUser: p.FromUser, MailboxAckID: p.MailboxAckID, Mailbox: p.Mailbox, DrainContextAppends: p.DrainContextAppends, Stage: stage}
}

func restorePauseInput(p recovery.PendingInput) pendingUserMessage {
	return pendingUserMessage{InputID: p.InputID, AcceptedOrder: p.Sequence, Revision: p.Revision, DraftID: p.DraftID, RequestID: p.RequestID, Content: p.Content, Parts: cloneContentParts(p.Parts), Kind: p.Kind, FromUser: p.FromUser, MailboxAckID: p.MailboxAckID, Mailbox: p.Mailbox, DrainContextAppends: p.DrainContextAppends, Stage: p.Stage}
}

func (a *MainAgent) captureMainPauseInputs(id recovery.TransferIdentity, frozenAt time.Time) *recovery.SessionPauseCheckpoint {
	checkpoint := &recovery.SessionPauseCheckpoint{Identity: id, CheckpointID: rand.Text(), Paused: true, FrozenAt: frozenAt}
	seen := make(map[string]bool)
	if state := a.delivery.committed; state != nil {
		checkpoint.InputDeliveryRevision = state.Revision
		for _, input := range state.Inputs {
			seen[input.InputID] = true
		}
	}
	appendInput := func(p pendingUserMessage, owner string) {
		if p.InputID == "" {
			p.InputID = rand.Text()
		}
		if seen[p.InputID] {
			return
		}
		seen[p.InputID] = true
		checkpoint.PendingInputs = append(checkpoint.PendingInputs, pendingPauseInput(p, owner))
	}
	for _, p := range a.pendingUserMessages {
		appendInput(p, identity.MainAgentID)
	}
	starts := a.requestStarts.Load()
	for _, entry := range a.stagedMainInputs {
		if starts <= entry.startedBaseline {
			appendInput(entry.input, identity.MainAgentID)
		}
	}
	for owner, inputs := range a.pause.parkedInputs {
		for _, p := range inputs {
			appendInput(p, owner)
		}
	}
	sort.SliceStable(checkpoint.PendingInputs, func(i, j int) bool {
		a, b := checkpoint.PendingInputs[i], checkpoint.PendingInputs[j]
		if a.OwnerTaskID != b.OwnerTaskID {
			return a.OwnerTaskID < b.OwnerTaskID
		}
		return a.Sequence < b.Sequence
	})
	return checkpoint
}

func (a *MainAgent) captureSubPauseInputs(checkpoint *recovery.SessionPauseCheckpoint) error {
	for _, sub := range a.subs.snapshotSubAgents() {
		capture, err := sub.CaptureSessionPause(a.parentCtx)
		if err != nil {
			return fmt.Errorf("capture task %s: %w", sub.taskID, err)
		}
		for _, p := range capture.PendingInputs {
			checkpoint.PendingInputs = append(checkpoint.PendingInputs, pendingPauseInput(p, capture.TaskID))
		}
		for _, msg := range capture.ContextAppends {
			checkpoint.PendingInputs = append(checkpoint.PendingInputs, recovery.PendingInput{InputID: rand.Text(), OwnerTaskID: capture.TaskID, Content: msg.Content, Parts: msg.Parts, Kind: msg.Kind, Stage: recovery.PendingInputStageQueued})
		}
	}
	return nil
}

// PendingSessionInputs is a view of the durable checkpoint, never a second
// submission channel. Binary content remains in session-owned attachments.
func (a *MainAgent) PendingSessionInputs() []recovery.PendingInput {
	a.recoverySnapshotMu.Lock()
	var inputs []recovery.PendingInput
	if a.pause.checkpoint != nil {
		inputs = a.pause.checkpoint.Clone().PendingInputs
	}
	a.recoverySnapshotMu.Unlock()
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		seen[input.InputID] = true
	}
	if snapshot := a.InputQueueSnapshot(); snapshot != nil && snapshot.State != nil {
		for _, input := range snapshot.State.Inputs {
			if !seen[input.InputID] {
				inputs = append(inputs, input)
				seen[input.InputID] = true
			}
		}
	}
	return inputs
}

// WaitingSubAgentMailboxMessages is the main-agent view of the mailbox messages
// still waiting for the next request, in delivery order. Restore replays them
// through MailboxQueuedEvent before the session-restore rebuild, and the TUI
// clears that queue when the switch starts; this reader lets the rebuild
// re-project the waiting rows instead of losing them for the rest of the run.
//
// Only the in-memory queues are read. A waiting message that spilled to the
// durable spool under memory pressure is not in either queue, so its row is not
// re-projected after a switch; the message itself stays in the mailbox log and
// is delivered with the next request.
func (a *MainAgent) WaitingSubAgentMailboxMessages() []SubAgentMailboxMessage {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	waiting := make([]SubAgentMailboxMessage, 0, len(a.subAgentInbox.urgent)+len(a.subAgentInbox.normal))
	waiting = append(waiting, a.subAgentInbox.urgent...)
	waiting = append(waiting, a.subAgentInbox.normal...)
	return waiting
}

func (a *MainAgent) handlePausedSessionInput(content string, parts []message.ContentPart, inputID, requestID string, order int64) bool {
	if !a.sessionExecutionPaused() {
		return false
	}
	if !a.pause.restored && !IsTUILocalOnlySlashCommand(content) && !IsIdleOnlySlashCommand(content) {
		a.handleDeliveryOperation(&deliveryOperation{admittedBeforePause: true, inputID: inputID, acceptedOrder: order, submit: &SubmitInput{OperationID: makeRequestID(), RequestID: requestID, Target: a.InputDeliveryTarget(), DeliveryPolicy: recovery.InputDeliveryNextRequest, Content: content, Parts: parts}})
		return true
	}
	if !a.pause.restored {
		a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{InputID: inputID, RequestID: requestID, AcceptedOrder: order, Content: content, Parts: parts, FromUser: true})
		a.emitInputResult(requestID, InputQueued, "", 0)
		return true
	}
	a.pause.resumeMain = true
	// Include the new constraint in the same batch as restored inputs before
	// any of those inputs can start a model request.
	a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{InputID: inputID, RequestID: requestID, AcceptedOrder: order, Content: content, Parts: parts, FromUser: true})
	a.emitInputResult(requestID, InputQueued, "", 0)
	if !a.resumePausedSessionForUser() {
		return true
	}
	a.resumePendingUserDrain()
	a.drainPendingUserMessages()
	return true
}
