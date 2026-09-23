// Package ctxmgr provides the context manager — the single source of truth
// for the conversation message list. All access is thread-safe.
package ctxmgr

import (
	"fmt"
	"slices"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// Manager holds the conversation state: system prompt, message history,
// token stats, and compression settings.
type Manager struct {
	mu                       sync.RWMutex
	systemPrompt             message.Message
	systemPromptBytes        int
	systemPromptContextBytes int
	messages                 []message.Message
	// declaredToolCallIDs indexes the tool call IDs assistant messages in the
	// history declare, so AnyAssistantDeclaresToolCallID answers O(1) instead
	// of scanning the full history per tool result. nil until first needed;
	// guarded by mu.
	declaredToolCallIDs map[string]struct{}
	payloadBytes        int
	contextBytes        int
	// imagePayloadBytes and imageEstimateTokens mirror the image parts inside
	// messages. Provider image billing is per image rather than per base64
	// byte, so the token estimator subtracts these payload bytes from its byte
	// denominator and charges the per-image allowance instead. payloadBytes and
	// contextBytes keep counting image bytes: they measure the request surface
	// for display and byte budgets, not token cost.
	imagePayloadBytes      int
	imageEstimateTokens    int
	lastInputTokens        int // full prompt size for compaction thresholds and input-budget displays (observed only)
	lastTotalContextTokens int // post-response context baseline (full prompt + output, observed only)
	// observedValid marks that the latest main response carried provider usage.
	// frozenEstimateTokens/frozenValid carry the single frozen estimate allowed
	// when the latest response missed usage but calibration samples exist. The
	// frozen value is computed once at that response's end and never grows with
	// later appends. When neither holds the state is unknown: 0, which is also
	// what a session that has not called the model yet shows, and no
	// usage-driven trigger.
	frozenEstimateTokens    int
	observedValid           bool
	frozenValid             bool
	calibrationInputTokens  int
	calibrationContextBytes int
	// calibrationImageTokens tracks image allowance at the observed baseline,
	// not the provider's actual image charge.
	calibrationImageTokens int
	// usageCalibration keeps a bounded window of (text-share prompt tokens,
	// estimator prompt bytes) samples from completed LLM calls; the median
	// tokens/bytes ratio is the usage-calibrated estimator. Both sides exclude
	// image parts, whose provider cost is per image rather than per byte. The
	// window deliberately survives session switches and context rewrites — the
	// ratio stays valid while the stale size fields above are cleared — so a
	// model switch keeps the last valid calibration instead of cold-starting.
	usageCalibration []calibrationSample
	// calibratedRatioCache caches the median clamped tokens/bytes ratio of the
	// calibration window. It is recomputed under the write lock whenever
	// UpdateFromUsage changes the window, so the estimate read paths only read
	// a float instead of re-sorting the window per message. 0 means no usable
	// sample has been recorded yet.
	calibratedRatioCache float64
	maxTokens            int
	inputBudget          int
	inputBudgetReserved  int
	// tokenBudgetsEpoch counts token-budget value changes (see SetTokenBudgets);
	// it identifies a usage-baseline window together with the session epoch.
	tokenBudgetsEpoch uint64

	threshold float64 // fraction of usable input budget that triggers compaction; <= 0 disables automatic compaction

	stats message.TokenUsage
}

// NewManager creates a Manager with the given token budget and compression
// threshold. A threshold <= 0 disables automatic compaction.
//
//   - maxTokens: the model's context window size (in tokens).
//   - threshold: fraction (0–1) of the input budget at which to trigger compaction.
func NewManager(maxTokens int, threshold float64) *Manager {
	return NewManagerWithInputBudget(maxTokens, 0, 0, threshold)
}

// NewManagerWithInputBudget creates a Manager with separate total-context,
// input-side budget, and reserved input headroom. If inputBudget <= 0,
// maxTokens is used as the input budget.
// A threshold <= 0 disables automatic compaction.
func NewManagerWithInputBudget(maxTokens, inputBudget, reservedInput int, threshold float64) *Manager {
	if inputBudget <= 0 {
		inputBudget = maxTokens
	}
	if reservedInput < 0 {
		reservedInput = 0
	}
	return &Manager{
		maxTokens:           maxTokens,
		inputBudget:         inputBudget,
		inputBudgetReserved: reservedInput,
		threshold:           threshold,
	}
}

// SetSystemPrompt replaces the system prompt.
func (m *Manager) SetSystemPrompt(msg message.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.systemPrompt = msg
	m.systemPromptBytes = MessagePayloadBytes([]message.Message{msg})
	m.systemPromptContextBytes = messageContextBytes([]message.Message{msg})
}

// SystemPrompt returns the current system prompt.
func (m *Manager) SystemPrompt() message.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.systemPrompt
}

// SetMaxTokens updates the context window size (token budget). Thread-safe.
// This is used when switching to a model with a different context limit.
func (m *Manager) SetMaxTokens(n int) {
	m.SetTokenBudgets(n, 0, 0)
}

// SetTokenBudgets updates total context window, input-side budget, and reserved
// input headroom. If inputBudget <= 0, maxTokens is used as the input budget.
// The token-budgets epoch increments only when a value actually changes, so it
// counts model/provider/budget switches (the budget_epoch component of the
// context-pressure reminder claim) rather than every per-response call.
func (m *Manager) SetTokenBudgets(maxTokens, inputBudget, reservedInput int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inputBudget <= 0 {
		inputBudget = maxTokens
	}
	if reservedInput < 0 {
		reservedInput = 0
	}
	if maxTokens != m.maxTokens || inputBudget != m.inputBudget || reservedInput != m.inputBudgetReserved {
		m.maxTokens = maxTokens
		m.inputBudget = inputBudget
		m.inputBudgetReserved = reservedInput
		m.tokenBudgetsEpoch++
	}
}

// TokenBudgetsEpoch returns the number of token-budget value changes since the
// manager was created. Combined with the session epoch it identifies a usage
// baseline window for the context-pressure reminder claim.
func (m *Manager) TokenBudgetsEpoch() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tokenBudgetsEpoch
}

