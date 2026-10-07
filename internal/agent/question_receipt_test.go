package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuestionReplyDoesNotWaitForReceiptConsumer(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		name := "full_reply_channel"
		if cancelled {
			name = "cancelled_caller"
		}
		t.Run(name, func(t *testing.T) {
			c := &questionCommand{
				operation: QuestionOperation{Operation: questionOpWait, OperationID: "wait"},
				reply:     make(chan QuestionReceipt, 1),
			}
			first := QuestionReceipt{Accepted: true, Version: 1}
			reply := first
			if cancelled {
				a := &MainAgent{eventCh: make(chan Event, 2), stoppingCh: make(chan struct{})}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := a.requestQuestion(ctx, c); !errors.Is(err, context.Canceled) {
					t.Fatalf("requestQuestion error = %v, want context cancellation", err)
				}
			} else {
				c.reply <- first
				reply.Version = 2
			}
			done := make(chan struct{})
			go func() {
				questionReply(c, reply)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				// Release a regressed blocking send before failing the test.
				<-c.reply
				<-done
				t.Fatal("question reply waited for a receipt consumer")
			}
			select {
			case got := <-c.reply:
				if !got.Accepted || got.Version != first.Version {
					t.Fatalf("buffered receipt = %+v, want %+v", got, first)
				}
			default:
				t.Fatal("first receipt was lost")
			}
		})
	}
}
