package agent

import (
	"errors"
	"strings"
	"sync"
	"time"
)

type PersistenceHealthState string

const (
	PersistenceHealthy    PersistenceHealthState = "healthy"
	PersistenceDegraded   PersistenceHealthState = "degraded"
	PersistenceRecovering PersistenceHealthState = "recovering"
)

var errPersistenceQueueUnavailable = errors.New("ordered persistence queue unavailable")

// PersistenceHealth is a read-only snapshot of a SubAgent's durable transcript
// health. Missing state in older sessions is interpreted as healthy.
type PersistenceHealth struct {
	State       PersistenceHealthState `json:"state"`
	LastError   string                 `json:"last_error,omitempty"`
	FailedAt    time.Time              `json:"failed_at"`
	RecoveredAt time.Time              `json:"recovered_at"`
}

type subAgentPersistenceHealth struct {
	mu          sync.RWMutex
	state       PersistenceHealthState
	lastError   string
	failedAt    time.Time
	recoveredAt time.Time
}

func normalizePersistenceHealthState(state PersistenceHealthState) PersistenceHealthState {
	switch state {
	case PersistenceDegraded, PersistenceRecovering:
		return state
	default:
		return PersistenceHealthy
	}
}

func (h *subAgentPersistenceHealth) snapshot() PersistenceHealth {
	if h == nil {
		return PersistenceHealth{State: PersistenceHealthy}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return PersistenceHealth{
		State:       normalizePersistenceHealthState(h.state),
		LastError:   h.lastError,
		FailedAt:    h.failedAt,
		RecoveredAt: h.recoveredAt,
	}
}

func (h *subAgentPersistenceHealth) restore(snapshot PersistenceHealth) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.state = normalizePersistenceHealthState(snapshot.State)
	// A process cannot resume an in-flight recovery operation. Retry it from the
	// durable degraded state when the restored runtime next checkpoints.
	if h.state == PersistenceRecovering {
		h.state = PersistenceDegraded
	}
	h.lastError = strings.TrimSpace(snapshot.LastError)
	h.failedAt = snapshot.FailedAt
	h.recoveredAt = snapshot.RecoveredAt
	h.mu.Unlock()
}

func (h *subAgentPersistenceHealth) markDegraded(err error) bool {
	if h == nil || err == nil {
		return false
	}
	now := time.Now()
	h.mu.Lock()
	changed := normalizePersistenceHealthState(h.state) != PersistenceDegraded || h.lastError != err.Error()
	h.state = PersistenceDegraded
	h.lastError = err.Error()
	h.failedAt = now
	h.mu.Unlock()
	return changed
}

func (h *subAgentPersistenceHealth) beginRecovery() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if normalizePersistenceHealthState(h.state) != PersistenceDegraded {
		return false
	}
	h.state = PersistenceRecovering
	return true
}

func (h *subAgentPersistenceHealth) markRecovered() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.state = PersistenceHealthy
	h.lastError = ""
	h.recoveredAt = time.Now()
	h.mu.Unlock()
}