// SetThreshold updates the automatic-compaction threshold fraction. A value
// <= 0 disables automatic compaction. The epoch bumps only when the value
// actually changes, so a per-model threshold switch (which also re-derives the
// reminder baseline) starts a fresh claim window like any other budget change.
func (m *Manager) SetThreshold(threshold float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if threshold != m.threshold {
		m.threshold = threshold
		m.tokenBudgetsEpoch++
	}
}

// CalibratedRatio returns the current usage-calibrated tokens-per-byte median
// (0 when no usable sample exists). Callers that must keep two estimates on
// the same calibration (e.g. a compaction preflight and its post-export
// re-check) should snapshot this once and use EstimateMessagesTokensWithRatio
// on both sides instead of reading the live ratio twice.
func (m *Manager) CalibratedRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.calibratedRatioCache
}

// GetMaxTokens returns the context window size (token budget). Thread-safe.
func (m *Manager) GetMaxTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxTokens
}

// GetInputBudget returns the configured input-side token budget used for
// auto-compaction before reserved headroom is applied.
func (m *Manager) GetInputBudget() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.inputBudget > 0 {
		return m.inputBudget
	}
	return m.maxTokens
}

// GetUsableInputBudget returns the input-side budget after subtracting reserved
// headroom for tokenizer drift, summary output, and provider overhead.
func (m *Manager) GetUsableInputBudget() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	budget := m.inputBudget
	if budget <= 0 {
		budget = m.maxTokens
	}
	budget -= m.inputBudgetReserved
	if budget < 0 {
		return 0
	}
	return budget
}

// IsAutoCompactEnabled reports whether automatic compaction is enabled.
func (m *Manager) IsAutoCompactEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.threshold > 0
}

// Threshold returns the configured compression threshold fraction (0–1).
func (m *Manager) Threshold() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.threshold
}

// Append adds a message to the conversation history. It is safe to call from
// multiple goroutines.
func (m *Manager) Append(msg message.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trackToolCallIDsLocked(msg)
	m.messages = append(m.messages, msg)
	m.payloadBytes += MessagePayloadBytes([]message.Message{msg})
	m.contextBytes += messageContextBytes([]message.Message{msg})
	imagePayloadBytes, imageTokens := imagePartAccounting([]message.Message{msg})
	m.imagePayloadBytes += imagePayloadBytes
	m.imageEstimateTokens += imageTokens
}

// trackToolCallIDsLocked records the tool call IDs an appended assistant
// message declares. Callers hold m.mu.
func (m *Manager) trackToolCallIDsLocked(msg message.Message) {
	if len(msg.ToolCalls) == 0 {
		return
	}
	if m.declaredToolCallIDs == nil {
		m.declaredToolCallIDs = make(map[string]struct{})
	}
	for _, tc := range msg.ToolCalls {
		if tc.ID != "" {
			m.declaredToolCallIDs[tc.ID] = struct{}{}
		}
	}
}

// rebuildToolCallIDIndexLocked recomputes the declared-ID index after a
// wholesale message-list rewrite. Callers hold m.mu.
func (m *Manager) rebuildToolCallIDIndexLocked() {
	m.declaredToolCallIDs = nil
	for _, msg := range m.messages {
		m.trackToolCallIDsLocked(msg)
	}
}

// DropLastMessage removes the last message from the conversation history.
// Safe to call from multiple goroutines.
func (m *Manager) DropLastMessage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.messages); n > 0 {
		m.payloadBytes -= MessagePayloadBytes(m.messages[n-1:])
		m.contextBytes -= messageContextBytes(m.messages[n-1:])
		imagePayloadBytes, imageTokens := imagePartAccounting(m.messages[n-1:])
		m.imagePayloadBytes -= imagePayloadBytes
		m.imageEstimateTokens -= imageTokens
		m.messages = m.messages[:n-1]
		m.rebuildToolCallIDIndexLocked()
	}
}

// DropLastMessages removes the last n messages from the conversation history.
// Used when a turn is cancelled after an assistant message with tool calls was
// appended and some (or all) tool results were already appended, so the next
// request does not send function_calls without corresponding function_call_output (API 400).
// No-op if n <= 0 or if there are fewer than n messages. Safe to call from multiple goroutines.
func (m *Manager) DropLastMessages(n int) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.messages) < n {
		n = len(m.messages)
	}
	m.payloadBytes -= MessagePayloadBytes(m.messages[len(m.messages)-n:])
	m.contextBytes -= messageContextBytes(m.messages[len(m.messages)-n:])
	imagePayloadBytes, imageTokens := imagePartAccounting(m.messages[len(m.messages)-n:])
	m.imagePayloadBytes -= imagePayloadBytes
	m.imageEstimateTokens -= imageTokens
	m.messages = m.messages[:len(m.messages)-n]
	// The declared-ID index must track the removal: a dropped assistant message
	// no longer declares its tool calls, and a stale entry would keep
	// AnyAssistantDeclaresToolCallID true for a call the history no longer
	// contains (letting a synthetic result persist against a strict API).
	m.rebuildToolCallIDIndexLocked()
}

// Snapshot returns a copy of the current message history. The returned slice
// is safe to mutate without affecting the Manager's internal state.
func (m *Manager) Snapshot() []message.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]message.Message, len(m.messages))
	copy(out, m.messages)
	return out
}

// RestoreMessages replaces the entire message history with msgs.
// When msgs is nil or empty (e.g. plan execution or role switch with clear history),
// or orphan-tool repair removes every message, the size observation (observed
// and frozen) is reset to unknown so context indicators stay empty until the
// next LLM call refreshes the tracked usage. The calibration ratio window
// survives: only the single-sample size fields are cleared.
//
// Orphan tool results (tool_call_id not declared by any preceding assistant message)
// are dropped so resumed sessions and compaction commits stay valid for strict APIs.
func (m *Manager) RestoreMessages(msgs []message.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repaired, dropped := message.RepairOrphanToolResults(msgs)
	if dropped > 0 {
		log.Warnf("ctxmgr: removed orphan tool messages when restoring history dropped=%v", dropped)
	}
	replaced := make([]message.Message, len(repaired))
	copy(replaced, repaired)
	m.messages = replaced
	m.payloadBytes = MessagePayloadBytes(replaced)
	m.contextBytes = messageContextBytes(replaced)
	m.imagePayloadBytes, m.imageEstimateTokens = imagePartAccounting(replaced)
	m.rebuildToolCallIDIndexLocked()
	m.calibrationInputTokens = 0
	m.calibrationContextBytes = 0
	m.calibrationImageTokens = 0
	if len(repaired) == 0 {
		m.lastInputTokens = 0
		m.lastTotalContextTokens = 0
		m.observedValid = false
		m.frozenEstimateTokens = 0
		m.frozenValid = false
	}
}

