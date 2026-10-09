package llm

import (
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// PromptCacheSettingsForModelRef returns the cache mode and TTL used by the configured pool
// target. Only Messages requests with caching enabled can incur the 1h write
// price; other transports use their ordinary cache-write price.
func (c *Client) PromptCacheSettingsForModelRef(ref string) (mode, ttl string) {
	if c == nil {
		return "", ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	target, ok := c.modelPoolTargetForRefLocked(ref)
	if !ok || target.ProviderConfig == nil || providerWireFamily(target.ProviderConfig) != modelcompat.WireFamilyAnthropic {
		return "", ""
	}
	tuning := tuningForPoolTarget(target)
	if c.nextTuning != nil {
		next, nextOK := c.modelPoolTargetForRefLocked("")
		if nextOK && modelRefWithVariant(next) == modelRefWithVariant(target) {
			tuning = mergeRequestTuning(tuning, *c.nextTuning)
		}
	}
	return tuning.Anthropic.PromptCacheMode, tuning.Anthropic.PromptCacheTTL
}

// PromptCacheMessageCountForModelRef returns the source prefix whose endpoint
// the renderer actually marks before transient turn overlays. Without an
// explicit boundary contract, keep the full request in cache diagnostics.
func (c *Client) PromptCacheMessageCountForModelRef(ref string, messages []message.Message, tailOverlayCount int) int {
	if c == nil {
		return len(messages)
	}
	if c.SupportsAnthropicPromptCache(ref) {
		if tailOverlayCount > 0 && tailOverlayCount < len(messages) {
			return len(messages) - tailOverlayCount
		}
		return len(messages)
	}
	c.mu.RLock()
	target, ok := c.modelPoolTargetForRefLocked(ref)
	supported := ok && providerWireFamily(target.ProviderConfig) == modelcompat.WireFamilyOpenAIResponses && modelcompat.SupportsResponsesCacheBreakpoints(target.ModelID)
	c.mu.RUnlock()
	if !supported {
		return len(messages)
	}
	// All durable user/tool input-text endings are marked. A trailing assistant
	// output or opaque native item cannot itself be a cache write endpoint.
	boundary := 0
	end := promptCacheDurableMessageCount(messages)
	for i, msg := range messages[:end] {
		if msg.Kind == message.KindTurnOverlay || msg.Kind == message.KindThinkingReplayPrefix {
			continue
		}
		if len(msg.MCPTools) > 0 {
			continue
		}
		if msg.Role != message.RoleUser && (msg.Role != message.RoleTool || msg.ToolCallID == "") {
			continue
		}
		if len(msg.Parts) == 0 {
			boundary = i + 1
			continue
		}
		for j := len(msg.Parts) - 1; j >= 0; j-- {
			part := msg.Parts[j]
			if part.Type == "image" || part.Type == "pdf" {
				break
			}
			if part.Text != "" {
				boundary = i + 1
				break
			}
		}
	}
	return boundary
}
