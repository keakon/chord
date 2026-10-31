package agent

import (
	"strings"
	"sync"

	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
)

// externalUsageTracker accumulates one external worker's reported usage across
// the task's turns. Claude Code repeats one model response's usage while it
// streams, so assistant-message counts keep the highest value seen per message
// id. Those per-step output counts are placeholders, so a completed result's
// modelUsage (the session-cumulative whole-tree total) is authoritative when
// present; its per-turn usage is the fallback for older CLI releases. A result
// event's total_cost_usd is likewise the running total of its Claude session —
// the CLI resumes the same session across this task's turns — so cost keeps the
// latest report instead of summing per-turn values.
type externalUsageTracker struct {
	mu sync.Mutex
	// messages holds the deduplicated per-response counts; seeded carries the
	// baseline a rehydrated worker restored from the durable record. They are
	// used for live progress and crash recovery until a result supplies a
	// complete token report.
	messages map[string]message.TokenUsage
	// unkeyed sums responses that arrived without a message ID: there is
	// nothing to deduplicate them by, so each counts once.
	unkeyed message.TokenUsage
	// resultTokens accumulates per-turn result usage when modelUsage is absent.
	// It is cleared when a cumulative modelUsage report supersedes it.
	resultTokens message.TokenUsage
	// cumulative is the latest session-wide token total from a result's
	// modelUsage. It already includes earlier turns and nested agents, so it
	// replaces seeded and resultTokens instead of adding to them.
	cumulative *message.TokenUsage
	seeded     message.TokenUsage
	usage      AgentUsage
}

func newExternalUsageTracker() *externalUsageTracker {
	return &externalUsageTracker{messages: make(map[string]message.TokenUsage)}
}

// observeMessage folds one complete assistant message's usage in. The raw
// Claude report is normalized the way the LLM client normalizes its own:
// InputTokens gains the cache-read subset, and the cache fields keep the raw
// split as subsets. Its output count is a placeholder until the result arrives,
// so this is live progress and a crash fallback, not the final token report.
func (t *externalUsageTracker) observeMessage(id string, u externalclaude.Usage) {
	normalized := normalizeExternalUsage(u)
	if externalTokenUsageEmpty(normalized) {
		return
	}
	id = strings.TrimSpace(id)
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == "" {
		addTokenUsage(&t.unkeyed, normalized)
		t.recomputeTokensLocked()
		return
	}
	if t.messages == nil {
		t.messages = make(map[string]message.TokenUsage)
	}
	saved := t.messages[id]
	saved.InputTokens = max(saved.InputTokens, normalized.InputTokens)
	saved.CacheReadTokens = max(saved.CacheReadTokens, normalized.CacheReadTokens)
	saved.CacheWriteTokens = max(saved.CacheWriteTokens, normalized.CacheWriteTokens)
	saved.OutputTokens = max(saved.OutputTokens, normalized.OutputTokens)
	t.messages[id] = saved
	t.recomputeTokensLocked()
}

// observeResultUsage replaces the live message counts with the result's token
// report. modelUsage is the CLI's session-cumulative whole-tree total and is
// therefore the authoritative source; result usage covers only the turn's main
// loop and is used when an older CLI omits modelUsage. A zeroed report is what
// a crashed session emits, so the already-received message counts stay as the
// fallback.
func (t *externalUsageTracker) observeResultUsage(usage *externalclaude.Usage, modelUsage map[string]externalclaude.ModelUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if total, ok := externalModelUsageTotal(modelUsage); ok {
		t.cumulative = &total
		t.resultTokens = message.TokenUsage{}
		t.messages = make(map[string]message.TokenUsage)
		t.unkeyed = message.TokenUsage{}
		t.recomputeTokensLocked()
		return
	}
	if usage == nil {
		return
	}
	total := normalizeExternalUsage(*usage)
	if externalTokenUsageEmpty(total) {
		return
	}
	addTokenUsage(&t.resultTokens, total)
	t.messages = make(map[string]message.TokenUsage)
	t.unkeyed = message.TokenUsage{}
	t.recomputeTokensLocked()
}

// observeResultCost replaces the cost with one result event's session-cumulative report.
func (t *externalUsageTracker) observeResultCost(cost float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// A resumed session continues from the total its transcript saved, so a
	// real report never regresses below what is already known. A zero is
	// therefore not a measurement: crash and startup-error results carry zeroed
	// values, and a result without the field decodes as zero. Keeping the known
	// cost is closer to the truth than resetting the display to $0.
	if cost <= 0 && t.usage.CostKnown && t.usage.CostUSD > 0 {
		return
	}
	t.usage.CostUSD = cost
	t.usage.CostKnown = true
}

// seed loads a usage snapshot persisted for an earlier attempt of the same
// task, so a rehydrated worker's display continues from where the record left
// off. A resumed Claude session's cumulative modelUsage already includes that
// earlier spend, so the seeded tokens are used only while no such report has
// arrived.
func (t *externalUsageTracker) seed(seeded AgentUsage) {
	if seeded.Zero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seeded = seeded.Tokens
	if seeded.CostKnown {
		t.usage.CostUSD = seeded.CostUSD
		t.usage.CostKnown = true
	}
	t.recomputeTokensLocked()
}

func (t *externalUsageTracker) snapshot() AgentUsage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

func (t *externalUsageTracker) recomputeTokensLocked() {
	total := t.seeded
	if t.cumulative != nil {
		total = *t.cumulative
	}
	addTokenUsage(&total, t.resultTokens)
	addTokenUsage(&total, t.unkeyed)
	for _, u := range t.messages {
		addTokenUsage(&total, u)
	}
	t.usage.Tokens = total
}

func normalizeExternalUsage(u externalclaude.Usage) message.TokenUsage {
	return message.TokenUsage{
		InputTokens:      u.InputTokens + u.CacheReadInputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

func externalModelUsageTotal(usages map[string]externalclaude.ModelUsage) (message.TokenUsage, bool) {
	var total message.TokenUsage
	for _, u := range usages {
		total.InputTokens += u.InputTokens + u.CacheReadInputTokens
		total.OutputTokens += u.OutputTokens
		total.CacheReadTokens += u.CacheReadInputTokens
		total.CacheWriteTokens += u.CacheCreationInputTokens
	}
	return total, !externalTokenUsageEmpty(total)
}

func addTokenUsage(total *message.TokenUsage, add message.TokenUsage) {
	total.InputTokens += add.InputTokens
	total.OutputTokens += add.OutputTokens
	total.CacheReadTokens += add.CacheReadTokens
	total.CacheWriteTokens += add.CacheWriteTokens
}

func externalTokenUsageEmpty(u message.TokenUsage) bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheWriteTokens == 0
}