// RepairOrphanToolMessagesInPlace removes tool messages that have no matching
// assistant tool_call in the current history. Returns how many were removed.
func (m *Manager) RepairOrphanToolMessagesInPlace() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	repaired, n := message.RepairOrphanToolResults(m.messages)
	if n == 0 {
		return 0
	}
	m.messages = repaired
	m.payloadBytes = MessagePayloadBytes(repaired)
	m.contextBytes = messageContextBytes(repaired)
	m.imagePayloadBytes, m.imageEstimateTokens = imagePartAccounting(repaired)
	m.calibrationInputTokens = 0
	m.calibrationContextBytes = 0
	m.calibrationImageTokens = 0
	// Every other path that replaces m.messages wholesale rebuilds the index.
	// Today the repair only drops tool-role messages, so the declared-call set
	// is unchanged — but that is a property of RepairOrphanToolResults, not of
	// this function, and the index is the one piece of state that goes silently
	// wrong rather than loudly.
	m.rebuildToolCallIDIndexLocked()
	if len(repaired) == 0 {
		m.lastInputTokens = 0
		m.lastTotalContextTokens = 0
		m.observedValid = false
		m.frozenEstimateTokens = 0
		m.frozenValid = false
	}
	return n
}

// ComputeSafeKeepBoundary returns SafeKeepBoundary applied to a snapshot of the
// current messages. This is used by the agent layer to compute a safe split
// point for async compaction without needing to hold the lock across the call.
func (m *Manager) ComputeSafeKeepBoundary(rawBoundary int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return SafeKeepBoundary(m.messages, rawBoundary)
}

// ScanBackward visits messages newest-first under the read lock until visit
// returns true. Targeted backward lookups (tool provenance, LSP review
// attribution, skill attribution) use this instead of Snapshot, which copies
// the whole history on every tool result. The visitor must not retain or
// mutate the message pointer; clone whatever escapes the callback.
func (m *Manager) ScanBackward(visit func(msg *message.Message) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.messages) - 1; i >= 0; i-- {
		if visit(&m.messages[i]) {
			return
		}
	}
}

// AnyAssistantDeclaresToolCallID reports whether any assistant message in the
// current history lists the given tool call id in ToolCalls.
func (m *Manager) AnyAssistantDeclaresToolCallID(callID string) bool {
	if callID == "" {
		return false
	}
	m.mu.RLock()
	if m.declaredToolCallIDs != nil {
		_, ok := m.declaredToolCallIDs[callID]
		m.mu.RUnlock()
		return ok
	}
	for i := range m.messages {
		if m.messages[i].Role != message.RoleAssistant {
			continue
		}
		for _, tc := range m.messages[i].ToolCalls {
			if tc.ID == callID {
				// First call after a rewrite: build the index, then answer
				// from it. The RLock→Lock upgrade drops the read lock first
				// — the index is content-derived, so racing appends only
				// add entries.
				m.mu.RUnlock()
				m.mu.Lock()
				m.rebuildToolCallIDIndexLocked()
				m.mu.Unlock()
				return true
			}
		}
	}
	m.mu.RUnlock()
	return false
}

// RestoreStats resets cumulative token statistics to the given values.
// Used when resuming a session so GetStats reflects that session's history.
func (m *Manager) RestoreStats(usage message.TokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = usage
}

const (
	calibrationWindowSize = 12
	// Sanity bounds for a tokens/bytes ratio: 0.05 (low-density repetitive or
	// base64-heavy text) to 1.0 (dense CJK). A single pathological sample
	// cannot drag the estimate outside this band.
	calibrationRatioMin = 0.05
	calibrationRatioMax = 1.0
)

// calibrationSample pairs the text-share prompt tokens with the estimator byte
// surface (image payloads excluded) of one completed LLM call.
type calibrationSample struct {
	tokens int
	bytes  int
}

// fullPromptTokens normalizes a usage report to the full prompt size across
// wire accounting conventions. Live responses already arrive canonicalized by
// the LLM client (input includes cache reads and excludes cache writes — see
// message.TokenUsage), so on that path only the cache-write add-back applies;
// the other branches are defense in depth for raw provider shapes, so a path
// that bypasses that normalization cannot blind the compaction threshold and
// the byte calibration below. Anthropic-style wires report cache-read and
// cache-write prefixes outside input_tokens (flags false — add them back), and
// some OpenAI-compatible relays report only the uncached remainder even though
// the wire contract says input includes the cached prefix — when input is
// smaller than a slice it supposedly contains, trust the sum instead.
func fullPromptTokens(usage message.TokenUsage) int {
	full := usage.InputTokens
	if !usage.InputIncludesCacheRead || usage.InputTokens < usage.CacheReadTokens {
		full += usage.CacheReadTokens
	}
	if !usage.InputIncludesCacheWrite || usage.InputTokens < usage.CacheWriteTokens {
		full += usage.CacheWriteTokens
	}
	return full
}

// FullPromptTokens normalizes a usage report to the full prompt size across
// wire accounting conventions (see fullPromptTokens). Providers must be compared
// through this normalization: cache reads are already inside input on most
// wires, so adding them again double-counts.
func FullPromptTokens(usage message.TokenUsage) int {
	return fullPromptTokens(usage)
}

// ContextUsageState is the observation state behind the context gauge and the
// auto-compaction trigger. observed = latest main response carried usage;
// estimated = latest missed usage but a frozen calibration estimate exists;
// unknown = no usable sample: 0, never triggers on usage.
type ContextUsageState string

const (
	ContextUsageObserved  ContextUsageState = "observed"
	ContextUsageEstimated ContextUsageState = "estimated"
	ContextUsageUnknown   ContextUsageState = "unknown"
)

