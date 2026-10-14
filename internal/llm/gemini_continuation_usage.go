package llm

import (
	"context"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
)

// ContinuationUsage is one confirmed physical stream receipt, independent of preview.
type ContinuationUsage struct {
	ChainID     string
	Request     int
	ModelRef    string
	ServiceTier config.ServiceTier
	Outcome     string
	UsageState  string
	Usage       *message.TokenUsage
}
type continuationObserverKey struct{}
type continuationTargetKey struct{}
type continuationTarget struct {
	ref  string
	tier config.ServiceTier
}

// Presence is retained for required observations; optional zero-valued cache and
// thinking counters may be omitted by the wire. An output report needs at least
// one of candidatesTokenCount or thoughtsTokenCount, including explicit zero.
type geminiUsageMetadata struct {
	PromptTokenCount        *int `json:"promptTokenCount"`
	CandidatesTokenCount    *int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      *int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int  `json:"cachedContentTokenCount"`
}

type geminiUsageSnapshot struct {
	message.TokenUsage
	promptKnown, outputKnown bool
}

func geminiChunkUsage(chunk geminiStreamChunk) *geminiUsageSnapshot {
	u := chunk.UsageMetadata
	if u == nil {
		return nil
	}
	result := &geminiUsageSnapshot{promptKnown: u.PromptTokenCount != nil, outputKnown: u.CandidatesTokenCount != nil || u.ThoughtsTokenCount != nil}
	if u.PromptTokenCount != nil {
		result.InputTokens = *u.PromptTokenCount
	}
	if u.CandidatesTokenCount != nil {
		result.OutputTokens = *u.CandidatesTokenCount
	}
	if u.ThoughtsTokenCount != nil {
		result.ReasoningTokens = *u.ThoughtsTokenCount
		result.OutputTokens += *u.ThoughtsTokenCount
	}
	result.CacheReadTokens = u.CachedContentTokenCount
	result.InputSemanticsKnown, result.InputIncludesCacheRead = true, true
	return result
}

func (u *geminiUsageSnapshot) tokens() *message.TokenUsage {
	if u == nil {
		return nil
	}
	return u.TokenUsage.Clone()
}

// Confirmed counters remain useful lower bounds when a later checkpoint or
// terminal report omits a field. Completeness is tracked by the hop separately.
func mergeGeminiConfirmedUsage(current, previous *geminiUsageSnapshot) *geminiUsageSnapshot {
	if current == nil {
		return previous
	}
	if previous == nil {
		return current
	}
	merged := *current
	if !current.promptKnown && previous.promptKnown {
		merged.InputTokens, merged.promptKnown = previous.InputTokens, true
		merged.CacheReadTokens = max(current.CacheReadTokens, previous.CacheReadTokens)
	}
	if !current.outputKnown && previous.outputKnown {
		merged.OutputTokens, merged.ReasoningTokens, merged.outputKnown = previous.OutputTokens, previous.ReasoningTokens, true
	}
	return &merged
}

func (u *geminiUsageSnapshot) complete() bool {
	return u != nil && u.promptKnown && u.outputKnown
}

type geminiUsageLedger struct {
	aggregate   message.TokenUsage
	prompt      int
	promptKnown bool
	known       bool
}

func (l *geminiUsageLedger) settle(u *geminiUsageSnapshot, initial, complete bool) {
	if u == nil {
		l.known = false
		return
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.ReasoningTokens < 0 || u.CacheReadTokens < 0 {
		l.known = false
		return
	}
	if !complete || !u.outputKnown {
		l.known = false
	}
	if initial && !l.promptKnown && u.promptKnown {
		l.prompt = u.InputTokens
		l.promptKnown = true
	}
	l.aggregate.InputTokens += u.InputTokens
	l.aggregate.OutputTokens += u.OutputTokens
	l.aggregate.ReasoningTokens += u.ReasoningTokens
	l.aggregate.CacheReadTokens += u.CacheReadTokens
	l.aggregate.InputSemanticsKnown = true
	l.aggregate.InputIncludesCacheRead = true
}
func (l *geminiUsageLedger) usage() *message.TokenUsage {
	result := l.aggregate.Clone()
	result.Context = &message.ContextTokenUsage{Known: l.known && l.promptKnown, PromptTokens: l.prompt, OutputTokens: l.aggregate.OutputTokens}
	return result
}
func reportGeminiContinuationUsage(ctx context.Context, provider *ProviderConfig, chain string, request int, outcome, usageState string, usage *geminiUsageSnapshot) bool {
	observer, _ := ctx.Value(continuationObserverKey{}).(func(ContinuationUsage))
	if observer == nil {
		return false
	}
	target, _ := ctx.Value(continuationTargetKey{}).(continuationTarget)
	response := &message.Response{Usage: usage.tokens()}
	normalizeResponseUsage(provider, response)
	observer(ContinuationUsage{ChainID: chain, Request: request, ModelRef: target.ref, ServiceTier: target.tier, Outcome: outcome, UsageState: usageState, Usage: response.Usage})
	return true
}

func (r geminiHopResult) usageState() string {
	if r.Usage == nil {
		return contextdiag.Missing
	}
	if r.Err != nil || !r.UsageComplete || !r.Usage.promptKnown || !r.Usage.outputKnown {
		return contextdiag.Partial
	}
	return contextdiag.Reported
}
