package config

import (
	"fmt"
	"time"
)

const (
	DefaultMaxLiveRuntimes     = 10
	DefaultMaxBorrowedRuntimes = 1
	// DefaultMaxBypassRuntimes bounds the deadlock-avoidance safety valve used
	// when a parked worker must be woken to receive a durable message and both
	// the runtime and borrow pools are exhausted. Wake reactivations run on the
	// main event loop, so they cannot block on capacity that only that loop can
	// release; the grant is uncounted against the runtime pool but must still be
	// finite, otherwise max_live_runtimes stops being an upper bound.
	DefaultMaxBypassRuntimes     = 4
	DefaultMaxActiveLLMRequests  = 10
	DefaultSubAgentQueueMessages = 256
	DefaultSubAgentQueueBytes    = 4 << 20
	DefaultMailboxMemoryMessages = 512
	DefaultMailboxMemoryBytes    = 8 << 20
	DefaultContextCompactUsage   = 0.8

	// A worker that escalated parks until its owner replies. Two independent
	// clocks decide when that wait is abandoned, because neither is sufficient
	// alone: explicit user turns alone would cancel a worker that escalated
	// seconds ago just because the user held five quick exchanges with the main
	// agent, and would never fire at all in a headless run or while the user is
	// away. The wait expires when the turn budget is spent AND at least
	// WaitingMainMinWait has passed, or unconditionally after
	// WaitingMainMaxWait.
	DefaultWaitingMainExpiryTurns = 5
	DefaultWaitingMainMinWait     = 5 * time.Minute
	DefaultWaitingMainMaxWait     = time.Hour
)

// OrchestrationConfig controls process-local multi-agent resource admission.
// Nonpositive scalars retain inherited or built-in defaults. Map entries only
// enforce positive limits; a zero entry removes that inherited map limit.
type OrchestrationConfig struct {
	MaxLiveRuntimes                 int            `json:"max_live_runtimes,omitempty" yaml:"max_live_runtimes,omitempty"`
	MaxBorrowedRuntimes             int            `json:"max_borrowed_runtimes,omitempty" yaml:"max_borrowed_runtimes,omitempty"`
	MaxBypassRuntimes               int            `json:"max_bypass_runtimes,omitempty" yaml:"max_bypass_runtimes,omitempty"`
	MaxActiveLLMRequests            int            `json:"max_active_llm_requests,omitempty" yaml:"max_active_llm_requests,omitempty"`
	ProviderMaxActiveRequests       map[string]int `json:"provider_max_active_requests,omitempty" yaml:"provider_max_active_requests,omitempty"`
	ModelMaxActiveRequests          map[string]int `json:"model_max_active_requests,omitempty" yaml:"model_max_active_requests,omitempty"`
	ProviderMaxActiveHostedRequests map[string]int `json:"provider_max_active_hosted_requests,omitempty" yaml:"provider_max_active_hosted_requests,omitempty"`
	ProviderHostedRequestsPerMinute map[string]int `json:"provider_hosted_requests_per_minute,omitempty" yaml:"provider_hosted_requests_per_minute,omitempty"`
	ProviderHostedRetriesPerMinute  map[string]int `json:"provider_hosted_retries_per_minute,omitempty" yaml:"provider_hosted_retries_per_minute,omitempty"`
	SubAgentQueueMessages           int            `json:"subagent_queue_messages,omitempty" yaml:"subagent_queue_messages,omitempty"`
	SubAgentQueueBytes              int            `json:"subagent_queue_bytes,omitempty" yaml:"subagent_queue_bytes,omitempty"`
	MailboxMemoryMessages           int            `json:"mailbox_memory_messages,omitempty" yaml:"mailbox_memory_messages,omitempty"`
	MailboxMemoryBytes              int            `json:"mailbox_memory_bytes,omitempty" yaml:"mailbox_memory_bytes,omitempty"`
	WaitingMainExpiryTurns          int            `json:"waiting_main_expiry_turns,omitempty" yaml:"waiting_main_expiry_turns,omitempty"`
	WaitingMainMinWaitSec           int            `json:"waiting_main_min_wait_sec,omitempty" yaml:"waiting_main_min_wait_sec,omitempty"`
	WaitingMainMaxWaitSec           int            `json:"waiting_main_max_wait_sec,omitempty" yaml:"waiting_main_max_wait_sec,omitempty"`
}

func (c OrchestrationConfig) EffectiveWaitingMainExpiryTurns() uint64 {
	if c.WaitingMainExpiryTurns > 0 {
		return uint64(c.WaitingMainExpiryTurns)
	}
	return DefaultWaitingMainExpiryTurns
}

func (c OrchestrationConfig) EffectiveWaitingMainMinWait() time.Duration {
	if c.WaitingMainMinWaitSec > 0 {
		return time.Duration(c.WaitingMainMinWaitSec) * time.Second
	}
	return DefaultWaitingMainMinWait
}

// EffectiveWaitingMainMaxWait never returns less than the min wait: a config
// that inverts the two would otherwise make the unconditional clock fire before
// the guarded one, cancelling escalations the min wait exists to protect.
func (c OrchestrationConfig) EffectiveWaitingMainMaxWait() time.Duration {
	if c.WaitingMainMaxWaitSec > 0 {
		return max(time.Duration(c.WaitingMainMaxWaitSec)*time.Second, c.EffectiveWaitingMainMinWait())
	}
	return max(DefaultWaitingMainMaxWait, c.EffectiveWaitingMainMinWait())
}

func (c OrchestrationConfig) EffectiveSubAgentQueueMessages() int {
	if c.SubAgentQueueMessages > 0 {
		return c.SubAgentQueueMessages
	}
	return DefaultSubAgentQueueMessages
}

