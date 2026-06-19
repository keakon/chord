package agent

import "sync"

// subAgentRegistry owns the live sub-agent orchestration state and the single
// RWMutex that guards it. These four maps plus the lock were previously inlined
// into MainAgent, interleaved with ~180 unrelated fields and reached into
// directly across ~16 files. Consolidating them here gives the orchestration
// concern one documented home and lets the dangerous multi-map mutations (such
// as removing an agent and all of its per-agent bookkeeping) become a single
// atomic method.
//
// Locking model is unchanged from the old MainAgent.mu: the lock is held for
// the duration of each method, and the package-internal "...Locked" helpers
// that iterate the maps across multi-step logic run while a caller holds mu via
// the rlock/runlock/lock/unlock helpers.
type subAgentRegistry struct {
	mu               sync.RWMutex
	subAgents        map[string]*SubAgent          // instanceID → live SubAgent
	taskRecords      map[string]*DurableTaskRecord // taskID → durable task record
	nudgeCounts      map[string]int                // agentID → idle nudge count
	stateEnteredTurn map[string]uint64             // agentID → turn it entered a waiting/terminal state
}

func newSubAgentRegistry() subAgentRegistry {
	return subAgentRegistry{
		subAgents:        make(map[string]*SubAgent),
		taskRecords:      make(map[string]*DurableTaskRecord),
		nudgeCounts:      make(map[string]int),
		stateEnteredTurn: make(map[string]uint64),
	}
}

// subAgent returns the live SubAgent for instanceID, or nil if none.
func (r *subAgentRegistry) subAgent(instanceID string) *SubAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subAgents[instanceID]
}

// snapshotSubAgents returns a slice copy of the live (non-nil) sub-agents so
// callers can iterate without holding the lock.
func (r *subAgentRegistry) snapshotSubAgents() []*SubAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*SubAgent, 0, len(r.subAgents))
	for _, sub := range r.subAgents {
		if sub != nil {
			out = append(out, sub)
		}
	}
	return out
}

// count returns the number of live sub-agents.
func (r *subAgentRegistry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.subAgents)
}

// add registers a live sub-agent under its instance ID.
func (r *subAgentRegistry) add(sub *SubAgent) {
	if sub == nil {
		return
	}
	r.mu.Lock()
	r.subAgents[sub.instanceID] = sub
	r.mu.Unlock()
}

// remove deletes a sub-agent and all of its per-agent bookkeeping (nudge count,
// state-entered-turn) under one lock, returning the removed SubAgent (or nil).
// Consolidating the three deletes prevents the partial-cleanup hazard of doing
// them separately.
func (r *subAgentRegistry) remove(instanceID string) *SubAgent {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub := r.subAgents[instanceID]
	delete(r.subAgents, instanceID)
	delete(r.nudgeCounts, instanceID)
	delete(r.stateEnteredTurn, instanceID)
	return sub
}
