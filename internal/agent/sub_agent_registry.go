package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/tools"
)

type subAgentActivation struct {
	done            chan struct{}
	sub             *SubAgent
	previousAgentID string
	err             error
	cancelled       bool
}

type subAgentAdmission struct {
	taskID             string
	ownerAgentID       string
	ownerTaskID        string
	agentType          string
	planTaskRef        string
	semanticTaskKey    string
	expectedWriteScope tools.WriteScope
	slotHeld           bool
	done               chan struct{}
	once               sync.Once
	result             tools.TaskHandle
	err                error
}

func (a *subAgentAdmission) complete(result tools.TaskHandle, err error) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		a.result = result
		a.err = err
		if a.done != nil {
			close(a.done)
		}
	})
}

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
	subAgents        map[string]*SubAgent           // instanceID → live SubAgent
	taskRecords      map[string]*DurableTaskRecord  // taskID → durable task record
	activations      map[string]*subAgentActivation // taskID → in-flight runtime rehydration
	admissions       map[string]*subAgentAdmission  // taskID → in-flight new-task admission
	nudgeCounts      map[string]int                 // agentID → idle nudge count
	stateEnteredTurn map[string]uint64              // agentID → turn it entered a waiting/terminal state
}

func newSubAgentRegistry() subAgentRegistry {
	return subAgentRegistry{
		subAgents:        make(map[string]*SubAgent),
		taskRecords:      make(map[string]*DurableTaskRecord),
		activations:      make(map[string]*subAgentActivation),
		admissions:       make(map[string]*subAgentAdmission),
		nudgeCounts:      make(map[string]int),
		stateEnteredTurn: make(map[string]uint64),
	}
}

func (r *subAgentRegistry) addAdmissionLocked(admission *subAgentAdmission) {
	if admission == nil || strings.TrimSpace(admission.taskID) == "" {
		return
	}
	if r.admissions == nil {
		r.admissions = make(map[string]*subAgentAdmission)
	}
	if admission.done == nil {
		admission.done = make(chan struct{})
	}
	r.admissions[admission.taskID] = admission
}

func (r *subAgentRegistry) removeAdmissionLocked(taskID string) *subAgentAdmission {
	taskID = strings.TrimSpace(taskID)
	admission := r.admissions[taskID]
	delete(r.admissions, taskID)
	return admission
}

func (r *subAgentRegistry) cancelAdmissionsLocked() (slots int) {
	for taskID, admission := range r.admissions {
		if admission != nil && admission.slotHeld {
			slots++
			admission.slotHeld = false
		}
		if admission != nil {
			admission.complete(tools.TaskHandle{}, fmt.Errorf("task admission cancelled"))
		}
		delete(r.admissions, taskID)
	}
	return slots
}

// subAgent returns the live SubAgent for instanceID, or nil if none.
func (r *subAgentRegistry) subAgent(instanceID string) *SubAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subAgents[instanceID]
}

func (r *subAgentRegistry) withSubAgent(instanceID string, fn func(*SubAgent) bool) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub := r.subAgents[instanceID]
	return sub != nil && fn(sub)
}

func (r *subAgentRegistry) subAgentByTaskID(taskID string) *SubAgent {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.activations[taskID] != nil {
		return nil
	}
	return r.subAgentByTaskIDLocked(taskID)
}

func (r *subAgentRegistry) subAgentByTaskIDLocked(taskID string) *SubAgent {
	for _, sub := range r.subAgents {
		if sub != nil && strings.TrimSpace(sub.taskID) == taskID {
			return sub
		}
	}
	return nil
}

func (r *subAgentRegistry) beginTaskActivation(taskID string) (*SubAgent, *subAgentActivation, bool) {
	taskID = strings.TrimSpace(taskID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if activation := r.activations[taskID]; activation != nil {
		return nil, activation, false
	}
	if sub := r.subAgentByTaskIDLocked(taskID); sub != nil {
		return sub, nil, false
	}
	if r.activations == nil {
		r.activations = make(map[string]*subAgentActivation)
	}
	activation := &subAgentActivation{done: make(chan struct{})}
	r.activations[taskID] = activation
	return nil, activation, true
}

func (r *subAgentRegistry) completeTaskActivation(taskID string, activation *subAgentActivation, sub *SubAgent, previousAgentID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activations[taskID] != activation {
		return
	}
	activation.sub = sub
	activation.previousAgentID = previousAgentID
	activation.err = err
	delete(r.activations, taskID)
	close(activation.done)
}

func (r *subAgentRegistry) cancelTaskActivations(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for taskID, activation := range r.activations {
		if activation == nil {
			continue
		}
		activation.cancelled = true
		activation.err = err
		delete(r.activations, taskID)
		close(activation.done)
	}
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
	return r.removeLocked(instanceID)
}

func (r *subAgentRegistry) removeLocked(instanceID string) *SubAgent {
	sub := r.subAgents[instanceID]
	delete(r.subAgents, instanceID)
	delete(r.nudgeCounts, instanceID)
	delete(r.stateEnteredTurn, instanceID)
	return sub
}

func (r *subAgentRegistry) noteStateEnteredTurn(instanceID string, state SubAgentState, turn uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stateEnteredTurn == nil {
		r.stateEnteredTurn = make(map[string]uint64)
	}
	switch state {
	case SubAgentStateRunning:
		delete(r.stateEnteredTurn, instanceID)
	case SubAgentStateWaitingMain, SubAgentStateWaitingDescendant, SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
		r.stateEnteredTurn[instanceID] = turn
	}
}

func (r *subAgentRegistry) stateEnteredTurnFor(instanceID string) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stateEnteredTurn[instanceID]
}

func (r *subAgentRegistry) incrementNudge(instanceID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nudgeCounts[instanceID]++
	return r.nudgeCounts[instanceID]
}

func (r *subAgentRegistry) resetNudge(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nudgeCounts[instanceID]; ok {
		r.nudgeCounts[instanceID] = 0
	}
}

func (r *subAgentRegistry) resetStateEnteredTurns() {
	r.mu.Lock()
	r.stateEnteredTurn = make(map[string]uint64)
	r.mu.Unlock()
}

func (r *subAgentRegistry) removeAllLiveLocked() (ids []string, subs []*SubAgent) {
	ids = make([]string, 0, len(r.subAgents))
	subs = make([]*SubAgent, 0, len(r.subAgents))
	for id, sub := range r.subAgents {
		ids = append(ids, id)
		subs = append(subs, sub)
		r.removeLocked(id)
	}
	return ids, subs
}
