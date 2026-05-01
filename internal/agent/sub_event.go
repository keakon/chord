package agent

import (
	"context"
	"github.com/keakon/golog/log"
	"time"

	"github.com/keakon/chord/internal/message"
)

// runLoop is the SubAgent's event loop. It runs in an independent goroutine.
// All state modifications happen in this single thread; user messages arrive
// via the inputCh channel.
func (s *SubAgent) runLoop() {
	log.Infof("SubAgent event loop started instance=%v task_id=%v agent_def=%v", s.instanceID, s.taskID, s.agentDefName)
	defer log.Infof("SubAgent event loop stopped instance=%v", s.instanceID)

	for {
		s.refillInputChannelFromOverflow()
		s.refillContextAppendChannelFromOverflow()
		if msg, ok := s.tryReceiveContextAppend(); ok {
			s.appendContextOnly(msg)
			continue
		}
		if input, ok := s.tryReceiveUserInput(); ok {
			s.resetIdleTimer()
			s.handleUserInput(input)
			continue
		}
		if s.tryHandleContinueSignal() {
			continue
		}

		var idleCh <-chan time.Time
		if s.idleTimer != nil {
			idleCh = s.idleTimer.C
		}

		select {
		case input := <-s.inputCh:
			s.resetIdleTimer()
			s.handleUserInput(input)
			s.refillInputChannelFromOverflow()

		case msg := <-s.ctxAppendCh:
			s.appendContextOnly(msg)
			s.refillContextAppendChannelFromOverflow()

		case <-s.continueCh:
			if s.turn == nil {
				s.handleContinue()
			}

		case result := <-s.llmCh:
			s.handleLLMResponse(result)

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
				if len(merged) > 0 {
					persistedResults := s.persistInterruptedToolResults(merged, ToolResultStatusCancelled, context.Canceled)
					if persistedResults > 0 {
						log.Infof("SubAgent: persisted interrupted tool-call results during shutdown agent=%v count=%v", s.instanceID, persistedResults)
					}
					emitCancelledToolResults(s.parent.emitToTUI, merged)
					s.parent.emitActivity(s.instanceID, ActivityIdle, "")
				}
			}
			return
		}
	}
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
		if msg.cancelCurrentTurn {
			s.cancelCurrentTurnFromLoop()
			return true
		}
		if s.turn == nil {
			s.handleContinue()
		}
		return true
	default:
		return false
	}
}
