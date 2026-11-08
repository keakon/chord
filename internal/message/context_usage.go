package message

// fullPromptTokens normalizes a usage report to the full prompt size across
// wire accounting conventions. Live responses already arrive canonicalized by
// the LLM client (input includes cache reads and excludes cache writes — see
// TokenUsage), so on that path only the cache-write add-back applies; the
// other branches are defense in depth for raw provider shapes, so a path that
// bypasses that normalization cannot blind a compaction threshold or byte
// calibration. Anthropic-style wires report cache-read and cache-write
// prefixes outside input_tokens (flags false — add them back), and some
// OpenAI-compatible relays report only the uncached remainder even though the
// wire contract says input includes the cached prefix — when input is smaller
// than a slice it supposedly contains, trust the sum instead.
func fullPromptTokens(usage TokenUsage) int {
	full := usage.InputTokens
	if !usage.InputIncludesCacheRead || usage.InputTokens < usage.CacheReadTokens {
		full += usage.CacheReadTokens
	}
	if !usage.InputIncludesCacheWrite || usage.InputTokens < usage.CacheWriteTokens {
		full += usage.CacheWriteTokens
	}
	return full
}

// hasUsageReport reports whether a TokenUsage carries any provider observation.
// An all-zero report is treated as missing usage, never as a real zero-size
// observation: no provider bills a real request at zero input and zero output.
func hasUsageReport(usage TokenUsage) bool {
	if fullPromptTokens(usage) > 0 {
		return true
	}
	return usage.OutputTokens > 0 || usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 || usage.ReasoningTokens > 0
}

// ContextPromptTokens separates window observations from multi-request billing.
func ContextPromptTokens(usage TokenUsage) (int, bool) {
	if usage.Context != nil {
		if !usage.Context.Known {
			return 0, false
		}
		return usage.Context.PromptTokens, true
	}
	return fullPromptTokens(usage), hasUsageReport(usage)
}
