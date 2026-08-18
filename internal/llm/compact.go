package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/keakon/golog/log"

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
		cb StreamCallback,
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
	for _, target := range c.modelPoolLocked() {
		if target.ProviderConfig == nil || !target.ProviderConfig.IsCodexOAuthTransport() {
			continue
		}
		if _, ok := target.ProviderImpl.(CompactProvider); ok {
			return true
		}
	}
	return false
}

// Compact executes a provider-specific compaction request. Callers should fall
// back to generic local summary compaction when this returns an error.
// The request walks the client's model pool so a failed compact on the primary
// target can retry on fallback providers without restarting the turn context.
func (c *Client) Compact(
	ctx context.Context,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
) (*message.Response, error) {
	if c == nil {
		return nil, fmt.Errorf("compact client is nil")
	}

	c.mu.Lock()
	pool := c.modelPoolLocked()
	if len(pool) == 0 {
		c.mu.Unlock()
		return nil, fmt.Errorf("compact provider config is nil")
	}
	startIdx := c.poolCursor
	if startIdx < 0 || startIdx >= len(pool) {
		startIdx = 0
	}
	orderedPool := append([]FallbackModel{pool[startIdx]}, rotatePoolAfterStart(pool, startIdx)...)
	systemPrompt := c.systemPrompt
	c.mu.Unlock()

	resp, runningOffset, err := c.compactWithFallback(ctx, orderedPool, systemPrompt, messages, tools, cb)
	if err != nil {
		return nil, err
	}
	runningTarget := orderedPool[runningOffset]
	selectedRef := modelRefWithVariant(orderedPool[0])
	runningRef := modelRefWithVariant(runningTarget)
	c.mu.Lock()
	c.poolCursor = (startIdx + runningOffset) % len(pool)
	c.lastCallStatus = CallStatus{
		SelectedModelRef:    selectedRef,
		RunningModelRef:     runningRef,
		RunningContextLimit: c.contextLimitForModelRefLocked(runningRef),
		RunningInputLimit:   c.inputLimitForModelRefLocked(runningRef),
		FallbackTriggered:   runningOffset > 0,
		ServiceTier:         effectiveServiceTierForTuning(tuningForPoolTarget(runningTarget), c.serviceTier),
	}
	c.mu.Unlock()
	if resp != nil && resp.Usage != nil {
		c.setLastInputTokens(resp.Usage.InputTokens)
	}
	return resp, nil
}

// compactWithFallback attempts the dedicated compact endpoint through the
// primary provider and, on failure, walks the snapshot fallback model pool so
// one compact request reuses the same per-turn state across retries.
func (c *Client) compactWithFallback(
	ctx context.Context,
	pool []FallbackModel,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	cb StreamCallback,
) (*message.Response, int, error) {
	var (
		lastErr        error
		requestStarted bool
	)
	for targetIndex, target := range pool {
		if target.ProviderConfig == nil || target.ProviderImpl == nil {
			continue
		}
		cp, ok := target.ProviderImpl.(CompactProvider)
		if !ok {
			continue
		}
		targetTuning := tuningForPoolTarget(target)
		keyCount := target.ProviderConfig.KeyCount()
		if keyCount == 0 {
			keyCount = 1
		}
		for keyAttempt := 0; keyAttempt < keyCount; keyAttempt++ {
			if err := ctx.Err(); err != nil {
				return nil, 0, fmt.Errorf("LLM request aborted: %w", err)
			}
			key, _, err := target.ProviderConfig.SelectKeyWithContext(ctx)
			if err != nil {
				lastErr = err
				if ctx.Err() != nil {
					return nil, 0, fmt.Errorf("LLM request aborted: %w", ctx.Err())
				}
				break
			}
			if requestStarted && cb != nil {
				cb(message.StreamDelta{
					Type: message.StreamDeltaStatus,
					Status: &message.StatusDelta{
						Type:   "retrying",
						Detail: "compact endpoint",
					},
				})
			}
			requestStarted = true
			resp, err := cp.Compact(ctx, key, target.ModelID, systemPrompt, messages, tools, target.MaxTokens, targetTuning, cb)
			if err == nil {
				target.ProviderConfig.MarkKeySuccess(key)
				target.ProviderConfig.WakeCodexRateLimitPolling()
				return resp, targetIndex, nil
			}
			lastErr = err
			log.Debugf("compact endpoint failed provider=%v model=%v key_id=%v error=%v", target.ProviderConfig.Name(), target.ModelID, keyLogID(key), err)
			cooldownResult := markKeyCooldown(ctx, target.ProviderConfig, key, err)
			if ctx.Err() != nil {
				return nil, 0, fmt.Errorf("LLM request aborted: %w", ctx.Err())
			}
			if c.isTerminalAPIStatusError(err) && !cooldownResult.oauthRefreshed {
				return nil, 0, err
			}
			if cooldownResult.oauthRefreshed {
				keyAttempt--
				continue
			}
			if !isRetriable(err) {
				break
			}
			if !cooldownResult.cooldownApplied && cooldownResult.refreshedKey == "" {
				target.ProviderConfig.MarkRecovering(key)
			}
		}
		if targetIndex+1 >= len(pool) || !c.compactFallbackEligible(ctx, lastErr) {
			return nil, 0, lastErr
		}
	}
	if lastErr != nil {
		return nil, 0, lastErr
	}
	return nil, 0, fmt.Errorf("compact endpoint failed for all providers")
}

// compactFallbackEligible reports whether a compact endpoint error may
// reasonably be retried on another provider: transient transport/5xx errors,
// timeouts, and 429 rate limits are eligible; permanent 4xx configuration
// errors are not.
func (c *Client) compactFallbackEligible(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.StatusCode == 400 {
		return false
	}
	if shouldFallback(err) {
		return true
	}
	if _, ok := errors.AsType[*APIError](err); ok {
		return false
	}
	return true
}
