package agent

import (
	"context"
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

type llmFallbackBoundaryPayload struct {
	turnID           uint64
	messages         []message.Message
	tailOverlayCount int
	reply            chan llmFallbackBoundaryResult
}

type llmFallbackBoundaryResult struct {
	messages []message.Message
	err      error
}

// updateMainLLMRequestBeforeFallback pauses the retry worker at the boundary
// before a fallback provider request. The event loop owns pendingUserMessages,
// so it must decide which queued inputs have arrived and append them to both
// the durable context and the fallback request snapshot.
func (a *MainAgent) updateMainLLMRequestBeforeFallback(ctx context.Context, turnID uint64, messages []message.Message, tailOverlayCount int) ([]message.Message, error) {
	if a == nil {
		return messages, nil
	}
	if !a.started.Load() {
		// The pending queue is event-loop owned. A direct call without a running
		// event loop cannot safely consume it, so leave it for the normal drain.
		return messages, nil
	}

	payload := &llmFallbackBoundaryPayload{
		turnID:           turnID,
		messages:         messages,
		tailOverlayCount: tailOverlayCount,
		reply:            make(chan llmFallbackBoundaryResult, 1),
	}
	a.sendEvent(Event{
		Type:    EventLLMFallbackBoundary,
		TurnID:  turnID,
		Payload: payload,
	})
	select {
	case result := <-payload.reply:
		return result.messages, result.err
	case <-ctx.Done():
		return nil, fmt.Errorf("fallback request update cancelled: %w", ctx.Err())
	case <-a.parentCtx.Done():
		return nil, fmt.Errorf("fallback request update cancelled: %w", a.parentCtx.Err())
	case <-a.stoppingCh:
		return nil, context.Canceled
	}
}

func (a *MainAgent) handleLLMFallbackBoundary(evt Event) {
	payload, ok := evt.Payload.(*llmFallbackBoundaryPayload)
	if !ok || payload == nil || payload.reply == nil {
		log.Errorf("handleLLMFallbackBoundary: invalid payload type=%T", evt.Payload)
		return
	}
	if a.turn == nil || payload.turnID == 0 || a.turn.ID != payload.turnID || evt.TurnID != payload.turnID {
		payload.reply <- llmFallbackBoundaryResult{err: context.Canceled}
		return
	}
	payload.reply <- llmFallbackBoundaryResult{messages: a.consumePendingUserMessagesForRequest(payload.messages, payload.tailOverlayCount)}
}
