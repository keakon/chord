package agent

import (
	"context"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// runLoop is the SubAgent's event loop. It runs in an independent goroutine.
// All state modifications happen in this single thread; user messages arrive
// via the inputCh channel.
func (s *SubAgent) runLoop() {
	log.Debugf("SubAgent event loop started instance=%v task_id=%v agent_def=%v", s.instanceID, s.taskID, s.agentDefName)
	defer func() {
		s.doneOnce.Do(func() { close(s.done) })
		log.Debugf("SubAgent event loop stopped instance=%v", s.instanceID)
	}()

	for {
		s.refillInputChannelFromOverflow()
		s.refillContextAppendChannelFromOverflow()
		if msg, ok := s.tryReceiveContextAppend(); ok {
			s.appendContextOnly(msg)
			continue
		}
		if s.canStartUserTurn() {
			if input, ok := s.tryReceiveUserInput(); ok {
				s.resetIdleTimer()
				s.handleUserInput(input)
				continue
			}
		}
		if s.tryHandleContinueSignal() {
			continue
		}
		if s.tryHandlePendingContinue() {
			continue
		}
		if result, ok := s.dequeuePromotedToolResult(); ok {
			s.resetIdleTimer()
			s.handleToolResult(result)
			continue
		}

		var idleCh <-chan time.Time
		if s.idleTimer != nil {
			idleCh = s.idleTimer.C
		}
		var inputCh <-chan pendingUserMessage
		if s.canStartUserTurn() {
			inputCh = s.inputCh
		}

		select {
		case input := <-inputCh:
			s.resetIdleTimer()
			s.handleUserInput(input)
			s.refillInputChannelFromOverflow()

		case msg := <-s.ctxAppendCh:
			s.appendContextOnly(msg)
			s.refillContextAppendChannelFromOverflow()

		case msg := <-s.continueCh:
			s.handleContinueSignal(msg)

		case <-s.wakeCh:
			// State transitions and queue producers use wakeCh to make this
			// select rebuild state-gated channels such as inputCh.

		case result := <-s.llmCh:
			s.finishLLMRequest()
			s.handleLLMResponse(result)
			if s.canStartUserTurn() {
				s.refillInputChannelFromOverflow()
			}

		case result := <-s.toolCh:
			s.resetIdleTimer()
			s.handleToolResult(result)

		case <-idleCh:
			s.sendEvent(Event{Type: EventAgentIdle, Payload: s.idleTimeout})
			s.idleTimer = nil

		case <-s.parentCtx.Done():
			if s.idleTimer != nil {
				s.idleTimer.Stop()
			}
			if s.turn != nil {
				cancelledExec := s.turn.cancelPendingToolCalls()
				cancelledStream := s.turn.drainStreamingToolCalls()
				merged := mergePendingToolCalls(cancelledExec, cancelledStream)
				merged = s.turn.filterCompletedToolCalls(merged)
				if len(merged) > 0 {
					persistedResults := finalizeInterruptedToolCalls(s.ctxMgr, s.parent.emitToTUI, s.persistInterruptedToolResults, merged, ToolResultStatusCancelled, context.Canceled)
					if persistedResults > 0 {
						log.Debugf("SubAgent: persisted interrupted tool-call results during shutdown agent=%v count=%v", s.instanceID, persistedResults)
					}
					s.parent.emitActivity(s.instanceID, ActivityIdle, "")
				}
			}
			return
		}
	}
}

func (s *SubAgent) finishLLMRequest() {
	if s == nil {
		return
	}
	s.llmRequestInFlight.Store(false)
	s.parent.sendEvent(Event{Type: EventSubAgentRequestBoundary, SourceID: s.instanceID})
}

func (s *SubAgent) canStartUserTurn() bool {
	if s == nil || s.State() != SubAgentStateRunning || s.llmRequestInFlight.Load() {
		return false
	}
	if s.turn == nil || s.idleTimer != nil {
		return true
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turn.PendingToolCalls.Load() == 0 && s.turn.activeToolBatchCancel == nil
}

func (s *SubAgent) canPark() bool {
	if s == nil || s.llmRequestInFlight.Load() {
		return false
	}
	switch s.State() {
	case SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant, SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
	default:
		return false
	}
	if len(s.inputCh) > 0 || len(s.ctxAppendCh) > 0 || len(s.llmCh) > 0 || len(s.toolCh) > 0 || len(s.continueCh) > 0 {
		return false
	}
	s.inputQueueMu.Lock()
	inputOverflow := len(s.inputOverflow)
	s.inputQueueMu.Unlock()
	s.ctxAppendQueueMu.Lock()
	contextOverflow := len(s.ctxAppendOverflow)
	s.ctxAppendQueueMu.Unlock()
	if inputOverflow > 0 || contextOverflow > 0 {
		return false
	}
	return true
}

func (s *SubAgent) dequeuePromotedToolResult() (*toolResult, bool) {
	if s == nil || len(s.promotedToolQueue) == 0 {
		return nil, false
	}
	result := s.promotedToolQueue[0]
	s.promotedToolQueue[0] = nil
	s.promotedToolQueue = s.promotedToolQueue[1:]
	return result, true
}

func (s *SubAgent) refillInputChannelFromOverflow() {
	if s == nil {
		return
	}
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()
	for len(s.inputOverflow) > 0 {
		select {
		case s.inputCh <- s.inputOverflow[0]:
			s.inputOverflow = s.inputOverflow[1:]
		default:
			return
		}
	}
}

func (s *SubAgent) refillContextAppendChannelFromOverflow() {
	if s == nil {
		return
	}
	s.ctxAppendQueueMu.Lock()
	defer s.ctxAppendQueueMu.Unlock()
	for len(s.ctxAppendOverflow) > 0 {
		select {
		case s.ctxAppendCh <- s.ctxAppendOverflow[0]:
			s.ctxAppendOverflow = s.ctxAppendOverflow[1:]
		default:
			return
		}
	}
}

func (s *SubAgent) tryReceiveUserInput() (pendingUserMessage, bool) {
	if s == nil {
		return pendingUserMessage{}, false
	}
	select {
	case input := <-s.inputCh:
		return input, true
	default:
		return pendingUserMessage{}, false
	}
}

func (s *SubAgent) tryReceiveContextAppend() (message.Message, bool) {
	if s == nil {
		return message.Message{}, false
	}
	select {
	case msg := <-s.ctxAppendCh:
		return msg, true
	default:
		return message.Message{}, false
	}
}

func (s *SubAgent) tryHandleContinueSignal() bool {
	if s == nil {
		return false
	}
	select {
	case msg := <-s.continueCh:
		s.handleContinueSignal(msg)
		return true
	default:
		return false
	}
}

func (s *SubAgent) handleContinueSignal(msg continueMsg) {
	if msg.cancelCurrentTurn {
		s.pendingContinue = nil
		s.cancelCurrentTurnFromLoop()
		return
	}
	if s.llmRequestInFlight.Load() {
		if msg.restartStoppedTurn {
			pending := msg
			s.pendingContinue = &pending
		}
		return
	}
	if s.turn == nil || msg.restartStoppedTurn {
		s.handleContinueMessage(msg)
	}
}

func (s *SubAgent) tryHandlePendingContinue() bool {
	if s.pendingContinue == nil || s.llmRequestInFlight.Load() {
		return false
	}
	msg := *s.pendingContinue
	s.pendingContinue = nil
	s.handleContinueMessage(msg)
	return true
}

func (s *SubAgent) handleContinueMessage(msg continueMsg) {
	if msg.drainContextAppends {
		s.drainContextAppendsBeforeTurn()
	}
	s.handleContinue()
}
