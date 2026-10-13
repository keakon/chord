package llm

import (
	"context"

	"github.com/keakon/chord/internal/executiongate"
)

func (c *Client) SetExecutionGate(gate *executiongate.Gate, owner string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.executionGate, c.executionOwner = gate, owner
	c.mu.Unlock()
}

func (c *Client) ExecutionGate() *executiongate.Gate {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.executionGate
}

func (c *Client) executionContext(ctx context.Context) context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return executiongate.WithContext(ctx, c.executionGate, c.executionOwner)
}

// Production adapters admit at transport submission. Custom providers admit at
// their invocation boundary, since no lower transport boundary is available.
func beginCustomProvider(ctx context.Context, provider Provider) (*executiongate.Lease, error) {
	switch provider.(type) {
	case *OpenAIProvider, *AnthropicProvider, *ResponsesProvider, *GeminiProvider:
		return nil, executiongate.Check(ctx)
	default:
		prepareDiagnosticSubmission(ctx, "custom")
		lease, err := executiongate.Begin(ctx, executiongate.LLM)
		if err == nil {
			markRequestPrepared(ctx)
			markRequestSent(ctx)
		}
		return lease, err
	}
}
