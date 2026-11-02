package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// hostedRequestProvider admits each wire attempt separately, releasing capacity
// before the client waits for key cooldown or round backoff.
type hostedRequestProvider struct {
	llm.Provider
	governor     *resourceGovernor
	providerName string
	retry        func() bool
	observe      func(*message.Response, error, time.Duration)
}

func (p hostedRequestProvider) CompleteStream(ctx context.Context, key, model, system string, messages []message.Message, defs []message.ToolDefinition, maxTokens int, tuning llm.RequestTuning, cb llm.StreamCallback) (*message.Response, error) {
	reservation, err := p.governor.acquireHostedLLM(ctx, p.providerName+"/"+model, p.retry != nil && p.retry())
	if err != nil {
		return nil, fmt.Errorf("acquire hosted LLM request capacity: %w", err)
	}
	if err := ctx.Err(); err != nil {
		reservation.release(true)
		return nil, err
	}
	requestCtx, dispatch := llm.WithRequestDispatch(ctx, reservation.markSent)
	defer func() { reservation.release(dispatch.NotSent()) }()
	started := time.Now()
	resp, err := p.Provider.CompleteStream(requestCtx, key, model, system, messages, defs, maxTokens, tuning, cb)
	if p.observe != nil && !dispatch.NotSent() {
		p.observe(resp, err, time.Since(started))
	}
	return resp, err
}

// ReleaseBodyCache forwards the optional body-cache release capability to the
// wrapped provider. The embedded Provider interface does not declare this
// method, so without the explicit forward the retry layer's assertion fails on
// the wrapper and the pinned body (plus its messages slice) survives the walk.
func (p hostedRequestProvider) ReleaseBodyCache(msgs *message.Message, msgsLen int) {
	if releaser, ok := p.Provider.(llm.BodyCacheReleaser); ok {
		releaser.ReleaseBodyCache(msgs, msgsLen)
	}
}
