package agent

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// A receipt must reach canonical history and its journal acknowledgement before
// the next request checks for unresolved execution. Request goroutines wait;
// the event loop remains free to drain input and process receipt events.
type nativeReceiptPersistence struct {
	done chan struct{}
	err  error // published by closing done
}

func newNativeReceiptPersistence(msg message.Message) *nativeReceiptPersistence {
	if msg.NativeTools == nil || msg.NativeTools.OutcomeUnknown || len(msg.NativeTools.RequestIDs) == 0 {
		return nil
	}
	return &nativeReceiptPersistence{done: make(chan struct{})}
}

func (p *nativeReceiptPersistence) finish(err error) {
	if p != nil {
		p.err = err
		close(p.done)
	}
}

func nativeReceiptPreflight(check func(context.Context) error, pending *nativeReceiptPersistence) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pending != nil {
			select {
			case <-pending.done:
				if pending.err != nil {
					return fmt.Errorf("persist native receipt: %w", pending.err)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return check(ctx)
	}
}

func (s *SubAgent) publishNativeReceipt(msg message.Message, err error) {
	if err == nil && msg.NativeTools != nil && s.parent != nil {
		s.parent.sendEvent(Event{Type: EventNativeReceipt, Payload: nativeReceiptPayload{epoch: s.sessionEpoch, agentID: s.instanceID, receipt: msg.NativeTools}})
	}
}
