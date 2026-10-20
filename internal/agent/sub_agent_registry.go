package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/tools"
)

type subAgentActivation struct {
	done            chan struct{}
	runtime         subAgentRuntime
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

// subAgentRuntime is the shared orchestration surface the registry stores for
// each live sub-agent runtime. Keeping runtimes behind this interface lets a
// runtime that is not a *SubAgent (an externally executed coding-agent worker)
// join the same orchestration state — admission, task records, mailbox, stall
// scanning, shutdown — without the registry and its shared read paths branching
// on the concrete type.
//
// The set is limited to what orchestration-wide code needs: identity, lifecycle
// state, activity/stall bookkeeping and persistence health. SubAgent-only
// capabilities (context manager, LLM client, turn loop, parking) are reached
// through the typed *SubAgent accessors, which assert the dynamic type.
type subAgentRuntime interface {
	InstanceID() string
	TaskID() string
	AgentDefName() string
	State() SubAgentState
	StateChangedAt() time.Time
	PersistenceHealth() PersistenceHealth
	LastSummary() string
	LastArtifact() tools.ArtifactRef
	TaskDescription() string
	ColorCode() string
	OwnerSnapshot() (ownerAgentID, ownerTaskID string, depth int, joinToOwner bool)
	LastReplyMessageID() string
	markActivity()
	invalidateRuntimeDiagnostics()

	// Admission and durable registration. CreateSubAgent drives both runtime
	// kinds through these so the admission, identity and publish ordering stay
	// shared instead of being reimplemented by each backend.
	injectInitialInput(description string) bool
	removeInitialInput()
	buildTaskRecord(previous *DurableTaskRecord, closedReason string, currentTurn uint64, now time.Time) *DurableTaskRecord
	persistRegistration(sessionDir string, record *DurableTaskRecord) error
	removeRegistration(sessionDir string, taskID string)
	persistRecoverySnapshot(taskID string)
	takeSlot()
	releaseSlot() (held, borrowed, bypassed bool)
	slotHeld() bool
	stallAlertRaised() bool
	raiseStallAlert()
	clearStallAlert()
	startRuntime()
	abortStartup()
}

// asSubAgent extracts the concrete *SubAgent from a registry entry. It returns
// nil for a non-SubAgent runtime so SubAgent-only read paths fail closed rather
// than panicking while another runtime type is registered.
func asSubAgent(rt subAgentRuntime) *SubAgent {
	sub, _ := rt.(*SubAgent)
	return sub
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
	subAgents        map[string]subAgentRuntime     // instanceID → live sub-agent runtime
	taskRecords      map[string]*DurableTaskRecord  // taskID → durable task record
	activations      map[string]*subAgentActivation // taskID → in-flight runtime rehydration
	admissions       map[string]*subAgentAdmission  // taskID → in-flight new-task admission
	stateEnteredTurn map[string]uint64              // agentID → turn it entered a waiting/terminal state
	settlements      map[taskAttemptKey]*TaskSettlement
	agentRequests    map[string]*DurableAgentRequest
	taskRevision     uint64
	taskChanged      chan struct{}
	sessionEpoch     uint64
}

func newSubAgentRegistry() subAgentRegistry {
	return subAgentRegistry{
		subAgents:        make(map[string]subAgentRuntime),
		taskRecords:      make(map[string]*DurableTaskRecord),
		activations:      make(map[string]*subAgentActivation),
		admissions:       make(map[string]*subAgentAdmission),
		stateEnteredTurn: make(map[string]uint64),
		settlements:      make(map[taskAttemptKey]*TaskSettlement),
		agentRequests:    make(map[string]*DurableAgentRequest),
		taskChanged:      make(chan struct{}),
	}
}

func (r *subAgentRegistry) notifyTaskChangeLocked() {
	r.taskRevision++
	if r.taskChanged != nil {
		close(r.taskChanged)
	}
	r.taskChanged = make(chan struct{})
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

// subAgent returns the live SubAgent for instanceID, or nil if none (including
// when the registered runtime is not a *SubAgent).
func (r *subAgentRegistry) subAgent(instanceID string) *SubAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subAgentLocked(instanceID)
}

// runtime returns the live runtime for instanceID, or nil.
func (r *subAgentRegistry) runtime(instanceID string) subAgentRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.subAgents[instanceID]
}

// subAgentLocked is subAgent for callers that already hold r.mu.
func (r *subAgentRegistry) subAgentLocked(instanceID string) *SubAgent {
	return asSubAgent(r.subAgents[instanceID])
}

// runtimeLocked returns the live runtime for instanceID for callers that
// already hold r.mu.
func (r *subAgentRegistry) runtimeLocked(instanceID string) subAgentRuntime {
	return r.subAgents[instanceID]
}