func (c OrchestrationConfig) EffectiveSubAgentQueueBytes() int {
	if c.SubAgentQueueBytes > 0 {
		return c.SubAgentQueueBytes
	}
	return DefaultSubAgentQueueBytes
}

func (c OrchestrationConfig) EffectiveMailboxMemoryMessages() int {
	if c.MailboxMemoryMessages > 0 {
		return c.MailboxMemoryMessages
	}
	return DefaultMailboxMemoryMessages
}

func (c OrchestrationConfig) EffectiveMailboxMemoryBytes() int {
	if c.MailboxMemoryBytes > 0 {
		return c.MailboxMemoryBytes
	}
	return DefaultMailboxMemoryBytes
}

func (c OrchestrationConfig) EffectiveMaxLiveRuntimes() int {
	if c.MaxLiveRuntimes > 0 {
		return c.MaxLiveRuntimes
	}
	return DefaultMaxLiveRuntimes
}

func (c OrchestrationConfig) EffectiveMaxBorrowedRuntimes() int {
	if c.MaxBorrowedRuntimes > 0 {
		return c.MaxBorrowedRuntimes
	}
	return DefaultMaxBorrowedRuntimes
}

func (c OrchestrationConfig) EffectiveMaxBypassRuntimes() int {
	if c.MaxBypassRuntimes > 0 {
		return c.MaxBypassRuntimes
	}
	return DefaultMaxBypassRuntimes
}

func (c OrchestrationConfig) EffectiveMaxActiveLLMRequests() int {
	if c.MaxActiveLLMRequests > 0 {
		return c.MaxActiveLLMRequests
	}
	return DefaultMaxActiveLLMRequests
}

// Orchestration load-time validation.
//
// Two failure classes are handled differently:
//   - Explicit contradictions between configured values fail the load
//     (orchestrationConfigLoadError). Every scalar is zero when unset, so a
//     single-key problem can never be told apart from "key omitted" at the
//     decoded struct level; only relations between two non-zero values are
//     detectable. The Effective* fallbacks stay in place as the documented
//     semantics for unset or default-retaining values.
//   - Detectably out-of-range single values are reset to their unset state
//     and reported as issues, mirroring how the compaction thresholds are
//     handled, so a broken value behaves
//     as not configured instead of riding through to the runtime.
//
// The three runtime pools (max_live_runtimes / max_borrowed_runtimes /
// max_bypass_runtimes) are independent capacity tiers with a strictly ordered
// fallback (runtime → borrow → bypass → refuse), so no arithmetic relation
// between them is required; the only cross-field constraint in the section is
// the waiting_main clock pair below.

// orchestrationConfigLoadError reports orchestration configurations whose
// explicitly set values contradict each other. A waiting_main_max_wait_sec
// below waiting_main_min_wait_sec would force the effective-value layer to
// silently raise the explicit maximum (or, when only one side is set, rewrite
// the default line), hiding a likely unit or meaning mistake. Both clocks must
// therefore be explicit and inverted for the load to fail; a single-sided
// value keeps the documented fallback where the effective maximum is never
// below the effective minimum, and zero values on either side retain the
// default.
func orchestrationConfigLoadError(cfg *Config) error {
	orch := cfg.Orchestration
	if orch.WaitingMainMinWaitSec > 0 && orch.WaitingMainMaxWaitSec > 0 && orch.WaitingMainMaxWaitSec < orch.WaitingMainMinWaitSec {
		return fmt.Errorf("orchestration: waiting_main_max_wait_sec %d is below waiting_main_min_wait_sec %d; the unconditional expiry clock must not run before the guarded one — raise the maximum or lower the minimum", orch.WaitingMainMaxWaitSec, orch.WaitingMainMinWaitSec)
	}
	return nil
}

// orchestrationScalarLeaf pairs one scalar orchestration leaf path with the
// value decoded from the merged config under evaluation.
type orchestrationScalarLeaf struct {
	path  []string
	value int
}

// orchestrationScalarLeafValues lists the scalar orchestration keys. At the
// project layer a value of zero or below means "keep the inherited line"
// (documented: only positive project scalar values override the corresponding
// global values), so the semantic strip pass evaluates each leaf against the
// merged candidate through these values.
func orchestrationScalarLeafValues(o OrchestrationConfig) []orchestrationScalarLeaf {
	return []orchestrationScalarLeaf{
		{[]string{"orchestration", "max_live_runtimes"}, o.MaxLiveRuntimes},
		{[]string{"orchestration", "max_borrowed_runtimes"}, o.MaxBorrowedRuntimes},
		{[]string{"orchestration", "max_bypass_runtimes"}, o.MaxBypassRuntimes},
		{[]string{"orchestration", "max_active_llm_requests"}, o.MaxActiveLLMRequests},
		{[]string{"orchestration", "subagent_queue_messages"}, o.SubAgentQueueMessages},
		{[]string{"orchestration", "subagent_queue_bytes"}, o.SubAgentQueueBytes},
		{[]string{"orchestration", "mailbox_memory_messages"}, o.MailboxMemoryMessages},
		{[]string{"orchestration", "mailbox_memory_bytes"}, o.MailboxMemoryBytes},
		{[]string{"orchestration", "waiting_main_expiry_turns"}, o.WaitingMainExpiryTurns},
		{[]string{"orchestration", "waiting_main_min_wait_sec"}, o.WaitingMainMinWaitSec},
		{[]string{"orchestration", "waiting_main_max_wait_sec"}, o.WaitingMainMaxWaitSec},
	}
}