// hasUsageReport reports whether a TokenUsage carries any provider observation.
// An all-zero report is treated as missing usage, never as a real zero-size
// observation: no provider bills a real request at zero input and zero output.
func hasUsageReport(usage message.TokenUsage) bool {
	if fullPromptTokens(usage) > 0 {
		return true
	}
	return usage.OutputTokens > 0 || usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 || usage.ReasoningTokens > 0
}

// computeFrozenEstimateLocked computes the single frozen estimate allowed when a
// response misses usage: one absolute size for the just-finished request, using
// the request-equivalent byte snapshot at this moment and historical samples.
// It never grows with later appends; callers store the result once. Must hold
// the write lock. Returns 0 when no applicable sample exists (unknown).
//
// The byte denominator is the full durable context, the same accounting the
// calibrated ratio and the planning estimators use — deliberately not the
// request-level reduced surface the samples were reported against (see
// EstimateMessagesTokensCalibrated). The two conventions differ by tool
// definitions, overlays, and request reduction; scaling a sample by a surface
// it never measured would make the frozen reading and the planning estimates
// disagree about the same context.
func (m *Manager) computeFrozenEstimateLocked() int {
	contextBytes := m.estimatorContextBytesLocked()
	if m.calibrationInputTokens > 0 && m.calibrationImageTokens > 0 {
		return m.estimateMixedSampleGrowthLocked(contextBytes)
	}
	if m.calibrationInputTokens > 0 && m.calibrationContextBytes > 0 {
		sampleTokens := m.calibrationInputTokens
		if contextBytes <= 0 {
			return m.calibrationInputTokens
		}
		if contextBytes <= m.calibrationContextBytes {
			return sampleTokens + m.imageEstimateTokens
		}
		return int((int64(sampleTokens)*int64(contextBytes))/int64(m.calibrationContextBytes)) + m.imageEstimateTokens
	}
	if m.calibratedRatioCache > 0 && contextBytes > 0 {
		return max(int(float64(contextBytes)*m.calibratedRatioCache), 1) + m.imageEstimateTokens
	}
	return 0
}

// UpdateFromUsage accumulates token usage statistics from an API response.
// With provider usage it records the observed baseline (full normalized prompt
// plus generated output) and refreshes calibration; without usage it freezes a
// single estimate for the just-finished request when samples exist, otherwise
// it records unknown. The frozen value never grows with later appends: only the
// next response or an explicit invalidation changes it.
func (m *Manager) UpdateFromUsage(usage message.TokenUsage) {
	m.mu.Lock()
	m.stats.InputTokens += usage.InputTokens
	m.stats.OutputTokens += usage.OutputTokens
	m.stats.CacheReadTokens += usage.CacheReadTokens
	m.stats.CacheWriteTokens += usage.CacheWriteTokens
	m.stats.ReasoningTokens += usage.ReasoningTokens
	if !hasUsageReport(usage) {
		frozen := m.computeFrozenEstimateLocked()
		m.lastInputTokens = 0
		m.lastTotalContextTokens = 0
		m.observedValid = false
		if frozen > 0 {
			m.frozenEstimateTokens = frozen
			m.frozenValid = true
		} else {
			m.frozenEstimateTokens = 0
			m.frozenValid = false
		}
		m.mu.Unlock()
		return
	}
	fullPrompt := fullPromptTokens(usage)
	m.lastInputTokens = fullPrompt
	m.lastTotalContextTokens = fullPrompt + usage.OutputTokens
	m.observedValid = true
	m.frozenEstimateTokens = 0
	m.frozenValid = false
	if fullPrompt > 0 {
		contextBytes := m.estimatorContextBytesLocked()
		if contextBytes > 0 || m.imageEstimateTokens > 0 {
			m.calibrationInputTokens = fullPrompt
			m.calibrationContextBytes = contextBytes
			m.calibrationImageTokens = m.imageEstimateTokens
			if m.imageEstimateTokens == 0 && contextBytes > 0 {
				m.usageCalibration = append(m.usageCalibration, calibrationSample{tokens: fullPrompt, bytes: contextBytes})
				if len(m.usageCalibration) > calibrationWindowSize {
					m.usageCalibration = m.usageCalibration[len(m.usageCalibration)-calibrationWindowSize:]
				}
				m.calibratedRatioCache = m.computeCalibratedRatioLocked()
			}
		}
	}
	m.mu.Unlock()
}

// NoteMissingUsage records that the latest main response carried no provider
// usage. It freezes one estimate when calibration samples exist, otherwise it
// records unknown (display 0, never trigger).
func (m *Manager) NoteMissingUsage() {
	m.UpdateFromUsage(message.TokenUsage{})
}

// GetStats returns the cumulative token usage.
func (m *Manager) GetStats() message.TokenUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

// LastInputTokens returns the observed input token count from the most recent
// API call (0 when the latest response missed usage or no observation exists).
func (m *Manager) LastInputTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.observedValid {
		return 0
	}
	return m.lastInputTokens
}

// LastTotalContextTokens returns the observed post-response context baseline
// from the most recent API call: the full normalized prompt plus generated
// output (0 when the latest response missed usage or no observation exists).
// Persistence, recovery, and diagnostics read this raw baseline; sidebar and
// trigger consumers that must stay aligned with auto-compaction should read
// EffectiveContextTokens instead, which falls back to the frozen estimate when
// the latest response missed usage.
func (m *Manager) LastTotalContextTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.observedValid {
		return 0
	}
	return m.lastTotalContextTokens
}

// ContextUsageState reports the observation state behind the gauge and trigger:
// observed, estimated (frozen), or unknown.
func (m *Manager) ContextUsageState() ContextUsageState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.observedValid {
		return ContextUsageObserved
	}
	if m.frozenValid {
		return ContextUsageEstimated
	}
	return ContextUsageUnknown
}

// FrozenEstimateTokens returns the frozen estimate for the just-finished
// request when the latest response missed usage but samples exist (0 otherwise).
// The value is fixed at freeze time and never grows with later appends.
func (m *Manager) FrozenEstimateTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.frozenValid {
		return 0
	}
	return m.frozenEstimateTokens
}

// MessageCount returns the number of messages currently in the context (for sidebar display).
func (m *Manager) MessageCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

