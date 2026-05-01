package llm

import (
	"context"
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// CompactProvider is an optional provider capability for dedicated compaction
// endpoints.
type CompactProvider interface {
	Compact(
		ctx context.Context,
		apiKey string,
		model string,
		systemPrompt string,
		messages []message.Message,
		tools []message.ToolDefinition,
		maxTokens int,
		tuning RequestTuning,
	) (*message.Response, error)
}

// SupportsCompactEndpoint reports whether the current client is backed by a
// provider implementation that can use a dedicated compact endpoint. Today this
// is only enabled for the official Codex preset.
func (c *Client) SupportsCompactEndpoint() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil || !c.provider.IsCodexOAuthTransport() {
		return false
	}
	_, ok := c.providerImpl.(CompactProvider)
	return ok
}

// Compact executes a provider-specific compaction request. Callers should fall
// back to generic local summary compaction when this returns an error.
func (c *Client) Compact(
	ctx context.Context,
	messages []message.Message,
	tools []message.ToolDefinition,
) (*message.Response, error) {
	if c == nil {
		return nil, fmt.Errorf("compact client is nil")
	}

	c.mu.RLock()
	provider := c.provider
	providerImpl := c.providerImpl
	modelID := c.modelID
	maxTokens := c.maxTokens
	tuning := c.tuning
	systemPrompt := c.systemPrompt
	c.mu.RUnlock()

	if provider == nil {
		return nil, fmt.Errorf("compact provider config is nil")
	}
	cp, ok := providerImpl.(CompactProvider)
	if !ok {
		return nil, fmt.Errorf("provider does not support compact endpoint")
	}

	apiKey, keySwitched, err := provider.SelectKeyWithContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("LLM request aborted: %w", ctx.Err())
		}
		return nil, err
	}
	if keySwitched {
		provider.ClearInlineDisplayRateLimitSnapshot()
	}

	resp, err := cp.Compact(ctx, apiKey, modelID, systemPrompt, messages, tools, maxTokens, tuning)
	if err != nil {
		return nil, err
	}
	provider.MarkKeySuccess(apiKey)
	provider.WakeCodexRateLimitPolling()
	if resp != nil && resp.Usage != nil {
		c.setLastInputTokens(resp.Usage.InputTokens)
	}
	return resp, nil
}
