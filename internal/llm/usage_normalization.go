package llm

import (
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func normalizeResponseUsage(provider *ProviderConfig, resp *message.Response) {
	if provider == nil || resp == nil || resp.Usage == nil {
		return
	}
	usage := resp.Usage
	includesRead, includesWrite := defaultInputUsageSemantics(provider.Type())
	if usage.InputSemanticsKnown {
		includesRead = usage.InputIncludesCacheRead
		includesWrite = usage.InputIncludesCacheWrite
	}
	compat := provider.UsageCompat()
	readOverridden, writeOverridden := false, false
	if compat != nil {
		if compat.InputIncludesCacheRead != nil {
			includesRead = *compat.InputIncludesCacheRead
			readOverridden = true
		}
		if compat.InputIncludesCacheWrite != nil {
			includesWrite = *compat.InputIncludesCacheWrite
			writeOverridden = true
		}
	}

	inputTokens := max(usage.InputTokens, 0)
	cacheReadTokens := max(usage.CacheReadTokens, 0)
	cacheWriteTokens := max(usage.CacheWriteTokens, 0)
	// A component larger than the reported top-level input cannot already be
	// included in it. This only resolves unambiguous malformed/compatible
	// shapes; ambiguous third-party semantics require the compat override.
	if !readOverridden && includesRead && cacheReadTokens > inputTokens {
		includesRead = false
	}
	if !writeOverridden && includesWrite && cacheWriteTokens > inputTokens {
		includesWrite = false
	}
	deductions := 0
	if includesRead {
		deductions += cacheReadTokens
	}
	if includesWrite {
		deductions += cacheWriteTokens
	}
	uncachedInputTokens := max(inputTokens-deductions, 0)
	usage.InputTokens = uncachedInputTokens + cacheReadTokens
	usage.CacheReadTokens = cacheReadTokens
	usage.CacheWriteTokens = cacheWriteTokens
	usage.InputSemanticsKnown = true
	usage.InputIncludesCacheRead = true
	usage.InputIncludesCacheWrite = false
}

func defaultInputUsageSemantics(providerType string) (includesRead, includesWrite bool) {
	switch strings.TrimSpace(strings.ToLower(providerType)) {
	case config.ProviderTypeMessages:
		return false, false
	case config.ProviderTypeChatCompletions, config.ProviderTypeChatCompletionsLegacy, config.ProviderTypeResponses:
		return true, true
	case config.ProviderTypeGenerateContent:
		return true, false
	default:
		return true, false
	}
}