// SetLastTotalContextTokens sets the last total context token count (e.g. when restoring from snapshot).
func (m *Manager) SetLastTotalContextTokens(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTotalContextTokens = n
	if n > 0 {
		m.observedValid = true
		m.frozenEstimateTokens = 0
		m.frozenValid = false
	} else if m.lastInputTokens <= 0 {
		m.observedValid = false
		m.frozenEstimateTokens = 0
		m.frozenValid = false
	}
}

// SetLastInputTokens sets the last input token count (e.g. when restoring a
// session from snapshot so input-budget-based context indicators show the correct value).
func (m *Manager) SetLastInputTokens(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastInputTokens = n
	if n > 0 {
		m.observedValid = true
		m.frozenEstimateTokens = 0
		m.frozenValid = false
	} else if m.lastTotalContextTokens <= 0 {
		m.observedValid = false
		m.frozenEstimateTokens = 0
		m.frozenValid = false
	}
}

// ClearLastTokenUsage clears the latest request-size usage sample without
// changing cumulative token stats. Call this after durable context rewrites so
// stale pre-rewrite usage cannot drive another automatic compaction before the
// next LLM call reports fresh usage. The calibration ratio window survives;
// only the single-sample size fields and the observed/frozen state are cleared.
func (m *Manager) ClearLastTokenUsage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastInputTokens = 0
	m.lastTotalContextTokens = 0
	m.observedValid = false
	m.frozenEstimateTokens = 0
	m.frozenValid = false
	m.calibrationInputTokens = 0
	m.calibrationContextBytes = 0
	m.calibrationImageTokens = 0
}

// InvalidateSizeObservation drops the size observation for a model/window change
// (window and tokenization may differ) while keeping the cross-model calibration
// ratio window. The next trigger decision stays unknown until fresh usage arrives.
func (m *Manager) InvalidateSizeObservation() {
	m.ClearLastTokenUsage()
}

// EstimateTotalTokens returns a rough token count for the current message list.
// Uses the same heuristic as CompressForTarget (~3 chars per token). Available as
// a restore-time fallback when callers need an approximate prompt burden before the
// next LLM call refreshes tracked usage.
func (m *Manager) EstimateTotalTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return EstimateMessagesTokens(m.messages)
}

// PayloadBytes returns message content bytes for the current message list. It
// intentionally excludes request-encoding overhead, assistant tool-call
// arguments, thinking metadata, and other API parameters.
func (m *Manager) PayloadBytes() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.payloadBytes
}

// ContextPayloadBytes returns the current prompt-side content bytes for the
// installed system prompt plus conversation messages. It excludes tool
// definitions and provider/JSON envelope overhead.
func (m *Manager) ContextPayloadBytes() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.systemPromptBytes + m.payloadBytes
}

func (m *Manager) SystemPromptPayloadBytes() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.systemPromptBytes
}

// MessagePayloadBytes returns message content bytes for a slice of messages.
func MessagePayloadBytes(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		if len(msg.Parts) > 0 {
			for _, part := range msg.Parts {
				total += len(part.Text) + int(part.PayloadBytes())
			}
		} else {
			total += len(msg.Content)
		}
	}
	return total
}

func messageContextBytes(messages []message.Message) int {
	total := MessagePayloadBytes(messages)
	for _, msg := range messages {
		total += len(msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			total += len(tc.ID) + len(tc.Name) + len(tc.Args)
		}
		for _, tb := range msg.ThinkingBlocks {
			total += len(tb.Thinking) + len(tb.Signature) + len(tb.Data)
		}
		for _, item := range msg.ResponsesOutput {
			total += len(item.ID) + len(item.EncryptedContent)
			for _, summary := range item.Summary {
				total += len(summary.Text)
			}
		}
		for _, part := range msg.GeminiParts {
			total += len(part.ThoughtSignature)
		}
		total += len(msg.ReasoningContent)
	}
	return total
}

// EstimateMessagesBytes returns the total byte size of a message slice using the
// same field accounting as the calibrated token estimator's denominator (payload
// bytes plus tool-call, thinking, responses and gemini fields), but unlike the
// estimator it keeps image payload bytes in the total. It measures the request
// surface for byte budgets and telemetry; the estimator strips images out of the
// byte ratio and charges them per image instead.
func EstimateMessagesBytes(messages []message.Message) int {
	return messageContextBytes(messages)
}

// imagePartEstimateTokens is the input-token allowance one image part carries in
// every estimate. Providers bill images per image (patch- or scale-based), not
// per base64 byte — byte accounting charged a 400 KB screenshot ~130K phantom
// tokens. The allowance covers the largest normalized image Chord accepts
// under the conservative Anthropic-style ceil(w*h/750) rule (2000x2000), so an
// unknown provider does not make capacity planning optimistic. PDF parts keep
// the byte-based accounting: their cost scales with the document and the part
// carries no page count to estimate.
const imagePartEstimateTokens = (2000*2000 + 749) / 750

// imagePartAccounting reports the two image-side quantities the token estimator
// keeps apart from the byte counters: the payload bytes image parts contribute
// and the per-image token allowance replacing them.
func imagePartAccounting(messages []message.Message) (payloadBytes, tokens int) {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type != message.ContentPartImage {
				continue
			}
			payloadBytes += int(part.PayloadBytes())
			tokens += imagePartEstimateTokens
		}
	}
	return payloadBytes, tokens
}

// messageEstimateBytesAndImages splits a message slice into the estimator's two
// inputs: the byte surface the calibrated ratio applies to (image payloads
// removed) and the per-image allowance for the image parts in it.
func messageEstimateBytesAndImages(messages []message.Message) (bytes, imageTokens int) {
	imagePayloadBytes, imageTokens := imagePartAccounting(messages)
	return messageContextBytes(messages) - imagePayloadBytes, imageTokens
}

// EstimateMessagesTokens returns the approximate input-token count for a slice
// of messages, including multipart text and attachment payloads.
func EstimateMessagesTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		total += EstimateMessageTokens(msg)
	}
	return total
}

