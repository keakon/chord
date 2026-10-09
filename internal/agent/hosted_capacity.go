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
	observe      func(*message.Response, error, time.Duration)
}

func (p hostedRequestProvider) CompleteStream(ctx context.Context, key, model, system string, messages []message.Message, defs []message.ToolDefinition, maxTokens int, tuning llm.RequestTuning, cb llm.StreamCallback) (*message.Response, error) {
	release, err := p.governor.acquireLLM(ctx, p.providerName+"/"+model)
	if err != nil {
		return nil, fmt.Errorf("acquire hosted LLM request capacity: %w", err)
	}
	defer release()
	started := time.Now()
	resp, err := p.Provider.CompleteStream(ctx, key, model, system, messages, defs, maxTokens, tuning, cb)
	if p.observe != nil {
		p.observe(resp, err, time.Since(started))
	}
	return resp, err
}
