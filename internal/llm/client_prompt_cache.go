package llm

import "github.com/keakon/chord/internal/modelcompat"

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