// EstimateMessageTokens returns approximate token count for a single message.
func EstimateMessageTokens(msg message.Message) int {
	payloadBytes := len(msg.Content)
	imageTokens := 0
	if len(msg.Parts) > 0 {
		payloadBytes = 0
		for _, part := range msg.Parts {
			if part.Type == message.ContentPartImage {
				imageTokens += imagePartEstimateTokens
				continue
			}
			payloadBytes += len(part.Text) + int(part.PayloadBytes())
		}
	}
	n := payloadBytes/3 + imageTokens
	n += len(msg.ToolCallID) / 3
	for _, tc := range msg.ToolCalls {
		n += len(tc.Args) / 3
	}
	for _, tb := range msg.ThinkingBlocks {
		n += (len(tb.Thinking) + len(tb.Signature) + len(tb.Data)) / 3
	}
	for _, item := range msg.ResponsesOutput {
		n += (len(item.ID) + len(item.EncryptedContent)) / 3
		for _, summary := range item.Summary {
			n += len(summary.Text) / 3
		}
	}
	for _, part := range msg.GeminiParts {
		n += len(part.ThoughtSignature) / 3
	}
	n += len(msg.ReasoningContent) / 3
	if n < 1 {
		n = 1
	}
	return n
}

type AutoCompactDecision struct {
	LastInputTokens      int
	EstimatedInputTokens int // planning-only live growth estimate, never a trigger input
	EffectiveInputTokens int // observed baseline or frozen estimate: the only trigger/display input
	UsageState           ContextUsageState
	InputBudget          int
	ReservedInput        int
	UsableInputBudget    int
	Threshold            float64
	ThresholdTokens      int
	ShouldCompact        bool
}

// observedEffectiveLocked returns the usage-only reading: the observed baseline
// when the latest response carried usage, else the frozen estimate, else 0
// (unknown). It never includes the live growth estimate: post-response appends
// must not move the gauge or the trigger until fresh provider usage arrives.
// Must hold at least an RLock.
func (m *Manager) observedEffectiveLocked() int {
	if m.observedValid {
		return max(m.lastTotalContextTokens, m.lastInputTokens)
	}
	if m.frozenValid {
		return m.frozenEstimateTokens
	}
	return 0
}

func (m *Manager) usageStateLocked() ContextUsageState {
	if m.observedValid {
		return ContextUsageObserved
	}
	if m.frozenValid {
		return ContextUsageEstimated
	}
	return ContextUsageUnknown
}

// EffectiveContextTokens returns the context-usage level in the same frame as
// AutoCompactDecision: the observed post-response baseline, or the single frozen
// estimate when the latest response missed usage, or 0 when unknown. Sidebar
// gauges and the auto-compaction trigger observe one value. Post-response
// growth never moves it; planning budgets use the calibrated estimators instead.
func (m *Manager) EffectiveContextTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.observedEffectiveLocked()
}

// AutoCompactDecision returns the current automatic compaction threshold inputs.
// The trigger and the gauge use only provider observations (or the single frozen
// estimate when the latest response missed usage). EstimatedInputTokens stays as
// the live planning estimate for capacity consumers, but ShouldCompact never
// reads it: unknown (0) never compacts.
func (m *Manager) AutoCompactDecision() AutoCompactDecision {
	m.mu.RLock()
	defer m.mu.RUnlock()
	budget := m.inputBudget
	if budget <= 0 {
		budget = m.maxTokens
	}
	usable := max(budget-m.inputBudgetReserved, 0)
	thresholdTokens := 0
	if m.threshold > 0 && usable > 0 {
		thresholdTokens = int(m.threshold * float64(usable))
	}
	estimatedInputTokens := m.estimatedInputTokensFromPayloadBytesLocked()
	effectiveInputTokens := m.observedEffectiveLocked()
	state := m.usageStateLocked()
	shouldCompact := m.threshold > 0 && usable > 0 && effectiveInputTokens > 0 && float64(effectiveInputTokens) >= m.threshold*float64(usable)
	return AutoCompactDecision{
		LastInputTokens:      m.lastInputTokens,
		EstimatedInputTokens: estimatedInputTokens,
		EffectiveInputTokens: effectiveInputTokens,
		UsageState:           state,
		InputBudget:          budget,
		ReservedInput:        m.inputBudgetReserved,
		UsableInputBudget:    usable,
		Threshold:            m.threshold,
		ThresholdTokens:      thresholdTokens,
		ShouldCompact:        shouldCompact,
	}
}

// estimatorContextBytesLocked returns the byte denominator the calibrated
// ratio applies to: full context bytes with image payloads removed, because
// provider image billing does not scale with the base64 payload a part carries.
func (m *Manager) estimatorContextBytesLocked() int {
	return m.systemPromptContextBytes + m.contextBytes - m.imagePayloadBytes
}

func (m *Manager) estimatedInputTokensFromPayloadBytesLocked() int {
	if m.calibrationInputTokens > 0 && m.calibrationImageTokens > 0 {
		return m.estimateMixedSampleGrowthLocked(m.estimatorContextBytesLocked())
	}
	if m.calibrationInputTokens <= 0 || m.calibrationContextBytes <= 0 {
		return 0
	}
	contextBytes := m.estimatorContextBytesLocked()
	if contextBytes <= m.calibrationContextBytes {
		return 0
	}
	sampleTokens := m.calibrationInputTokens
	return int((int64(sampleTokens)*int64(contextBytes))/int64(m.calibrationContextBytes)) + m.imageEstimateTokens
}

func (m *Manager) estimateMixedSampleGrowthLocked(contextBytes int) int {
	growthBytes := max(contextBytes-m.calibrationContextBytes, 0)
	growthTokens := growthBytes / 3
	if m.calibratedRatioCache > 0 {
		growthTokens = int(float64(growthBytes) * m.calibratedRatioCache)
	}
	return m.calibrationInputTokens + growthTokens + max(m.imageEstimateTokens-m.calibrationImageTokens, 0)
}