// liveCountLocked returns the number of registered runtimes for callers that
// already hold r.mu.
func (r *subAgentRegistry) liveCountLocked() int {
	return len(r.subAgents)
}

// subAgentsLocked returns the live sub-agents as concrete *SubAgent values for
// SubAgent-only read paths; callers must already hold r.mu.
func (r *subAgentRegistry) subAgentsLocked() []*SubAgent {
	out := make([]*SubAgent, 0, len(r.subAgents))
	for _, rt := range r.subAgents {
		if sub := asSubAgent(rt); sub != nil {
			out = append(out, sub)
		}
	}
	return out
}

// snapshotRuntimes returns a slice copy of the live runtimes so shared read
// paths can iterate them through the orchestration interface without holding
// the lock.
func (r *subAgentRegistry) snapshotRuntimes() []subAgentRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotRuntimesLocked()
}

func (r *subAgentRegistry) snapshotRuntimesLocked() []subAgentRuntime {
	out := make([]subAgentRuntime, 0, len(r.subAgents))
	for _, rt := range r.subAgents {
		if rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

func (r *subAgentRegistry) withSubAgent(instanceID string, fn func(*SubAgent) bool) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub := r.subAgentLocked(instanceID)
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
		if sub != nil && strings.TrimSpace(sub.TaskID()) == taskID {
			return asSubAgent(sub)
		}
	}
	return nil
}

func (r *subAgentRegistry) beginTaskActivation(taskID string) (subAgentRuntime, *subAgentActivation, bool) {
	taskID = strings.TrimSpace(taskID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if activation := r.activations[taskID]; activation != nil {
		return nil, activation, false
	}
	for _, rt := range r.subAgents {
		if rt != nil && rt.TaskID() == taskID {
			return rt, nil, false
		}
	}
	if r.activations == nil {
		r.activations = make(map[string]*subAgentActivation)
	}
	activation := &subAgentActivation{done: make(chan struct{})}
	r.activations[taskID] = activation
	return nil, activation, true
}

func (r *subAgentRegistry) completeTaskActivation(taskID string, activation *subAgentActivation, rt subAgentRuntime, previousAgentID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activations[taskID] != activation {
		return
	}
	activation.runtime = rt
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

// runtimeByTaskID returns the live runtime for taskID, or nil.
func (r *subAgentRegistry) runtimeByTaskID(taskID string) subAgentRuntime {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rt := range r.subAgents {
		if rt != nil && strings.TrimSpace(rt.TaskID()) == taskID {
			return rt
		}
	}
	return nil
}

// snapshotSubAgents returns a slice copy of the live (non-nil) sub-agents so
// callers can iterate without holding the lock.
func (r *subAgentRegistry) snapshotSubAgents() []*SubAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*SubAgent, 0, len(r.subAgents))
	for _, rt := range r.subAgents {
		if sub := asSubAgent(rt); sub != nil {
			out = append(out, sub)
		}
	}
	return out
}

// add registers a live sub-agent runtime under its instance ID.
func (r *subAgentRegistry) add(rt subAgentRuntime) {
	if rt == nil {
		return
	}
	r.mu.Lock()
	r.subAgents[rt.InstanceID()] = rt
	r.mu.Unlock()
}

// removeRuntime deletes a runtime and its per-agent bookkeeping under one
// lock, returning the removed runtime (or nil).
func (r *subAgentRegistry) removeRuntime(instanceID string) subAgentRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeRuntimeLocked(instanceID)
}

func (r *subAgentRegistry) removeRuntimeLocked(instanceID string) subAgentRuntime {
	rt := r.subAgents[instanceID]
	if rt != nil {
		rt.invalidateRuntimeDiagnostics()
	}
	delete(r.subAgents, instanceID)
	delete(r.stateEnteredTurn, instanceID)
	return rt
}

// remove deletes a sub-agent and its per-agent bookkeeping (state-entered-turn)
// under one lock, returning the removed SubAgent (or nil). Consolidating the
// deletes prevents the partial-cleanup hazard of doing them separately.
func (r *subAgentRegistry) remove(instanceID string) *SubAgent {
	return asSubAgent(r.removeRuntime(instanceID))
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

func (r *subAgentRegistry) resetStateEnteredTurns() {
	r.mu.Lock()
	r.stateEnteredTurn = make(map[string]uint64)
	r.mu.Unlock()
}

func (r *subAgentRegistry) removeAllLiveLocked() (ids []string, runtimes []subAgentRuntime) {
	ids = make([]string, 0, len(r.subAgents))
	runtimes = make([]subAgentRuntime, 0, len(r.subAgents))
	for id := range r.subAgents {
		ids = append(ids, id)
		runtimes = append(runtimes, r.subAgents[id])
		r.removeRuntimeLocked(id)
	}
	return ids, runtimes
}
