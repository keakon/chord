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

// PromptCacheMessagesForModelRef projects the source prefix through the actual
// cache endpoint, including a partial multimodal message. The projection owns
// any shortened message; canonical history remains unchanged. Without an
// explicit boundary contract, diagnostics compare the full request.
func (c *Client) PromptCacheMessagesForModelRef(ref string, messages []message.Message, tailOverlayCount int) []message.Message {
	if c == nil {
		return messages
	}
	c.mu.RLock()
	target, ok := c.modelPoolTargetForRefLocked(ref)
	c.mu.RUnlock()
	if !ok {
		return messages
	}
	return promptCacheMessagesForTarget(providerWireFamily(target.ProviderConfig), tuningForPoolTarget(target), responsesCacheBreakpointsEnabled(target.ProviderConfig, target.ModelID), messages, tailOverlayCount)
}

// responsesCacheBreakpointsEnabled reports whether a target emits explicit
// Responses cache breakpoints: the model generation must support them and the
// resolved compat must not opt out. The request builder and the cache
// projection share this so a boundary Chord no longer marks is not still
// reported as a cache expectation.
func responsesCacheBreakpointsEnabled(provider *ProviderConfig, model string) bool {
	if !modelcompat.SupportsResponsesCacheBreakpoints(model) {
		return false
	}
	if provider == nil {
		return true
	}
	rc := provider.ResponsesCompat(model)
	return rc == nil || compatBool(rc.SendPromptCacheBreakpoint, true)
}

func promptCacheMessagesForTarget(family string, tuning RequestTuning, cacheBreakpoints bool, messages []message.Message, tailOverlayCount int) []message.Message {
	if family == modelcompat.WireFamilyAnthropic && tuning.Anthropic.PromptCacheMode == "explicit" {
		end := anthropicCacheDurableMessageCount(messages)
		if tailOverlayCount > 0 && tailOverlayCount < len(messages) {
			end = min(end, len(messages)-tailOverlayCount)
		}
		return messages[:end]
	}
	if family != modelcompat.WireFamilyOpenAIResponses || !cacheBreakpoints {
		return messages
	}
	// All durable user/tool input-text endings are marked. A trailing assistant
	// output or opaque native item cannot itself be a cache write endpoint.
	boundary := 0
	partEnd := 0
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
		if endpoint := responsesCacheTextPartEnd(msg); endpoint >= 0 {
			boundary, partEnd = i+1, endpoint
		}
	}
	if boundary == 0 {
		return messages[:0]
	}
	last := messages[boundary-1]
	if len(last.Parts) == partEnd {
		return messages[:boundary]
	}
	projected := append([]message.Message(nil), messages[:boundary]...)
	if partEnd == 0 {
		// User fallback is empty text; tool fallback retains its Content.
		if last.Role == message.RoleUser {
			projected[boundary-1].Content = ""
		}
		projected[boundary-1].Parts = nil
	} else {
		projected[boundary-1].Parts = append([]message.ContentPart(nil), last.Parts[:partEnd]...)
		projected[boundary-1].Content = ""
	}
	return projected
}
