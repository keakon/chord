package agent

import (
	"context"
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

type llmRetryBoundaryPayload struct {
	turnID           uint64
	inputMessages    []message.Message
	messages         []message.Message
	tailOverlayCount int
	reply            chan llmRetryBoundaryResult
}

type llmRetryBoundaryResult struct {
	messages []message.Message
	rebuild  bool
	err      error
}

// updateMainLLMRequestBeforeRetryRound pauses the retry worker after a
// cooling/backoff wait and before the first target attempt of the next round.
// The event loop admits queued next-request inputs only after their transcript
// and execution permit are durable, then extends this round's snapshot.
func (a *MainAgent) updateMainLLMRequestBeforeRetryRound(ctx context.Context, turnID uint64, messages []message.Message, tailOverlayCount int) ([]message.Message, error) {
	if a == nil {
		return messages, nil
	}
	if !a.started.Load() {
		// The pending queue is event-loop owned. A direct call without a running
		// event loop cannot safely consume it, so leave it for the normal drain.
		return messages, nil
	}
	payload := &llmRetryBoundaryPayload{
		turnID:           turnID,
		messages:         messages,
		tailOverlayCount: tailOverlayCount,
		reply:            make(chan llmRetryBoundaryResult, 1),
	}
	a.sendEvent(Event{
		Type:    EventLLMRetryBoundary,
		TurnID:  turnID,
		Payload: payload,
	})
	select {
	case result := <-payload.reply:
		if result.err != nil {
			return result.messages, result.err
		}
		messages = result.messages
		if result.rebuild {
			updated, err := a.rebuildMainLLMRequestMessages(messages)
			if err != nil {
				return nil, err
			}
			messages = updated
		}
		return messages, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("retry request update cancelled: %w", ctx.Err())
	case <-a.parentCtx.Done():
		return nil, fmt.Errorf("retry request update cancelled: %w", a.parentCtx.Err())
	case <-a.stoppingCh:
		return nil, context.Canceled
	}
}

func (a *MainAgent) handleLLMRetryBoundary(evt Event) {
	payload, ok := evt.Payload.(*llmRetryBoundaryPayload)
	if !ok || payload == nil || payload.reply == nil {
		log.Errorf("handleLLMRetryBoundary: invalid payload type=%T", evt.Payload)
		return
	}
	if a.turn == nil || payload.turnID == 0 || a.turn.ID != payload.turnID || evt.TurnID != payload.turnID {
		payload.reply <- llmRetryBoundaryResult{err: context.Canceled}
		return
	}
	if a.prepareInputDeliveryRequest(deliveryRequest{turnID: a.turn.ID, turnEpoch: a.turn.Epoch, retry: payload}) {
		return
	}
	messages := a.mergeBoundaryRequestInputs(payload.messages, payload.inputMessages, payload.tailOverlayCount)
	payload.reply <- llmRetryBoundaryResult{
		messages: messages,
		rebuild:  len(payload.inputMessages) > 0,
	}
}