// computeCalibratedRatioLocked recomputes the median clamped tokens-per-byte
// ratio over the bounded usage-calibration window and stores it in
// calibratedRatioCache. It must be called under the write lock whenever the
// window changes (UpdateFromUsage), so the estimate read paths only read a
// float instead of re-sorting the window per message. Returns 0 when the
// window has no usable sample. The median is robust against a single
// pathological request (e.g. a base64 image payload), and each sample is
// clamped into the sanity band before the median so the estimate stays inside
// the physical band even when the window is full of outliers. Clamping rather
// than discarding matters: an image-heavy window whose every sample sits below
// the lower bound would otherwise leave an empty window and fall back to
// bytes/3, which is ~6.7x the lower bound and would over-contract every budget
// derived from it.
func (m *Manager) computeCalibratedRatioLocked() float64 {
	if len(m.usageCalibration) == 0 {
		return 0
	}
	ratios := make([]float64, 0, len(m.usageCalibration))
	for _, sample := range m.usageCalibration {
		if sample.bytes <= 0 || sample.tokens <= 0 {
			continue
		}
		ratio := float64(sample.tokens) / float64(sample.bytes)
		ratios = append(ratios, min(max(ratio, calibrationRatioMin), calibrationRatioMax))
	}
	if len(ratios) == 0 {
		return 0
	}
	slices.Sort(ratios)
	return ratios[len(ratios)/2]
}

// EstimateMessagesTokensCalibrated returns an input-token estimate for a
// message slice using the usage-calibrated median ratio when the manager has
// samples, falling back to the plain bytes/3 estimate otherwise. The estimate
// is only used for conservative budgets (context-pressure decisions, recovery
// thresholds); provider-reported usage remains the authority for the
// compaction trigger. The receiver may be nil, in which case the plain
// estimate is returned.
func (m *Manager) EstimateMessagesTokensCalibrated(messages []message.Message) int {
	if m == nil {
		return EstimateMessagesTokens(messages)
	}
	m.mu.RLock()
	ratio := m.calibratedRatioCache
	m.mu.RUnlock()
	// The calibration denominator (system prompt + context bytes) includes
	// tool-call arguments and thinking payloads, so the ratio applies to the
	// same byte accounting — a conservative overestimate, which is the safe
	// direction for budget decisions. Image payloads are the exception: they
	// are excluded from the ratio and charged the per-image allowance, because
	// provider image billing does not scale with base64 bytes.
	//
	// The two sides are not the same surface: the denominator is the full
	// durable history, while the numerator (provider-reported prompt tokens)
	// comes from the request-level reduced surface plus tool definitions and
	// overlays. Tool definitions push the ratio up, request reduction pushes it
	// down, and under heavy reduction the net effect underestimates the ratio.
	// Callers must therefore keep this estimator to capacity planning; request
	// admission gates stay on the plain bytes/3 bound, which cannot be dragged
	// below the physical token density by a low calibration sample.
	return EstimateMessagesTokensWithRatio(messages, ratio)
}

// EstimateMessagesTokensWithRatio estimates input tokens from a caller-provided
// calibration ratio, keeping two estimates on the same calibration baseline
// even when the live manager ratio may change between them (a compaction
// preflight and its post-export re-check, for example). The ratio applies to
// the non-image bytes, and every image part adds its per-image allowance. A
// non-positive ratio falls back to the plain bytes/3 estimate, matching
// EstimateMessagesTokensCalibrated's no-sample behavior.
func EstimateMessagesTokensWithRatio(messages []message.Message, ratio float64) int {
	if ratio <= 0 {
		return EstimateMessagesTokens(messages)
	}
	bytes, imageTokens := messageEstimateBytesAndImages(messages)
	return max(int(float64(bytes)*ratio), 1) + imageTokens
}

// EstimateBytesForTokensCalibrated converts a token budget back into a byte
// allowance using the same usage-calibrated ratio as
// EstimateMessagesTokensCalibrated, falling back to tokens*3. Used where a
// byte budget must be derived from a remaining-token budget so both sides of
// a decision use the same accounting convention.
func (m *Manager) EstimateBytesForTokensCalibrated(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	if m == nil {
		return tokens * 3
	}
	m.mu.RLock()
	ratio := m.calibratedRatioCache
	m.mu.RUnlock()
	if ratio <= 0 {
		return tokens * 3
	}
	bytes := int(float64(tokens) / ratio)
	bytes = max(bytes, 1)
	return bytes
}

// ShouldAutoCompact reports whether the latest prompt size crossed the
// configured automatic compaction threshold.
func (m *Manager) ShouldAutoCompact() bool {
	return m.AutoCompactDecision().ShouldCompact
}

// CompressForTarget compresses a message list to fit within targetTokens
// using sliding-window truncation. It keeps the first message (assumed to be
// a system/context header) and as many of the most recent messages as fit.
// Token count is estimated as len(content)/3.
//
// Returns nil if the messages cannot be meaningfully compressed (e.g. there
// are fewer than 2 messages, or even the most recent message alone exceeds
// the budget). The returned slice is a new allocation; the input is not
// modified.
//
// This method is safe to call from any goroutine; it does not access Manager
// state (it operates only on the supplied messages).
func (m *Manager) CompressForTarget(messages []message.Message, targetTokens int) []message.Message {
	if len(messages) <= 2 || targetTokens <= 0 {
		return nil
	}

	// Always keep the first message (context/system header or first user turn).
	firstTokens := EstimateMessageTokens(messages[0])
	remaining := targetTokens - firstTokens
	if remaining <= 0 {
		// Even the first message exceeds the budget — can't compress.
		return nil
	}

	// Walk backwards from the end, accumulating messages that fit.
	var kept []message.Message
	for i := len(messages) - 1; i >= 1; i-- {
		cost := EstimateMessageTokens(messages[i])
		if remaining-cost < 0 {
			break
		}
		remaining -= cost
		kept = append(kept, messages[i])
	}

	if len(kept) == 0 {
		return nil
	}

	// Reverse kept so messages are in chronological order.
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}

	// Adjust boundary to avoid starting with an orphaned tool result.
	startIdx := 0
	for startIdx < len(kept) && kept[startIdx].Role == message.RoleTool {
		startIdx++
	}
	if startIdx >= len(kept) {
		return nil
	}
	kept = kept[startIdx:]

	// Build the compressed message list.
	discarded := len(messages) - 1 - len(kept) // -1 for first message
	header := message.Message{
		Role: message.RoleUser,
		Content: fmt.Sprintf(
			"[system] Context was compressed to fit a smaller model. %d earlier messages were removed. Recent conversation continues below.",
			discarded,
		),
	}

	result := make([]message.Message, 0, 2+len(kept))
	result = append(result, messages[0]) // original first message
	result = append(result, header)
	result = append(result, kept...)
	return result
}

