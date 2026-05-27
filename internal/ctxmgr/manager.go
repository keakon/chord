// Package ctxmgr provides the context manager — the single source of truth
// for the conversation message list. All access is thread-safe.
package ctxmgr

import (
	"fmt"
	"sync"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// Manager holds the conversation state: system prompt, message history,
// token stats, and compression settings.
type Manager struct {
	mu                     sync.RWMutex
	systemPrompt           message.Message
	messages               []message.Message
	lastInputTokens        int // prompt size only (for compaction thresholds and input-budget displays)
	lastTotalContextTokens int // true input-side context burden (input + cache_write) for recovery/diagnostics
	maxTokens              int
	inputBudget            int
	inputBudgetReserved    int

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
func (m *Manager) SetTokenBudgets(maxTokens, inputBudget, reservedInput int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inputBudget <= 0 {
		inputBudget = maxTokens
	}
	if reservedInput < 0 {
		reservedInput = 0
	}
	m.maxTokens = maxTokens
	m.inputBudget = inputBudget
	m.inputBudgetReserved = reservedInput
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
	m.messages = append(m.messages, msg)
}

// DropLastMessage removes the last message from the conversation history.
// Safe to call from multiple goroutines.
func (m *Manager) DropLastMessage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.messages); n > 0 {
		m.messages = m.messages[:n-1]
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
	m.messages = m.messages[:len(m.messages)-n]
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
// or orphan-tool repair removes every message, lastInputTokens and
// lastTotalContextTokens are reset to 0 so context indicators stay empty until
// the next LLM call refreshes the tracked usage.
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
	if len(repaired) == 0 {
		m.lastInputTokens = 0
		m.lastTotalContextTokens = 0
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
	if len(repaired) == 0 {
		m.lastInputTokens = 0
		m.lastTotalContextTokens = 0
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

// AnyAssistantDeclaresToolCallID reports whether any assistant message in the
// current history lists the given tool call id in ToolCalls.
func (m *Manager) AnyAssistantDeclaresToolCallID(callID string) bool {
	if callID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.messages {
		if m.messages[i].Role != "assistant" {
			continue
		}
		for _, tc := range m.messages[i].ToolCalls {
			if tc.ID == callID {
				return true
			}
		}
	}
	return false
}

// RestoreStats resets cumulative token statistics to the given values.
// Used when resuming a session so GetStats reflects that session's history.
func (m *Manager) RestoreStats(usage message.TokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = usage
}

// UpdateFromUsage accumulates token usage statistics from an API response.
// lastInputTokens = prompt size (for compaction thresholds and input-budget displays).
// lastTotalContextTokens = actual context-window burden from the most recent request:
// input_tokens plus cache_creation_input_tokens. Anthropic input_tokens already
// includes cache_read_input_tokens, while output/reasoning are generated after
// the request and do not occupy the input-side context window for that request.
func (m *Manager) UpdateFromUsage(usage message.TokenUsage) {
	m.mu.Lock()
	m.stats.InputTokens += usage.InputTokens
	m.stats.OutputTokens += usage.OutputTokens
	m.stats.CacheReadTokens += usage.CacheReadTokens
	m.stats.CacheWriteTokens += usage.CacheWriteTokens
	m.stats.ReasoningTokens += usage.ReasoningTokens
	m.lastInputTokens = usage.InputTokens
	m.lastTotalContextTokens = usage.InputTokens + usage.CacheWriteTokens
	m.mu.Unlock()
}

// GetStats returns the cumulative token usage.
func (m *Manager) GetStats() message.TokenUsage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

// LastInputTokens returns the input token count from the most recent API call.
func (m *Manager) LastInputTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastInputTokens
}

// LastTotalContextTokens returns the true input-side context burden from the
// most recent API call: input_tokens plus cache_creation_input_tokens.
// Preserved for recovery and diagnostics when callers need the full prompt-side burden.
func (m *Manager) LastTotalContextTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastTotalContextTokens
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
}

// SetLastInputTokens sets the last input token count (e.g. when restoring a
// session from snapshot so input-budget-based context indicators show the correct value).
func (m *Manager) SetLastInputTokens(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastInputTokens = n
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

// EstimateMessagesTokens returns approximate token count for a slice of
// messages (content/3 + tool/3 + thinking/3, min 1 per msg).
func EstimateMessagesTokens(messages []message.Message) int {
	total := 0
	for _, msg := range messages {
		total += EstimateMessageTokens(msg)
	}
	return total
}

// EstimateMessageTokens returns approximate token count for a single message.
func EstimateMessageTokens(msg message.Message) int {
	n := len(msg.Content) / 3
	for _, tc := range msg.ToolCalls {
		n += len(tc.Args) / 3
	}
	for _, tb := range msg.ThinkingBlocks {
		n += len(tb.Thinking) / 3
	}
	if n < 1 {
		n = 1
	}
	return n
}

// ShouldAutoCompact reports whether the latest prompt size crossed the
// configured automatic compaction threshold.
func (m *Manager) ShouldAutoCompact() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	budget := m.inputBudget
	if budget <= 0 {
		budget = m.maxTokens
	}
	budget -= m.inputBudgetReserved
	if m.threshold <= 0 || budget <= 0 {
		return false
	}
	return float64(m.lastInputTokens) >= m.threshold*float64(budget)
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
	for startIdx < len(kept) && kept[startIdx].Role == "tool" {
		startIdx++
	}
	if startIdx >= len(kept) {
		return nil
	}
	kept = kept[startIdx:]

	// Build the compressed message list.
	discarded := len(messages) - 1 - len(kept) // -1 for first message
	header := message.Message{
		Role: "user",
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
	for boundary > 0 && boundary < len(msgs) && msgs[boundary].Role == "tool" {
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
		if msgs[i].Role != "assistant" || len(msgs[i].ToolCalls) == 0 {
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
				if msgs[j].Role == "tool" && msgs[j].ToolCallID == tc.ID {
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
			for split > 0 && msgs[split-1].Role == "tool" {
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

	return nil
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
		if msg.Role == "assistant" {
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
		if msg.Role == "tool" && msg.ToolCallID != "" {
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
