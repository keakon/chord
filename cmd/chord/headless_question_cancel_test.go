package main

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

type cancellingQuestionBackend struct {
	mockBackend
	ctx context.Context
}

func (b *cancellingQuestionBackend) ApplyQuestionOperation(ctx context.Context, _ agent.QuestionOperation) (agent.QuestionReceipt, error) {
	b.ctx = ctx
	return agent.QuestionReceipt{}, ctx.Err()
}

func TestHeadlessQuestionUsesCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	backend := &cancellingQuestionBackend{}
	out := newTestOut()
	w := out.writer()
	w.commandCtx = ctx
	handleHeadlessQuestionCommand(headlessCommand{Action: agent.QuestionOpAnswer, OperationID: "answer", RequestID: "choice"}, backend, w)
	if backend.ctx != ctx || backend.ctx.Err() != context.Canceled {
		t.Fatal("question operation did not inherit command cancellation")
	}
}