// SafeKeepBoundary adjusts the raw boundary index so that we don't start
// the kept slice with a "tool" message (which would be an orphaned tool
// result without its preceding assistant tool_calls message). If the message
// at rawBoundary is a tool result, we scan backwards to include the matching
// assistant message as well.
const incompleteToolCallProtectedTailMessages = 10

func SafeKeepBoundary(msgs []message.Message, rawBoundary int) int {
	if rawBoundary <= 0 {
		return 0
	}
	if rawBoundary > len(msgs) {
		rawBoundary = len(msgs)
	}

	boundary := rawBoundary

	// Walk backwards while the boundary message is a tool result. We need to
	// include the assistant message that initiated the tool call.
	for boundary > 0 && boundary < len(msgs) && msgs[boundary].Role == message.RoleTool {
		boundary--
	}

	if boundary == 0 {
		return 0
	}

	// Do not archive assistant tool calls whose tool results have not been recorded
	// yet. Keeping the assistant and any partial results in the tail preserves a
	// valid request surface for the next LLM call. Walk forward and stop at the
	// earliest pending assistant inside the protected tail window; the resulting
	// split point safely covers any later pending assistants too because they sit
	// in the kept tail.
	for i := 0; i < boundary; i++ {
		if msgs[i].Role != message.RoleAssistant || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		if boundary-i > incompleteToolCallProtectedTailMessages {
			continue
		}
		pending := false
		for _, tc := range msgs[i].ToolCalls {
			if tc.ID == "" {
				continue
			}
			found := false
			for j := i + 1; j < boundary; j++ {
				if msgs[j].Role == message.RoleTool && msgs[j].ToolCallID == tc.ID {
					found = true
					break
				}
			}
			if !found {
				pending = true
				break
			}
		}
		if pending {
			// Pull the boundary just before this assistant. Walk back over any
			// preceding tool results so the kept tail does not start mid-chain.
			split := i
			for split > 0 && msgs[split-1].Role == message.RoleTool {
				split--
			}
			return split
		}
	}

	return boundary
}

// ReplacePrefixAtomic replaces [0, upTo) with prefix, preserving [upTo:] as tail.
// The under callback is invoked while holding the lock, allowing atomic operations
// (e.g., rewriting main.jsonl) to be performed with a consistent view of the tail.
// The callback receives the tail slice and returns the complete new message list.
// If under returns an error, the operation is aborted without modifying the message list.
// Orphan tool results in the tail are automatically repaired.
func (m *Manager) ReplacePrefixAtomic(
	upTo int,
	prefix []message.Message,
	under func(tail []message.Message) ([]message.Message, error),
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate upTo bounds
	if upTo < 0 {
		upTo = 0
	}
	if upTo > len(m.messages) {
		upTo = len(m.messages)
	}

	// Extract tail
	tail := m.messages[upTo:]

	// Repair orphan tool results in tail (tool results without matching tool_calls in prefix)
	repairedTail, dropped := repairOrphanToolResultsInTail(prefix, tail)
	if dropped > 0 {
		log.Warnf("ctxmgr: removed orphan tool messages during ReplacePrefixAtomic dropped=%v", dropped)
	}

	// Invoke callback with tail to get final message list
	if under == nil {
		// No callback: just apply prefix + tail directly
		newMessages := make([]message.Message, 0, len(prefix)+len(repairedTail))
		newMessages = append(newMessages, prefix...)
		newMessages = append(newMessages, repairedTail...)
		m.messages = newMessages
		m.refreshMessageByteStateLocked()
		return nil
	}

	newMessages, err := under(repairedTail)
	if err != nil {
		return err
	}

	// Apply the new messages
	if newMessages == nil {
		m.messages = nil
	} else {
		m.messages = make([]message.Message, len(newMessages))
		copy(m.messages, newMessages)
	}
	m.refreshMessageByteStateLocked()

	return nil
}

func (m *Manager) refreshMessageByteStateLocked() {
	m.payloadBytes = MessagePayloadBytes(m.messages)
	m.contextBytes = messageContextBytes(m.messages)
	m.imagePayloadBytes, m.imageEstimateTokens = imagePartAccounting(m.messages)
	m.calibrationInputTokens = 0
	m.calibrationContextBytes = 0
	m.calibrationImageTokens = 0
	// Byte-state refresh runs exactly after wholesale rewrites (compaction
	// replace, orphan repair, restore), where the declared-ID index must be
	// rebuilt; incremental appends never reach here.
	m.rebuildToolCallIDIndexLocked()
}

// repairOrphanToolResultsInTail removes tool messages from tail that don't have
// a matching tool_call in the prefix. This ensures the resulting message list
// is valid for strict APIs that require tool_results to have matching tool_calls.
func repairOrphanToolResultsInTail(prefix []message.Message, tail []message.Message) ([]message.Message, int) {
	if len(tail) == 0 {
		return tail, 0
	}

	// Collect all tool call IDs from prefix
	prefixCallIDs := make(map[string]struct{})
	for _, msg := range prefix {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				prefixCallIDs[tc.ID] = struct{}{}
			}
		}
	}

	// Also collect tool call IDs from tail's assistant messages
	// (tool results can reference tool_calls from earlier in tail)
	tailCallIDs := make(map[string]struct{})
	for _, msg := range tail {
		if msg.Role == message.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					tailCallIDs[tc.ID] = struct{}{}
				}
			}
		}
	}

	// Filter out orphan tool results
	var repaired []message.Message
	dropped := 0
	for _, msg := range tail {
		if msg.Role == message.RoleTool && msg.ToolCallID != "" {
			// Check if the tool_call_id exists in prefix or earlier in tail
			_, inPrefix := prefixCallIDs[msg.ToolCallID]
			_, inTail := tailCallIDs[msg.ToolCallID]
			if !inPrefix && !inTail {
				dropped++
				continue
			}
		}
		repaired = append(repaired, msg)
	}

	if dropped == 0 {
		return tail, 0
	}
	return repaired, dropped
}
