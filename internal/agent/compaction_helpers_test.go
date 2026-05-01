package agent

import "fmt"

// startCompactionState seeds compactionState for tests that exercise gate logic
// without running the full async compaction goroutine. Production schedules
// compaction through scheduleCompactionAsync, which sets these fields itself.
func (a *MainAgent) startCompactionState(planID uint64, target compactionTarget, trigger compactionTrigger, continuation continuationPlan) {
	a.compactionState = compactionState{
		running:      true,
		planID:       planID,
		target:       target,
		trigger:      trigger,
		discard:      false,
		continuation: continuation,
		headSplit:    0,
		cancel:       nil,
	}
}

// historyMutationAllowed reports whether a mutation of an existing persisted
// history message at idx is allowed under the current compaction state. Used
// only by gate-policy tests; production runtime relies on ReplacePrefixAtomic
// to enforce the same invariant.
func (a *MainAgent) historyMutationAllowed(idx int) error {
	if idx < 0 {
		return nil
	}
	if !a.IsCompactionRunning() {
		return nil
	}
	if idx < a.compactionState.headSplit {
		return fmt.Errorf("history mutation at index %d blocked: async compaction froze [0, %d)", idx, a.compactionState.headSplit)
	}
	return nil
}

// shouldDurableCompactBeforeMainLLM exposes the durable-compaction gate decision
// to tests; production paths consume compactionTriggerForMainLLM directly.
func (a *MainAgent) shouldDurableCompactBeforeMainLLM() bool {
	return a.compactionTriggerForMainLLM().needed()
}
