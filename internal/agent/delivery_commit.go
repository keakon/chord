package agent

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type deliveryCommitAck struct {
	turn    *Turn
	Message message.Message
	Err     error
}

func successfulDeliveredFiles(files []message.DeliveredFile, failed bool) []message.DeliveredFile {
	if failed {
		return nil
	}
	return message.CloneDeliveredFiles(files)
}

// enqueueDeliveryCommit only publishes an acknowledgment; the owning loop is
// responsible for modifying context and releasing its pending-tool barrier.
func (a *MainAgent) enqueueDeliveryCommit(manager *recovery.RecoveryManager, owner string, msg message.Message, after func(*deliveryCommitAck)) error {
	msg = msg.Clone()
	finish := func(err error) { after(&deliveryCommitAck{Message: msg.Clone(), Err: err}) }
	if manager == nil {
		return fmt.Errorf("file delivery requires a persistent session")
	}
	if a.shuttingDown.Load() || !a.persist.tryEnqueue(persistEntry{durable: true, agentID: owner, msg: msg.Clone(), recovery: manager, after: finish}) {
		return errPersistenceQueueUnavailable
	}
	return nil
}
func (a *MainAgent) beginDeliveryCommit(p *ToolResultPayload) bool {
	if p.deliveryCommit != nil {
		if err := p.deliveryCommit.Err; err != nil {
			a.notePersistenceFailure(err)
			p.DeliveredFiles = nil
			p.Error = fmt.Errorf("file delivery persistence failed; outcome unknown, do not replay: %w", err)
			p.RecoveryState = message.ToolRecoveryStateOutcomeUnknown
			p.Result = p.Error.Error()
			p.composedTexts = &composedToolResultTexts{Display: p.Result, Context: p.Result, ErrorText: p.Result, IsError: true}
		}
		return false
	}
	if len(p.DeliveredFiles) == 0 || p.Error != nil {
		return false
	}
	if p.composedTexts == nil {
		a.composeToolResultTextsOffLoop(a.turn.ID, p)
		return true
	}
	if a.turn.Ctx.Err() != nil {
		p.DeliveredFiles = nil
		p.Error = context.Canceled
		p.Result = "File delivery canceled before durable submission"
		p.composedTexts = &composedToolResultTexts{Display: p.Result, Context: p.Result, ErrorText: p.Result, IsError: true}
		return false
	}
	if p.composedTexts.IsError {
		p.DeliveredFiles = nil
		return false
	}
	msg := a.buildToolResultMessage(p, p.composedTexts.Context, nil, false, toolProvenanceFromContext(a.ctxMgr, p.CallID))
	turn := a.turn
	turn.deliveryPending.Store(true)
	turn.pendingToolMu.Lock()
	turn.deliveryCommitCallID = p.CallID
	turn.pendingToolMu.Unlock()
	copyPayload := *p
	copyPayload.DeliveredFiles = message.CloneDeliveredFiles(p.DeliveredFiles)
	err := a.enqueueDeliveryCommit(a.recoveryManager(), identity.MainAgentID, msg, func(ack *deliveryCommitAck) {
		ack.turn = turn
		copyPayload.deliveryCommit = ack
		a.sendEvent(Event{Type: EventToolResult, TurnID: copyPayload.TurnID, Payload: &copyPayload})
	})
	if err != nil {
		p.deliveryCommit = &deliveryCommitAck{turn: turn, Err: err}
		return a.beginDeliveryCommit(p)
	}
	return true
}
func (s *SubAgent) beginDeliveryCommit(p *toolResult) bool {
	if p.deliveryCommit != nil {
		if err := p.deliveryCommit.Err; err != nil {
			s.notePersistenceFailure(err)
			p.DeliveredFiles = nil
			p.Error = fmt.Errorf("file delivery persistence failed; outcome unknown, do not replay: %w", err)
			p.RecoveryState = message.ToolRecoveryStateOutcomeUnknown
			p.Result = p.Error.Error()
			p.composed = &composedToolResultTexts{Display: p.Result, Context: p.Result, ErrorText: p.Result, IsError: true}
		}
		return false
	}
	if len(p.DeliveredFiles) == 0 || p.Error != nil {
		return false
	}
	if p.composed == nil {
		s.composeToolResultTextsOffLoop(p)
		return true
	}
	if s.turn.Ctx.Err() != nil {
		p.DeliveredFiles = nil
		p.Error = context.Canceled
		p.Result = "File delivery canceled before durable submission"
		p.composed = &composedToolResultTexts{Display: p.Result, Context: p.Result, ErrorText: p.Result, IsError: true}
		return false
	}
	if p.composed.IsError {
		p.DeliveredFiles = nil
		return false
	}
	msg := message.Message{Role: message.RoleTool, ToolCallID: p.CallID, Content: p.composed.Context, ToolPayload: p.Payload, ToolNotes: append([]string(nil), p.Notes...), ToolDurationMs: p.Duration.Milliseconds(), ToolStatus: message.ToolStatusSuccess, Audit: p.Audit.Clone(), Provenance: toolProvenanceFromContext(s.ctxMgr, p.CallID), DeliveredFiles: message.CloneDeliveredFiles(p.DeliveredFiles)}
	turn := s.turn
	turn.deliveryPending.Store(true)
	turn.pendingToolMu.Lock()
	turn.deliveryCommitCallID = p.CallID
	turn.pendingToolMu.Unlock()
	copyPayload := *p
	copyPayload.DeliveredFiles = message.CloneDeliveredFiles(p.DeliveredFiles)
	err := s.parent.enqueueDeliveryCommit(s.recoveryManager(), s.instanceID, msg, func(ack *deliveryCommitAck) {
		ack.turn = turn
		copyPayload.deliveryCommit = ack
		select {
		case s.toolCh <- &copyPayload:
		case <-s.parentCtx.Done():
		}
	})
	if err != nil {
		p.deliveryCommit = &deliveryCommitAck{turn: turn, Err: err}
		return s.beginDeliveryCommit(p)
	}
	return true
}
